package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

// satelled process discovery and restart, kept from the removed update command.

// restartHooks are the seams restartServiceIfRunningRoot calls through instead of
// the real systemctl-touching functions directly. Hermetic tests override these
// (restoring via t.Cleanup) so no test can ever shell out to the operator's actual
// systemctl / actual satelle.service unit (AC6, the plan's "avoid real systemd in
// unit tests") — the fake /proc tree alone is not enough, since the real functions
// name the real serviceUnitName constant. Defaults are the real implementations.
var restartHooks = struct {
	lookSystemctl        func() error
	userUnitActive       func() bool
	userUnitMainPID      func() int
	restartUserUnit      func() error
	systemUnitMainPID    func() int
	systemUnitRestartAlw func() bool
	signalPID            func(int, syscall.Signal) error
	systemUnitRespawned  func(int) (int, bool)
	systemUnitStartLtd   func() bool
}{
	lookSystemctl:        func() error { _, err := exec.LookPath("systemctl"); return err },
	userUnitActive:       userUnitActive,
	userUnitMainPID:      userUnitMainPID,
	restartUserUnit:      func() error { return runCaptureEnv(userEnv(), "systemctl", "--user", "restart", serviceUnitName) },
	systemUnitMainPID:    systemUnitMainPID,
	systemUnitRestartAlw: systemUnitRestartAlways,
	signalPID:            signalPID,
	systemUnitRespawned:  systemUnitRespawned,
	systemUnitStartLtd:   systemUnitStartLimited,
}

// restartServiceIfRunningRootOpt is the injectable core. skipIfCurrent is true
// for `satelle update` (already-current is a no-op) and false for
// `satelle service restart` (the operator asked for a cycle).
func restartServiceIfRunningRootOpt(out io.Writer, procRoot string, skipIfCurrent bool) error {
	if runtime.GOOS != "linux" {
		return nil // cgroup/port discovery and /proc identity are Linux-only (WSL is the story context)
	}
	if restartHooks.lookSystemctl() != nil {
		return nil
	}
	wanted, wantedOK := wantedExeIdentity(procRoot)
	livePID := discoverLivePID(procRoot)

	// Kernel-fact already-current check FIRST (sty_062656a5). A healthy,
	// correctly-versioned process must never be signalled, and must never be
	// told to restart manually — including on a dual-unit host whose system
	// unit still reports a stale MainPID. Gated to the update caller: an
	// explicit `satelle service restart` must still cycle (skipIfCurrent=false).
	if skipIfCurrent && livePID > 0 && wantedOK {
		if live, ok := identityFromPID(procRoot, livePID); ok && identitiesMatch(live, wanted) {
			fmt.Fprintf(out, "%s (pid %d) is already running the new binary — no restart needed\n", serviceUnitName, livePID)
			return nil
		}
	}

	// 1) systemd USER unit — sudo-free when the user manager is alive. Verify by
	// identity, not by the restart command's exit code alone.
	if restartHooks.userUnitActive() {
		if err := restartHooks.restartUserUnit(); err == nil {
			pid := resolveServicePID(procRoot, restartHooks.userUnitMainPID())
			return reportRestartOutcome(out, "user unit", pid, waitForIdentity(procRoot, pid, wanted, wantedOK))
		}
	}
	// 2) SYSTEM unit — sudo-free restart by signalling MainPID; Restart=always respawns.
	// Only enter when the reported MainPID IS the live service process
	// (discoverLivePID). A dead or recycled MainPID, or a dual-unit host where
	// a different pid holds the port, falls through (sty_062656a5).
	if sysPID := restartHooks.systemUnitMainPID(); sysPID > 0 && sysPID == livePID {
		// Only signal a unit that will RESPAWN — a clean SIGTERM to a Restart=on-failure
		// unit would STOP the service, not reload it. Never leave a good release with a
		// dead service; guide the operator to a persistent supervisor instead.
		if !restartHooks.systemUnitRestartAlw() {
			return fmt.Errorf("system unit %s is not Restart=always — a signal would stop it, not respawn. Upgrade it with `satelle service install --system`, or restart manually: sudo systemctl restart %s", serviceUnitName, serviceUnitName)
		}
		if err := restartHooks.signalPID(sysPID, syscall.SIGTERM); err == nil {
			if newPID, respawned := restartHooks.systemUnitRespawned(sysPID); respawned {
				return reportRestartOutcome(out, "system unit", newPID, waitForIdentity(procRoot, newPID, wanted, wantedOK))
			}
			if restartHooks.systemUnitStartLtd() {
				return fmt.Errorf("%s is start-limited (systemd exhausted its restart attempts and gave up) — run: systemctl reset-failed %s && sudo systemctl restart %s", serviceUnitName, serviceUnitName, serviceUnitName)
			}
			return fmt.Errorf("signalled %s (pid %d) but it did not respawn — restart manually: sudo systemctl restart %s", serviceUnitName, sysPID, serviceUnitName)
		}
	}
	// 3) Neither systemctl path is reachable, or the bus-reported pid was dead.
	// Locate the live process WITHOUT the bus (cgroup, then listening-port).
	if livePID == 0 {
		livePID = discoverLivePID(procRoot)
	}
	if livePID == 0 {
		fmt.Fprintf(out, "binary updated, but no running %s process was found — start one with `satelle service install` (user) or `satelle service install --system` (persistent)\n", serviceUnitName)
		return nil
	}
	// A stale process is running and its supervisor cannot be COMMANDED from here —
	// but it does not need to be. A supervisor respawns its own child from the unit
	// file autonomously when that child exits; D-Bus is only the channel an EXTERNAL
	// actor uses to REQUEST a restart. So terminate the stale process and let the
	// supervisor do the rest, on the newly installed binary (sty_f20f3f3b).
	return busFreeRestart(out, procRoot, livePID, wanted, wantedOK)
}

