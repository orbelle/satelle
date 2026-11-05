package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

func TestSystemdUnitContent(t *testing.T) {
	unit := systemdUnit("/usr/local/bin/satelled", "/home/u/repo", "0.0.0.0", 8787)
	for _, want := range []string{
		"Description=satelled",
		"ExecStart=/usr/local/bin/satelled --addr 0.0.0.0 --port 8787",
		"WorkingDirectory=", // $HOME preferred; not a single repo (sty_dbdadfa0)
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "WorkingDirectory=/home/u/repo") {
		t.Errorf("unit must not pin per-repo WorkingDirectory:\n%s", unit)
	}
}

func TestJoinArgs(t *testing.T) {
	got := joinArgs([]string{"systemctl", "--user", "enable", "--now", "satelle.service"})
	if got != "systemctl --user enable --now satelle.service" {
		t.Errorf("joinArgs = %q", got)
	}
}

// TestUserSystemctlEnv: the systemd user-session env is defaulted when absent (so
// a headless shell reaches the user bus) and preserved when already set (sty_00dadc91).
func TestUserSystemctlEnv(t *testing.T) {
	// (a) empty env → both defaults for the given uid.
	got := userSystemctlEnv(nil, "1000")
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"XDG_RUNTIME_DIR=/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("defaulted env missing %q:\n%s", want, joined)
		}
	}
	// (b) existing values are preserved, not duplicated or overwritten.
	pre := []string{"XDG_RUNTIME_DIR=/custom/run", "DBUS_SESSION_BUS_ADDRESS=unix:path=/custom/bus", "PATH=/bin"}
	out := userSystemctlEnv(pre, "1000")
	if len(out) != len(pre) {
		t.Errorf("preset env should be unchanged; got %d entries, want %d:\n%v", len(out), len(pre), out)
	}
	j := strings.Join(out, "\n")
	if strings.Contains(j, "/run/user/1000") {
		t.Errorf("preset env should NOT be overwritten with the /run/user default:\n%s", j)
	}
}

// TestSystemSystemdUnit: the persistent system unit targets multi-user.target and
// runs as the given user, with the correct ExecStart (sty_00dadc91). WorkingDirectory
// is $HOME (push-fed serve needs no per-repo cwd; sty_dbdadfa0 / sty_455f0d6e).
func TestSystemSystemdUnit(t *testing.T) {
	unit := systemSystemdUnit("/usr/local/bin/satelled", "/home/u/repo", "0.0.0.0", 8787, "bobmc")
	for _, want := range []string{
		"ExecStart=/usr/local/bin/satelled --addr 0.0.0.0 --port 8787",
		"WorkingDirectory=", // non-empty home or repo fallback
		"User=bobmc",
		"Group=bobmc",
		"WantedBy=multi-user.target",
		"Restart=always", // sudo-free restart: a clean SIGTERM must respawn, not stop (sty_1ac9f095)
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("system unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "WorkingDirectory=/home/u/repo") {
		t.Errorf("system unit must not pin a single repo WorkingDirectory (push-fed serve):\n%s", unit)
	}
	if strings.Contains(unit, "Restart=on-failure") {
		t.Errorf("system unit must be Restart=always (a clean signal respawns), not on-failure:\n%s", unit)
	}
	// The per-user unit must NOT carry User=/multi-user.target, and is Restart=always
	// so `satelle update`'s bus-free cycle respawns on a GRACEFUL stop (sty_f20f3f3b).
	userUnit := systemdUnit("/usr/local/bin/satelle", "/home/u/repo", "0.0.0.0", 8787)
	if strings.Contains(userUnit, "User=") || strings.Contains(userUnit, "multi-user.target") {
		t.Errorf("per-user unit should stay default.target with no User=:\n%s", userUnit)
	}
	if !strings.Contains(userUnit, "Restart=always") {
		t.Errorf("per-user unit must be Restart=always so a graceful stop respawns:\n%s", userUnit)
	}
}

// TestSystemInstallSteps pins the exact sudo command plan for a system-unit install.
func TestSystemInstallSteps(t *testing.T) {
	steps := systemInstallSteps("/tmp/x.service", "/etc/systemd/system/satelle.service")
	if len(steps) != 4 {
		t.Fatalf("want 4 steps, got %d: %v", len(steps), steps)
	}
	want := []string{
		"sudo install -m 0644 /tmp/x.service /etc/systemd/system/satelle.service",
		"sudo systemctl daemon-reload",
		"sudo systemctl enable satelle.service",  // persist across boot
		"sudo systemctl restart satelle.service", // load the new binary NOW (not enable --now, which no-ops a running unit)
	}
	for i, w := range want {
		if got := joinArgs(steps[i]); got != w {
			t.Errorf("step %d = %q, want %q", i, got, w)
		}
	}
}

// TestRunCaptureEnvFoldsStderr guards the regression that made busUnreachable dead
// code: the runner must fold a command's STDERR into the error (runQuiet discards
// it), so a "Failed to connect to bus" diagnostic survives for classification.
func TestRunCaptureEnvFoldsStderr(t *testing.T) {
	err := runCaptureEnv(nil, "sh", "-c", "echo 'Failed to connect to bus' >&2; exit 1")
	if err == nil {
		t.Fatal("want an error from a failing command")
	}
	if !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Errorf("stderr not folded into error: %v", err)
	}
	if err := runCaptureEnv(nil, "sh", "-c", "exit 0"); err != nil {
		t.Errorf("success command should return nil, got %v", err)
	}
}