// discoverLivePID finds whichever process is holding the service NOW — cgroup
// first, then listening port. Re-run after a respawn rather than reusing the
// pre-cycle pid: the old pid is gone, and the only thing that matters is what
// currently serves.
func discoverLivePID(procRoot string) int {
	if pid := findPIDByCgroup(procRoot, serviceUnitName); pid > 0 {
		return pid
	}
	return findPIDByListenPort(procRoot, servicePort())
}

// identityFromPID stats procRoot/<pid>/exe directly (NOT a readlink+restat): Linux
// resolves this magic symlink to the process's currently-mapped executable inode
// even after the on-disk path has been renamed away (the exact situation right
// after `satelle update` replaces the binary under a still-running process), so
// this is reliable where readlink's path string ("...(deleted)") is not.
func identityFromPID(procRoot string, pid int) (exeIdentity, bool) {
	if pid <= 0 {
		return exeIdentity{}, false
	}
	return identityFromPath(filepath.Join(procRoot, strconv.Itoa(pid), "exe"))
}

// identitiesMatch compares dev+inode — the same file, however it was reached.
func identitiesMatch(a, b exeIdentity) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino
}

// wantedExeIdentity is the ground truth for "the newly installed binary": the
// ExecStart binary named by whichever unit file is present (user, then system),
// falling back to the CLI install target when no unit file can be read. ok is
// false only when neither source resolves — callers must never treat that as a
// match.
func wantedExeIdentity(procRoot string) (exeIdentity, bool) {
	if content, ok := installedUnitFile(); ok {
		if bin := parseExecStartBinary(content); bin != "" {
			if id, ok := identityFromPath(bin); ok {
				return id, true
			}
		}
	}
	return identityFromPath(installTarget())
}

// unitDirective returns the value of the first `key=` line in a unit file, or ""
// when absent. ONE scanner serves every directive the restart path reads
// (ExecStart, Restart) so the two cannot diverge in whitespace or case handling
// (sty_d45618d5).
func unitDirective(unitContent, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(unitContent, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, prefix))
	}
	return ""
}

// findPIDAndCgroup is findPIDByCgroup plus the cgroup line that matched. The
// path is not incidental detail: it names the SLICE the process sits in, which
// says WHICH installed unit supervises it (a user@<uid>.service slice → the user
// unit, otherwise a system slice) — the fact `satelle service status` needs to
// avoid calling a supervised process ephemeral (sty_29b2af69). One scanner
// serves both callers so the pid and the path can never disagree.
func findPIDAndCgroup(procRoot, unitName string) (int, string) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, ""
	}
	suffix := "/" + unitName
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cgroup"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == unitName || strings.HasSuffix(line, suffix) {
				return pid, line
			}
		}
	}
	return 0, ""
}

// servicePort resolves the configured web-service port, falling back to the
// documented default when global config cannot be loaded.
func servicePort() int {
	port, _ := servicePortResolved()
	return port
}

// findPIDByListenPort locates the process holding a LISTENing TCP socket on port
// by matching procRoot/net/tcp{,6}'s socket inode to a procRoot/<pid>/fd entry.
// Bus-independent fallback for when cgroup discovery finds nothing (e.g. a
// non-systemd supervisor). Best-effort: returns 0 on any lookup failure.
func findPIDByListenPort(procRoot string, port int) int {
	inode := ""
	for _, name := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(filepath.Join(procRoot, "net", name))
		if err != nil {
			continue
		}
		if found := parseListenInode(string(data), port); found != "" {
			inode = found
			break
		}
	}
	if inode == "" {
		return 0
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0
	}
	want := "socket:[" + inode + "]"
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(procRoot, e.Name(), "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(procRoot, e.Name(), "fd", fd.Name()))
			if err == nil && link == want {
				return pid
			}
		}
	}
	return 0
}

// installTarget is the binary update replaces: SATELLE_INSTALL_DIR (else
// ~/.local/bin)/satelle — the same location scripts/install.sh writes.
func installTarget() string {
	dir := os.Getenv("SATELLE_INSTALL_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "bin")
	}
	return filepath.Join(dir, "satelle")
}

// busFreeRestart cycles a supervised process without any systemctl call: signal
// it with the ONE signal its unit's Restart policy respawns from, let the
// supervisor restart it from the unit file, then CONFIRM from kernel facts. It
// never starts a process itself — an ephemeral relaunch would satisfy a version
// check while breaking the durability the release contract requires
// (sty_f20f3f3b), and it never sends a signal whose effect is unknown
// (sty_d45618d5).
func busFreeRestart(out io.Writer, procRoot string, livePID int, wanted exeIdentity, wantedOK bool) error {
	sup, ok := persistentSupervisor(procRoot, livePID)
	if !ok || !sup.Persistent {
		// Nothing would restart it. Signalling here would take the service DOWN and
		// leave it down — strictly worse than stale. Change nothing, say so.
		owner := "its parent is not a systemd manager"
		if ok && sup.Name == "systemd" {
			owner = "its systemd user manager is not lingering, so it dies with the login session"
		}
		msg := fmt.Sprintf("%s (pid %d) is running on the OLD binary, but no persistent supervisor owns it (%s) — a restart would not be respawned, so it was left running. Install a persistent unit, then re-run `satelle update`:\n%s", serviceUnitName, livePID, owner, printInstallRemedy())
		fmt.Fprint(out, msg)
		return fmt.Errorf("%s", strings.TrimRight(msg, "\n"))
	}

	policy, known := unitRestartPolicy()
	sig, willRespawn := restartSignalForPolicy(policy, known)
	if !willRespawn {
		why := fmt.Sprintf("its unit declares Restart=%s", policy)
		if !known {
			why = "its unit file could not be read, so its Restart policy is unknown"
		} else if policy == "" {
			why = "its unit declares no Restart policy"
		}
		msg := fmt.Sprintf("%s (pid %d) is running on the OLD binary, but %s — no signal would make systemd respawn it, so it was left running rather than stopped. Install a unit that restarts, then re-run `satelle update`:\n%s", serviceUnitName, livePID, why, printInstallRemedy())
		fmt.Fprint(out, msg)
		return fmt.Errorf("%s", strings.TrimRight(msg, "\n"))
	}

	start := time.Now()
	fmt.Fprintf(out, "%s (pid %d) is on the old binary; its supervisor (%s, pid %d) is unreachable by bus but Restart=%s respawns on %s — cycling\n",
		serviceUnitName, livePID, sup.Name, sup.PID, policy, sig)

	newPID, ok := signalAndAwaitRespawn(procRoot, livePID, sig)
	if !ok {
		// No second, different signal: the policy already told us which one
		// respawns, so a failure here is a failure to converge, not a reason to
		// try a more destructive one.
		return fmt.Errorf("%s (pid %d) did not respawn after %s — no replacement process is holding the service; start it with `satelle service install --system`",
			serviceUnitName, livePID, time.Since(start).Round(time.Millisecond))
	}

	// CONFIRM from kernel facts. The signal's error value proves nothing about what
	// is running now, so it is not consulted here.
	live, liveOK := identityFromPID(procRoot, newPID)
	if !wantedOK || !liveOK || !identitiesMatch(live, wanted) {
		return fmt.Errorf("%s respawned as pid %d after %s but it is NOT running the newly installed binary (exe identity does not match) — the old binary may still be on PATH ahead of the install",
			serviceUnitName, newPID, time.Since(start).Round(time.Millisecond))
	}
	// The respawn must have come FROM the supervisor. Same parent proves it, and
	// rules out an unrelated process having taken the port.
	if got, ok := persistentSupervisor(procRoot, newPID); !ok || got.PID != sup.PID {
		return fmt.Errorf("%s respawned as pid %d on the new binary, but it was not respawned by the original supervisor (pid %d) — refusing to report a durable restart",
			serviceUnitName, newPID, sup.PID)
	}
	fmt.Fprintf(out, "restarted %s (supervisor respawn, pid %d) onto the new binary in %s\n",
		serviceUnitName, newPID, time.Since(start).Round(time.Millisecond))
	return nil
}