// TestBusUnreachable: a bus-connection failure is distinguished from a genuine
// enable/restart fault so install can point at the --system fallback.
func TestBusUnreachable(t *testing.T) {
	if !busUnreachable([]string{"systemctl --user daemon-reload: Failed to connect to bus: No such file or directory"}) {
		t.Error("a 'connect to bus' failure should be classed unreachable")
	}
	if busUnreachable([]string{"systemctl --user enable satelle.service: Unit satelle.service is masked"}) {
		t.Error("a plain enable fault should NOT be classed as bus-unreachable")
	}
	if busUnreachable(nil) {
		t.Error("no failures → not unreachable")
	}
}

// --- serviceStatusLine end-to-end scenarios (sty_acd4b61e AC1/AC5) ---
//
// Every case overrides serviceStatusHooks so NOTHING here ever shells out to the
// real systemctl or touches the operator's actual satelle.service unit — the same
// discipline restartServiceIfRunningRoot's tests already follow (sty_c344d080 AC6).

func withServiceStatusHooks(t *testing.T, h struct {
	userIsActive   func() (string, bool)
	systemIsActive func() (string, bool)
	systemStartLtd func() bool
}) {
	t.Helper()
	prev := serviceStatusHooks
	serviceStatusHooks = h
	t.Cleanup(func() { serviceStatusHooks = prev })
}

func unreachableHooks() struct {
	userIsActive   func() (string, bool)
	systemIsActive func() (string, bool)
	systemStartLtd func() bool
} {
	return struct {
		userIsActive   func() (string, bool)
		systemIsActive func() (string, bool)
		systemStartLtd func() bool
	}{
		userIsActive:   func() (string, bool) { return "", false },
		systemIsActive: func() (string, bool) { return "", false },
		systemStartLtd: func() bool { return false },
	}
}

// writeFakeUserUnit installs a fake ~/.config/systemd/user/satelle.service naming
// execStart as its ExecStart binary, isolating HOME so the real operator unit is
// never read. Returns the unit path.
// isolateSystemUnitDir points systemUnitPath() at a temp dir so no test reads the
// operator's real /etc/systemd/system/satelle.service (sty_d50218d1). Returns the
// dir so a test can plant a fixture unit in it. Mirrors the package's existing
// isolation idiom: a package var swapped and restored via t.Cleanup, like
// restartHooks. Tests using it must not call t.Parallel() — the var is global.
func isolateSystemUnitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := systemUnitDir
	systemUnitDir = dir
	t.Cleanup(func() { systemUnitDir = prev })
	return dir
}