// resolveServicePID honours a bus-reported pid only when it is alive in
// procRoot; otherwise it falls back to discoverLivePID (cgroup, then port).
func resolveServicePID(procRoot string, reported int) int {
	if reported > 0 && pidAlive(procRoot, reported) {
		return reported
	}
	return discoverLivePID(procRoot)
}

// reportRestartOutcome renders the terminal message for a restart that WAS
// commanded (user-unit restart, or a respawned system unit): success only when
// exe identity confirms the live process is the newly installed binary (AC1) —
// a restart command's exit code or a changed PID alone is not proof.
// Failure phrasing returns an error so exit code and output agree (sty_062656a5 AC4).
func reportRestartOutcome(out io.Writer, via string, pid int, matched bool) error {
	switch {
	case pid <= 0:
		msg := fmt.Sprintf("restarted %s (%s) but could not locate its process to confirm the new binary is running", serviceUnitName, via)
		fmt.Fprintln(out, msg)
		return fmt.Errorf("%s", msg)
	case matched:
		fmt.Fprintf(out, "restarted %s (%s, pid %d) onto the new binary\n", serviceUnitName, via, pid)
		return nil
	default:
		msg := fmt.Sprintf("restarted %s (%s, pid %d) but could not confirm it is running the new binary", serviceUnitName, via, pid)
		fmt.Fprintln(out, msg)
		return fmt.Errorf("%s", msg)
	}
}

// waitForIdentity polls the live process's exe identity for a short window,
// matching the existing systemUnitRespawned poll cadence — a fresh exec can take
// a moment to show up. Returns false when either identity is unavailable, never
// treating "unknown" as a match.
func waitForIdentity(procRoot string, pid int, wanted exeIdentity, wantedOK bool) bool {
	if pid <= 0 || !wantedOK {
		return false
	}
	for i := 0; i < identityPollAttempts; i++ {
		if live, ok := identityFromPID(procRoot, pid); ok && identitiesMatch(live, wanted) {
			return true
		}
		time.Sleep(identityPollInterval)
	}
	return false
}

// signalPID sends sig to pid via os.Process.Signal (not syscall.Kill, which is
// Unix-only and breaks the Windows cross-build). The systemd restart path only runs
// on Linux; this just has to COMPILE on Windows, where Signal returns unsupported.
func signalPID(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}

// userUnitActive reports whether the systemd USER unit is active, defaulting the
// user-session env so a headless shell can reach the user bus.
func userUnitActive() bool {
	c := exec.Command("systemctl", "--user", "is-active", serviceUnitName)
	c.Env = userEnv()
	out, _ := c.Output()
	return strings.TrimSpace(string(out)) == "active"
}

// systemUnitMainPID returns the MainPID of the ACTIVE system unit, or 0 when the
// unit is not active. Read-only (`systemctl show` needs no sudo).
func systemUnitMainPID() int {
	act, _ := exec.Command("systemctl", "is-active", serviceUnitName).Output()
	if strings.TrimSpace(string(act)) != "active" {
		return 0
	}
	out, _ := exec.Command("systemctl", "show", "--property=MainPID", "--value", serviceUnitName).Output()
	return parseMainPID(string(out))
}