func writeFakeUserUnit(t *testing.T, execStart string) string {
	t.Helper()
	testutil.IsolateHome(t) // servicePort()'s config.LoadGlobal() needs SATELLE_HOME set
	isolateSystemUnitDir(t) // no real /etc/systemd/system read (sty_d50218d1)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, serviceUnitName)
	content := "[Service]\nExecStart=" + execStart + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFakeSystemUnit plants a fake system unit in the ALREADY-isolated
// systemUnitDir, so a test can model a dual-unit host (sty_29b2af69). It refuses
// to run against the real /etc/systemd/system rather than silently reading the
// operator's machine (the discipline sty_d50218d1 established).
func writeFakeSystemUnit(t *testing.T, execStart string) string {
	t.Helper()
	if systemUnitDir == "/etc/systemd/system" {
		t.Fatal("writeFakeSystemUnit: call isolateSystemUnitDir(t) first — never write the real system unit dir")
	}
	path := systemUnitPath()
	content := "[Service]\nExecStart=" + execStart + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestServiceStatusLine_NotInstalled: (a) no unit file — never derived from a
// systemctl query, purely a filesystem fact.
func TestServiceStatusLine_NotInstalled(t *testing.T) {
	testutil.IsolateHome(t)
	t.Setenv("HOME", t.TempDir()) // empty HOME: no ~/.config/systemd/user/satelle.service
	isolateSystemUnitDir(t)       // empty system dir too: "not installed" must mean NEITHER unit exists
	withServiceStatusHooks(t, unreachableHooks())
	got := serviceStatusLine(t.TempDir())
	if got != "not installed" {
		t.Errorf("serviceStatusLine = %q, want %q", got, "not installed")
	}
	// AC1: status reports the local tier as satelled, not "the satelle server".
	report := serviceStatusReport(t.TempDir())
	if !strings.HasPrefix(report, "satelled — ") {
		t.Errorf("serviceStatusReport = %q, want prefix satelled —", report)
	}
	if !strings.Contains(report, "not installed") {
		t.Errorf("serviceStatusReport = %q, want the kernel-derived state", report)
	}
}

// TestServiceStatusLine_InstalledNotRunning: (b) unit installed, no process found
// by kernel facts, supervisor reachable and confirms inactive.
func TestServiceStatusLine_InstalledNotRunning(t *testing.T) {
	writeFakeUserUnit(t, "/opt/bin/satelled")
	withServiceStatusHooks(t, struct {
		userIsActive   func() (string, bool)
		systemIsActive func() (string, bool)
		systemStartLtd func() bool
	}{
		userIsActive:   func() (string, bool) { return "inactive", true },
		systemIsActive: func() (string, bool) { return "", false },
		systemStartLtd: func() bool { return false },
	})
	got := serviceStatusLine(t.TempDir()) // empty /proc: no live PID anywhere
	if got != "installed, not running" {
		t.Errorf("serviceStatusLine = %q, want %q", got, "installed, not running")
	}
}

// TestServiceStatusLine_StartLimited: (e) unit installed, not running, and
// systemd reports it start-limited — named explicitly, not folded into "stopped".
func TestServiceStatusLine_StartLimited(t *testing.T) {
	writeFakeUserUnit(t, "/opt/bin/satelled")
	withServiceStatusHooks(t, struct {
		userIsActive   func() (string, bool)
		systemIsActive func() (string, bool)
		systemStartLtd func() bool
	}{
		userIsActive:   func() (string, bool) { return "failed", true },
		systemIsActive: func() (string, bool) { return "", false },
		systemStartLtd: func() bool { return true },
	})
	got := serviceStatusLine(t.TempDir())
	if !strings.Contains(got, "start-limited") {
		t.Errorf("serviceStatusLine = %q, want it to name start-limited", got)
	}
}

// TestServiceStatusLine_EphemeralOutsideSupervision: a process is physically
// found (via the listening port, not the unit's cgroup) while the bus IS
// reachable and reports the unit inactive — an ephemeral relaunch outside the
// installed unit's supervision, the state release.md's dogfood forbids as a
// final state. Must be named distinctly from both "active" and "unreachable".
func TestServiceStatusLine_EphemeralOutsideSupervision(t *testing.T) {
	writeFakeUserUnit(t, "/opt/bin/satelled")
	withServiceStatusHooks(t, struct {
		userIsActive   func() (string, bool)
		systemIsActive func() (string, bool)
		systemStartLtd func() bool
	}{
		userIsActive:   func() (string, bool) { return "inactive", true },
		systemIsActive: func() (string, bool) { return "inactive", true },
		systemStartLtd: func() bool { return false },
	})
	procRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procRoot, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	port := 8787
	body := "  sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:" + strconv.FormatInt(int64(port), 16) + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 88888 1 0 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(procRoot, "net", "tcp"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fdDir := filepath.Join(procRoot, "777", "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[88888]", filepath.Join(fdDir, "3")); err != nil {
		t.Fatal(err)
	}
	got := serviceStatusLine(procRoot)
	if !strings.Contains(got, "ephemeral") {
		t.Errorf("serviceStatusLine = %q, want the ephemeral/outside-supervision state named", got)
	}
	if strings.Contains(got, "active (") {
		t.Errorf("serviceStatusLine = %q must not report active when the unit itself reports inactive", got)
	}
	// sty_29b2af69 AC2: a port-located process has no cgroup naming a unit, so
	// the verdict must not name a supervisor either — that claim belongs only to
	// cgroup-located processes.
	if strings.Contains(got, "supervised by the installed") {
		t.Errorf("serviceStatusLine = %q must not name a supervising unit for a process found only by listening port", got)
	}
}

// TestCgroupOwnerUnit covers the classifier itself: which installed unit a
// cgroup path names, and the cases where it must make no claim at all.
func TestCgroupOwnerUnit(t *testing.T) {
	for _, tc := range []struct {
		name          string
		path          string
		userInstalled bool
		sysInstalled  bool
		want          string
	}{
		{"user slice, user unit installed", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/" + serviceUnitName, true, false, "user unit"},
		{"system slice, system unit installed", "0::/system.slice/" + serviceUnitName, false, true, "system unit"},
		{"user slice but no user unit on disk", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/" + serviceUnitName, false, true, ""},
		{"system slice but no system unit on disk", "0::/system.slice/" + serviceUnitName, true, false, ""},
		{"no cgroup path (found by listening port)", "", true, true, ""},
		// Segment-bounded: "user@" inside another name is not a user slice.
		{"user@ only as a substring", "0::/system.slice/notauser@host.mount/" + serviceUnitName, false, true, "system unit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cgroupOwnerUnit(tc.path, tc.userInstalled, tc.sysInstalled); got != tc.want {
				t.Errorf("cgroupOwnerUnit(%q, %v, %v) = %q, want %q", tc.path, tc.userInstalled, tc.sysInstalled, got, tc.want)
			}
		})
	}
}

// TestSystemUnitPathIsInjected guards the isolation itself (sty_d50218d1 AC4).
// Without it, a future change reverting systemUnitPath() to a hardcoded
// /etc/systemd/system would silently reintroduce the defect: the six tests it
// broke would pass again on a CI runner (which has no system unit) and fail only
// on a developer machine that does.
//
// HOME is emptied so the USER unit cannot shadow the system one — otherwise this
// would pass even if the system path were still hardcoded.
func TestSystemUnitPathIsInjected(t *testing.T) {
	testutil.IsolateHome(t)
	t.Setenv("HOME", t.TempDir()) // no ~/.config/systemd/user/satelle.service
	dir := isolateSystemUnitDir(t)

	if got := systemUnitPath(); got != filepath.Join(dir, serviceUnitName) {
		t.Fatalf("systemUnitPath() = %q, want it under the injected dir %q", got, dir)
	}
	if strings.HasPrefix(systemUnitPath(), "/etc/") {
		t.Fatal("systemUnitPath() still resolves under the real /etc — the injection is not wired")
	}

	// With no unit planted, the reader must take the no-unit path.
	if content, ok := installedUnitFile(); ok {
		t.Fatalf("installedUnitFile() found a unit in an empty sandbox: %q", content)
	}
	if _, known := unitRestartPolicy(); known {
		t.Error("unitRestartPolicy() must report unknown when no unit file exists")
	}

	// Plant a fixture at the injected path: the readers must pick up THAT file,
	// with a value the operator's real unit would not produce.
	fixture := "[Service]\nExecStart=/nowhere/fixture-serve --port 1\nRestart=on-abnormal\n"
	if err := os.WriteFile(filepath.Join(dir, serviceUnitName), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	content, ok := installedUnitFile()
	if !ok || !strings.Contains(content, "/nowhere/fixture-serve") {
		t.Fatalf("installedUnitFile() did not read the injected fixture: ok=%v content=%q", ok, content)
	}
	policy, known := unitRestartPolicy()
	if !known || policy != "on-abnormal" {
		t.Errorf("unitRestartPolicy() = (%q,%v), want (\"on-abnormal\",true) from the fixture", policy, known)
	}
	if got := parseExecStartBinary(content); got != "/nowhere/fixture-serve" {
		t.Errorf("parseExecStartBinary = %q, want the fixture's binary", got)
	}
}

// TestSystemUnitDirDefaultsToRealPath — production must still write and read the
// real location; the injection point is test-only and has no env or flag knob.
func TestSystemUnitDirDefaultsToRealPath(t *testing.T) {
	if systemUnitDir != "/etc/systemd/system" {
		t.Errorf("systemUnitDir default = %q, want /etc/systemd/system", systemUnitDir)
	}
}