// systemUnitRestartAlways reports whether the system unit's Restart policy is
// "always" — the precondition for a sudo-free SIGTERM restart to RESPAWN rather
// than stop the service. Read-only (no sudo).
func systemUnitRestartAlways() bool {
	out, _ := exec.Command("systemctl", "show", "--property=Restart", "--value", serviceUnitName).Output()
	return strings.TrimSpace(string(out)) == "always"
}

// systemUnitRespawned confirms the system unit came back on a DIFFERENT MainPID
// after the SIGTERM — proof the supervisor respawned it (Restart=always) rather
// than it being a Restart=on-failure unit that stopped on the clean signal. Returns
// the new PID so the caller can verify exe identity on it (AC1) — a changed PID
// alone is evidence of a respawn, not evidence of WHICH binary it respawned onto.
func systemUnitRespawned(oldPID int) (int, bool) {
	for i := 0; i < 15; i++ {
		time.Sleep(300 * time.Millisecond)
		if np := systemUnitMainPID(); np > 0 && np != oldPID {
			return np, true
		}
	}
	return 0, false
}

// userUnitMainPID returns the MainPID of the ACTIVE user unit, or 0. Mirrors
// systemUnitMainPID but through the user-session systemctl (userEnv()).
func userUnitMainPID() int {
	c := exec.Command("systemctl", "--user", "show", "--property=MainPID", "--value", serviceUnitName)
	c.Env = userEnv()
	out, _ := c.Output()
	return parseMainPID(string(out))
}

// systemUnitStartLimited reports whether the system unit has exhausted systemd's
// restart attempts (StartLimitBurst) and given up — a distinct, nameable failure
// mode (AC4) rather than a silent fold into "did not respawn".
func systemUnitStartLimited() bool {
	out, _ := exec.Command("systemctl", "show", "--property=Result", "--value", serviceUnitName).Output()
	return parseUnitStartLimited(string(out))
}

// exeIdentity is the on-disk identity of an executable: device+inode is the
// authoritative comparison (survives a rename-over-target replace); size is cheap
// corroboration only.
type exeIdentity struct {
	Dev, Ino uint64
	Size     int64
}

// identityFromPath stats path (following symlinks) and extracts dev+inode via the
// platform-specific statIdentity (cmd_update_identity_*.go — dev+inode has no
// portable representation, e.g. Windows' os.FileInfo carries no syscall.Stat_t).
// ok is false when the path cannot be stat'd or the platform cannot report identity.
func identityFromPath(path string) (id exeIdentity, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return exeIdentity{}, false
	}
	dev, ino, ok := statIdentity(info)
	if !ok {
		return exeIdentity{}, false
	}
	return exeIdentity{Dev: dev, Ino: ino, Size: info.Size()}, true
}

// parseExecStartBinary extracts the binary path from a unit file's ExecStart=
// line (the first whitespace-delimited token). Pure/unit-tested.
func parseExecStartBinary(unitContent string) string {
	return firstToken(unitDirective(unitContent, "ExecStart"))
}

// findPIDByCgroup scans procRoot/<pid>/cgroup for a line naming unitName (e.g.
// ".../app.slice/satelle.service"), bounded so a substring collision (e.g. a unit
// named "notsatelle.service") does not false-match. Returns 0 when not found.
// Bus-independent: cgroup membership is a kernel fact, not a systemctl query.
func findPIDByCgroup(procRoot, unitName string) int {
	pid, _ := findPIDAndCgroup(procRoot, unitName)
	return pid
}

// servicePortResolved is servicePort plus whether the global config actually
// loaded. Callers that must not fabricate certainty from a fallback port read
// the flag (sty_fb5e6d96); the rest keep using servicePort. One resolution path
// serves both, so no surface can drift onto a different port.
func servicePortResolved() (int, bool) {
	gc, err := config.LoadGlobal()
	if err != nil {
		return config.DefaultWebPort, false
	}
	return gc.Service.ResolvePort(), true
}

// parseListenInode finds the socket inode for a LISTEN-state (0A) row in
// /proc/net/tcp{,6} whose local port matches. Pure/unit-tested. Format:
// "  sl  local_address rem_address   st ... inode ...", local_address is
// "hex_ip:hex_port".
func parseListenInode(procNetTCP string, port int) string {
	wantPort := strings.ToUpper(strconv.FormatInt(int64(port), 16))
	lines := strings.Split(procNetTCP, "\n")
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		local := fields[1]
		st := fields[3]
		i := strings.LastIndexByte(local, ':')
		if i < 0 || !strings.EqualFold(local[i+1:], wantPort) {
			continue
		}
		if !strings.EqualFold(st, "0A") { // TCP_LISTEN
			continue
		}
		return fields[9]
	}
	return ""
}

// restartSignalForPolicy maps a unit's Restart= policy to the signal that makes
// systemd RESPAWN that unit — or reports that no signal does (sty_d45618d5).
//
// This is the correction to v0.0.361, which sent SIGTERM first and escalated to
// SIGKILL. systemd classifies SIGTERM (with SIGHUP/SIGINT/SIGPIPE) as a CLEAN
// exit, so an on-failure unit does not respawn from it — it STOPS, permanently,
// and the escalation cannot recover it because the process is already gone. A
// signal is therefore only ever sent when its effect on THIS policy is known:
//
//   - always     → SIGTERM. Graceful, and it respawns.
//   - on-failure → SIGKILL. The only signal systemd counts as a failure, hence
//     the only one this unit respawns from. Not an escalation — the correct
//     first and only signal for this policy.
//   - anything else (no, absent, unreadable, or a conditional policy whose
//     respawn semantics are not established: on-abnormal, on-abort, on-success,
//     on-watchdog) → NO signal. Stopping a service nothing will restart is
//     strictly worse than leaving it stale.
//
// known distinguishes "the unit declares Restart=no" from "we could not read the
// unit at all". Both forbid signalling, so the signal decision is the same; the
// caller keeps the distinction for its message.
func restartSignalForPolicy(policy string, known bool) (syscall.Signal, bool) {
	if !known {
		return 0, false
	}
	switch policy {
	case "always":
		return syscall.SIGTERM, true
	case "on-failure":
		return syscall.SIGKILL, true
	}
	return 0, false
}

// printInstallRemedy names the one-time fix for a service nothing would respawn.
// Shared by both refusal branches so "the same remedy" is structural rather than
// two strings that can drift apart. Returned so it travels with the error (sty_062656a5 AC4).
func printInstallRemedy() string {
	return "  satelle service install            (user unit; needs lingering)\n" +
		"  satelle service install --system   (persistent system unit)\n"
}

// signalAndAwaitRespawn signals pid and waits a BOUNDED window for a different
// process to be holding the service. Returns the new pid and whether one
// appeared. The wait reuses the identity poll cadence, so tests shrink it the
// same way they shrink waitForIdentity.
func signalAndAwaitRespawn(procRoot string, pid int, sig syscall.Signal) (int, bool) {
	if err := restartHooks.signalPID(pid, sig); err != nil {
		return 0, false
	}
	for i := 0; i < identityPollAttempts; i++ {
		if newPID := discoverLivePID(procRoot); newPID > 0 && newPID != pid {
			return newPID, true
		}
		time.Sleep(identityPollInterval)
	}
	return 0, false
}

// pidAlive reports whether /proc/<pid> exists under procRoot. A bus-reported
// MainPID that is not alive is stale (sty_062656a5).
func pidAlive(procRoot string, pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.Stat(filepath.Join(procRoot, strconv.Itoa(pid)))
	return err == nil
}

// parseMainPID extracts a PID from `systemctl show --property=MainPID` output —
// either "1234" (with --value) or "MainPID=1234". Pure/unit-tested. Returns 0 for
// none/PID 0 (inactive) or PID 1 (never our serve).
func parseMainPID(s string) int {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '='); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 1 {
		return 0
	}
	return n
}

// parseUnitStartLimited is the pure classifier: `systemctl show --property=Result`
// reports "start-limit-hit" when StartLimitBurst was exhausted. Pure/unit-tested.
func parseUnitStartLimited(result string) bool {
	return strings.TrimSpace(strings.ToLower(result)) == "start-limit-hit"
}

// firstToken returns s up to the first space or tab.
func firstToken(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// identityPollAttempts/identityPollInterval bound waitForIdentity's poll — vars
// so hermetic tests shrink the window.
var (
	identityPollAttempts = 15
	identityPollInterval = 300 * time.Millisecond
)
