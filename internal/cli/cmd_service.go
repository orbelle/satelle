// `satelle service` — install/manage the background web service so the project
// page stays up without an open terminal. On Linux/WSL it manages a systemd
// unit; the global config (~/.satelle/config.toml) holds the port/addr/repo so
// they survive reinstalls and are editable. Native Windows has no systemd, so
// install there prints Task Scheduler guidance instead.

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/health"
)

// serviceUnitName is the on-disk unit filename. Kept as satelle.service so
// already-installed units and cgroup-path matchers keep working; the unit
// *references* satelled via Description and ExecStart (sty_bd9de06d).
const serviceUnitName = "satelle.service"

func init() {
	svc := &cobra.Command{
		Use:   "service",
		Short: "Manage satelled (always-on project page)",
		Long: `Manage the background web service that serves the always-on project page.

install puts it under a persistent supervisor; status reports what is actually
running; restart cycles it onto the installed binary. A persistent supervisor is
the point: an ephemeral process dies with your shell, and satelle update cannot
cycle a service nothing owns.`,
	}
	svc.AddCommand(serviceInstallCmd(), serviceUninstallCmd(), serviceStatusCmd(), serviceRestartCmd())
	register(svc)
}

func serviceInstallCmd() *cobra.Command {
	var port int
	var addr, repo, serveBinFlag string
	var system bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start the background web service (systemd user unit)",
		Long: `install resolves the service settings (flags > ~/.satelle/config.toml >
defaults), saves them to the global config, and installs a systemd user service
that runs satelled for the chosen repo — so the project page stays up
across terminals and WSL restarts, reachable from a Windows browser.

Re-running after 'make install' restarts the unit so the live process loads the
new binary (release dogfood). systemctl --user calls are run with the systemd
user-session env defaulted (XDG_RUNTIME_DIR / DBUS_SESSION_BUS_ADDRESS), so a
headless / non-login agent shell with a running user manager still succeeds.

When the user bus is genuinely unreachable (no running user manager), the binary
still installed fine — install says so and points at the persistent fallback
rather than reporting a failed binary. For a supervisor that SURVIVES session
loss, use --system: it installs a system unit (WantedBy=multi-user.target,
running as you) via sudo. This is also the persistent supervisor for the
connected-projects fleet. On native Windows, install prints Task Scheduler
guidance and exits non-zero until the service is managed by a real install path.

Change the port later by editing ~/.satelle/config.toml (or passing --port) and
re-running 'satelle service install'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			self, err := resolveSelfPath()
			if err != nil {
				return err
			}
			bin, viaFallback := resolveServeBinary(self, serveBinFlag, nil)
			if viaFallback {
				fmt.Fprintln(out, "service: satelled not found next to this binary — the unit uses satelled from PATH")
			}

			// Resolve settings: flags override the saved global config; repo
			// defaults to the saved value, else the current directory.
			gc, _ := config.LoadGlobal()
			if cmd.Flags().Changed("port") {
				gc.Service.Port = port
			}
			if cmd.Flags().Changed("addr") {
				gc.Service.Addr = addr
			}
			resolvedRepo := repo
			if resolvedRepo == "" {
				resolvedRepo = gc.Service.Repo
			}
			if resolvedRepo == "" {
				if wd, werr := os.Getwd(); werr == nil {
					resolvedRepo = wd
				}
			}
			if abs, aerr := filepath.Abs(resolvedRepo); aerr == nil {
				resolvedRepo = abs
			}
			gc.Service.Repo = resolvedRepo
			if err := config.SaveGlobal(gc); err != nil {
				return err
			}
			rPort, rAddr := gc.Service.ResolvePort(), gc.Service.ResolveAddr()
			fmt.Fprintf(out, "config: %s (port %d, addr %s, repo %s)\n",
				config.GlobalConfigPath(), rPort, rAddr, resolvedRepo)
			fmt.Fprintf(out, "binary: %s\n", bin)

			unit := systemdUnit(bin, resolvedRepo, rAddr, rPort)

			// Platform branch. Install must actually start (or restart) the service
			// for dogfood — soft success with printed guidance is a failed install
			// (release treats local install failure as release failure).
			if runtime.GOOS == "windows" {
				printWindowsGuidance(out, bin, resolvedRepo, rAddr, rPort)
				return fmt.Errorf("service: native Windows has no systemd install path — configure Task Scheduler as printed, then re-run when the service is managed")
			}
			if _, lerr := exec.LookPath("systemctl"); lerr != nil {
				printNoSystemdGuidance(out, unit)
				return fmt.Errorf("service: systemctl not found — enable systemd or install systemctl, then re-run satelle service install")
			}
			// --system installs a PERSISTENT system unit (survives session loss) via
			// sudo — the honest path when the user bus can't be brought up.
			if system {
				return installSystemUnit(out, systemSystemdUnit(bin, resolvedRepo, rAddr, rPort, currentUsername()), rPort)
			}
			return installUserUnit(out, unit, rPort, rAddr)
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "service port (default 8787 or saved global config)")
	cmd.Flags().StringVar(&addr, "addr", "", "bind address (default 0.0.0.0 — reachable from Windows)")
	cmd.Flags().StringVar(&repo, "repo", "", "repo to serve (default: current directory or saved config)")
	cmd.Flags().StringVar(&serveBinFlag, "serve-bin", "", "path to satelled binary (default: sibling of this binary)")
	cmd.Flags().BoolVar(&system, "system", false, "install a persistent system unit via sudo (survives session loss; needs sudo)")
	return cmd
}

func serviceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the background web service",
		Long: `Stop the background web service and remove its unit.

The project page stops being served; nothing in the repo or the database is
touched, so this is reversible with install. Reach for it to stop serving on a
machine, not to fix a bad build — restart cycles onto a new binary without
tearing the unit down.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if runtime.GOOS == "windows" {
				fmt.Fprintln(out, "On Windows, remove the satelle task from Task Scheduler.")
				return nil
			}
			if _, err := exec.LookPath("systemctl"); err != nil {
				fmt.Fprintln(out, "systemctl not found — nothing to uninstall.")
				return nil
			}
			env := userEnv()
			_ = runQuietEnv(env, "systemctl", "--user", "disable", "--now", serviceUnitName)
			unitPath := userUnitPath()
			if err := os.Remove(unitPath); err == nil {
				fmt.Fprintf(out, "removed %s\n", unitPath)
			}
			_ = runQuietEnv(env, "systemctl", "--user", "daemon-reload")
			fmt.Fprintln(out, "service uninstalled.")
			return nil
		},
	}
}

func serviceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show satelled status (unit, live process, installed binary)",
		Long: `Report what is actually serving: the unit on disk, the live process, and whether
that process runs the INSTALLED binary.

Bus-independent by design — it reads the unit file and finds the process by
cgroup or listening port rather than trusting a systemctl query, so an
unreachable user bus is reported as "restart control unavailable", not as a
stopped service. That distinction is the whole value when a release goes wrong.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if runtime.GOOS == "windows" {
				fmt.Fprintln(out, "On Windows, check the satelle task in Task Scheduler.")
				return nil
			}
			if _, err := exec.LookPath("systemctl"); err != nil {
				fmt.Fprintln(out, "systemctl not found.")
				return nil
			}
			gc, _ := config.LoadGlobal()
			fmt.Fprintf(out, "service: %s\n", serviceStatusReport("/proc"))
			fmt.Fprintf(out, "config:  %s (port %d, addr %s, repo %s)\n",
				config.GlobalConfigPath(), gc.Service.ResolvePort(), gc.Service.ResolveAddr(), gc.Service.Repo)
			fmt.Fprintf(out, "url:     http://localhost:%d\n", gc.Service.ResolvePort())
			printRegisteredRepoHealth(out)
			return nil
		},
	}
}

// serviceRestartCmd is the remedy `service status` names when it reports a stale
// process (sty_a7b2cd3c). It is deliberately NARROW: replacing the binary is what
// leaves the running service on the old image, and the operator in that state has
// an already-current binary and a stale process. `satelle update` would work, but
// it is a wide instrument — it queries GitHub and may install a release — so
// naming it as the fix for "stale process" tells the operator to run a network
// release check to solve a local process problem.
//
// It reuses the SAME restart path `satelle update` uses; it adds no systemctl
// call, no signal logic and no identity comparison of its own.
func serviceRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the background web service onto the installed binary",
		Long: `restart cycles the running satelled service so it executes the binary
that is currently installed, and VERIFIES the result by exe identity.

It installs nothing and checks no release — use ` + "`satelle update`" + ` when the
binary itself is out of date. This verb is for the case where the binary is
already current but the running process is still the old image, which is what
` + "`satelle service status`" + ` reports as "(stale process)".

It fails non-zero when the service is still stale afterwards: a verb invoked to
fix a stale process must not report success while the process is still stale.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			// Guard in the COBRA layer, not inside the shared restart function:
			// restartServiceIfRunningRoot returns nil immediately off Linux, and
			// silent success is wrong for a verb the operator typed. Keeping the
			// guard here leaves `satelle update` untouched.
			if runtime.GOOS == "windows" {
				fmt.Fprintln(out, "On Windows, restart the satelle task in Task Scheduler.")
				return nil
			}
			if _, err := exec.LookPath("systemctl"); err != nil {
				fmt.Fprintln(out, "systemctl not found.")
				return nil
			}
			return runServiceRestart(out, "/proc")
		},
	}
}

// runServiceRestart is the injectable core (procRoot lets hermetic tests
// substitute a fake /proc tree, the same seam serviceStatusLine and
// restartServiceIfRunningRoot already use).
//
// It restarts through the SHARED path and then VERIFIES with the SAME
// identityVerdict that produced the stale verdict this verb exists to clear.
// The shared path now returns error on a non-matching respawn (sty_062656a5 AC4).
// This wrapper still re-checks identityVerdict so a skipIfCurrent=false cycle
// that printed success cannot hide a stale process (sty_a7b2cd3c).
func runServiceRestart(out io.Writer, procRoot string) error {
	if err := restartServiceIfRunningRootOpt(out, procRoot, false); err != nil {
		return err
	}
	pid := discoverLivePID(procRoot)
	if pid <= 0 {
		// Nothing running is not a failure: there was no stale process to fix.
		return nil
	}
	known, matches, _ := identityVerdict(procRoot, pid)
	if known && !matches {
		return fmt.Errorf(
			"service restarted but pid %d is still NOT running the installed binary — "+
				"the old binary may be earlier on PATH, or the supervisor respawned the previous image; "+
				"check `satelle service status`", pid)
	}
	return nil
}

// printRegisteredRepoHealth summarises each registered repository's deterministic
// readiness (sty_e9da28e2). Service startup used to say nothing about the repos
// it serves, so an unhealthy one looked identical to a ready one until an agent
// tried to engage a story there.
//
// The diagnostic lives on the CLI side deliberately: satelled is a push-fed
// mirror that never opens a repo database, and teaching it to would undo that
// separation. This is the surface that already has the repo databases in reach.
// It is INFORMATIONAL — an unhealthy repo never fails `service status`, because
// the service itself is running fine; `satelle doctor --all` is where an operator
// goes for the detail.
func printRegisteredRepoHealth(out io.Writer) {
	reports := doctor.CheckAll(context.Background(), doctor.Opts{})
	if len(reports) == 0 {
		return
	}
	healthy, unhealthy := doctor.Summarise(reports)
	fmt.Fprintf(out, "repos:   %d registered — %d healthy, %d unhealthy\n", len(reports), healthy, unhealthy)
	for _, r := range reports {
		if r.OK {
			fmt.Fprintf(out, "  OK        %s\n", r.Repo)
			continue
		}
		errs := r.Findings.WithSeverity(health.SeverityError)
		first := "unreadable"
		if len(errs) > 0 {
			first = errs[0].ID
		}
		fmt.Fprintf(out, "  UNHEALTHY %s — %d problem(s), first: %s\n", r.Repo, len(errs), first)
	}
	if unhealthy > 0 {
		fmt.Fprintln(out, "         run `satelle doctor --all` for the detail")
	}
}

// serviceStatusHooks are the systemctl-touching seams serviceStatusLine calls
// through, mirroring restartHooks' rationale (cmd_update.go): hermetic tests
// override these so status reporting is never exercised against the operator's
// real systemctl or real satelle.service unit. systemStartLtd reuses
// restartHooks.systemUnitStartLtd rather than re-querying (sty_acd4b61e AC4) —
// it is assigned in init() below to avoid an initialization-order dependency
// between the two package-level var blocks.
var serviceStatusHooks = struct {
	userIsActive   func() (state string, reachable bool)
	systemIsActive func() (state string, reachable bool)
	systemStartLtd func() bool
}{
	userIsActive:   func() (string, bool) { return queryIsActive(userEnv(), true) },
	systemIsActive: func() (string, bool) { return queryIsActive(nil, false) },
}

func init() {
	serviceStatusHooks.systemStartLtd = restartHooks.systemUnitStartLtd
}

// queryIsActive runs `systemctl [--user] is-active <unit>` and reports systemd's
// verdict alongside whether the query reached a bus at all: a bus-unreachable
// query prints its "Failed to connect to bus" diagnostic to stderr (discarded
// here) and leaves stdout empty, which is NOT the same claim as "inactive" — an
// empty result must never be read as a verdict about the unit (sty_acd4b61e AC2).
func queryIsActive(env []string, asUser bool) (state string, reachable bool) {
	args := []string{"is-active", serviceUnitName}
	if asUser {
		args = append([]string{"--user"}, args...)
	}
	c := exec.Command("systemctl", args...)
	if env != nil {
		c.Env = env
	}
	out, _ := c.Output()
	s := strings.TrimSpace(string(out))
	return s, s != ""
}

// unitFileExists reports whether a unit file is present on disk — installed-ness
// is a filesystem fact, never derived from a systemctl query (sty_acd4b61e AC2).
func unitFileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// identityVerdict compares the live process's exe identity against the
// installed binary named by whichever unit file is present. known is false
// when the comparison could not be made at all — callers must never read that
// as a match OR a mismatch (sty_acd4b61e AC3). suffix is the report fragment.
func identityVerdict(procRoot string, pid int) (known, matches bool, suffix string) {
	live, liveOK := identityFromPID(procRoot, pid)
	wanted, wantedOK := wantedExeIdentity(procRoot)
	switch {
	case !liveOK || !wantedOK:
		return false, false, "; exe identity: could not be determined"
	case identitiesMatch(live, wanted):
		return true, true, "; exe identity: matches the installed binary"
	default:
		// Name the remedy on the same output, in the `→ fix: ` form the init
		// validator established (sty_a7b2cd3c). Stating the problem and stopping
		// left the operator to already know that restarting is the answer, and an
		// un-actionable warning undercuts the health lines printed around it.
		//
		// The remedy lives HERE, in the one place the mismatch suffix is produced,
		// so all four stale-capable branches of serviceStatusLine inherit it and
		// cannot drift apart.
		return true, false, "; exe identity: does NOT match the installed binary (stale process)" +
			"\n         → fix: satelle service restart"
	}
}

// cgroupOwnerUnit names WHICH installed unit a live process's cgroup path
// identifies as its supervisor: a `user@<uid>.service` segment means the user
// manager owns it, anything else is a system slice. It returns "" when there is
// no claim to make — no cgroup path (the process was located by listening port
// instead, so nothing supervises it as far as kernel facts go), or the unit that
// WOULD own it is not installed here.
//
// Segment-bounded on purpose: a substring test would false-match a path that
// merely contains "user@" inside some other name.
func cgroupOwnerUnit(cgroupPath string, userInstalled, sysInstalled bool) string {
	if cgroupPath == "" {
		return ""
	}
	for _, seg := range strings.Split(cgroupPath, "/") {
		if strings.HasPrefix(seg, "user@") && strings.HasSuffix(seg, ".service") {
			if userInstalled {
				return "user unit"
			}
			return ""
		}
	}
	if sysInstalled {
		return "system unit"
	}
	return ""
}

// serviceStatusLine is the injectable core (procRoot lets hermetic tests
// substitute a fake /proc tree, mirroring restartServiceIfRunningRoot): it
// derives the reported state from kernel facts first — a unit file on disk,
// and the live process located by cgroup or listening-port inspection, neither
// of which needs a reachable systemd bus (sty_acd4b61e AC1/AC2) — and consults
// systemctl only to add detail (which supervisor, start-limited) when the live
// process is confirmed by kernel facts. A failed or unreachable systemctl query
// never by itself produces "not installed" or "stopped".
// serviceStatusReport prefixes the kernel-derived state with the local-tier
// name so `satelle service status` always reports the daemon as satelled
// (sty_bd9de06d AC1).
func serviceStatusReport(procRoot string) string {
	return buildinfo.DaemonName + " — " + serviceStatusLine(procRoot)
}

func serviceStatusLine(procRoot string) string {
	userInstalled := unitFileExists(userUnitPath())
	sysInstalled := unitFileExists(systemUnitPath())
	if !userInstalled && !sysInstalled {
		return "not installed"
	}

	livePID := 0
	cgroupPath := ""
	if runtime.GOOS == "linux" {
		livePID, cgroupPath = findPIDAndCgroup(procRoot, serviceUnitName)
		if livePID == 0 {
			livePID = findPIDByListenPort(procRoot, servicePort())
		}
	}

	// Only consult the supervisor(s) that are actually installed — an unasked
	// system-bus query answering about a system unit that was never installed is
	// a reachable but IRRELEVANT signal, not evidence about this unit's state
	// (this is what wrongly labelled a genuinely bus-unreachable user unit as an
	// "ephemeral" process on this repo's own dogfood machine).
	var userState, sysState string
	var userReachable, sysReachable bool
	if userInstalled {
		userState, userReachable = serviceStatusHooks.userIsActive()
	}
	if sysInstalled {
		sysState, sysReachable = serviceStatusHooks.systemIsActive()
	}
	reachable := userReachable || sysReachable

	if livePID == 0 {
		if !reachable {
			return "installed, not running (supervisor unreachable — could not check for a start-limited unit)"
		}
		if serviceStatusHooks.systemStartLtd() {
			return fmt.Sprintf("stopped — %s is start-limited (systemd exhausted its restart attempts and gave up); run: systemctl reset-failed %s && sudo systemctl restart %s",
				serviceUnitName, serviceUnitName, serviceUnitName)
		}
		return "installed, not running"
	}

	known, matches, suffix := identityVerdict(procRoot, livePID)
	owner := cgroupOwnerUnit(cgroupPath, userInstalled, sysInstalled)
	switch {
	case userReachable && userState == "active":
		return fmt.Sprintf("active (user unit, pid %d)%s", livePID, suffix)
	case sysReachable && sysState == "active":
		return fmt.Sprintf("active (system unit, pid %d)%s", livePID, suffix)
	case reachable && owner != "":
		// The live pid's cgroup names an installed unit — that unit is supervising
		// it even when a sibling unit is reachable-and-inactive (sty_29b2af69).
		// Name the sibling only when both units are actually installed.
		sibling := ""
		if userInstalled && sysInstalled {
			sibling = "; another installed unit for this service is not active"
		}
		return fmt.Sprintf("active (%s, pid %d) — supervised by the installed %s (its cgroup names the unit%s)%s", owner, livePID, owner, sibling, suffix)
	case reachable:
		return fmt.Sprintf("running (pid %d) but NOT reported active by the installed unit — an ephemeral process outside persistent supervision%s", livePID, suffix)
	case known && matches:
		// The supervisor can't be asked, but exe identity independently confirms the
		// live process IS the installed binary — don't hedge a claim identity already
		// settled; the gap here is restart CONTROL, not binary freshness.
		return fmt.Sprintf("running (pid %d) on the installed binary, but its supervisor is unreachable from here (systemctl could not reach the user/system bus) — restart control is unavailable, not identity%s", livePID, suffix)
	case known && !matches:
		return fmt.Sprintf("running (pid %d) but its supervisor is unreachable from here (systemctl could not reach the user/system bus) — AND it is on the OLD binary%s", livePID, suffix)
	default:
		return fmt.Sprintf("running (pid %d) but its supervisor is unreachable from here (systemctl could not reach the user/system bus) — binary identity could not be confirmed%s", livePID, suffix)
	}
}

// renderUnit renders the unit file content for the service. Pure (testable): the
// ExecStart bakes in the resolved addr/port. WorkingDirectory is the operator home
// (not a single repo) — push-fed serve (sty_dbdadfa0) opens only the machine-wide
// mirror under ~/.satelle/serve/ and needs no per-repo cwd.
// wantedBy selects the install target: default.target for a per-user unit,
// multi-user.target for a system unit that runs with no login. A system unit adds
// User=/Group= so it runs as the operator (reaching ~/.satelle), not root.
func renderUnit(binPath, repo, addr string, port int, wantedBy, runAsUser, restartPolicy string) string {
	userLines := ""
	if runAsUser != "" {
		userLines = fmt.Sprintf("User=%s\nGroup=%s\n", runAsUser, runAsUser)
	}
	// Prefer home as WorkingDirectory; fall back to repo only if home is unknown.
	wd := os.Getenv("HOME")
	if wd == "" {
		wd = repo
	}
	return fmt.Sprintf(`[Unit]
Description=satelled — local read-only mirror (push-fed web UI)
After=network.target

[Service]
ExecStart=%s --addr %s --port %d
WorkingDirectory=%s
%sRestart=%s
RestartSec=2

[Install]
WantedBy=%s
`, binPath, addr, port, wd, userLines, restartPolicy, wantedBy)
}

// resolveServeBinary picks the satelled binary: --serve-bin, the sibling of
// self, else satelled on PATH (REBUILD Q-serve: the unit calls satelled).
// exists is injectable for tests; nil uses os.Stat.
func resolveServeBinary(selfPath, flagOverride string, exists func(string) bool) (execPath string, viaFallback bool) {
	if exists == nil {
		exists = func(p string) bool {
			st, err := os.Stat(p)
			return err == nil && !st.IsDir()
		}
	}
	if flagOverride != "" {
		if exists(flagOverride) {
			return flagOverride, false
		}
	}
	dir := filepath.Dir(selfPath)
	sib := filepath.Join(dir, buildinfo.DaemonName)
	if exists(sib) {
		return sib, false
	}
	// Fallback: satelled from PATH (the unit calls satelled, never a satelle verb).
	if p, err := exec.LookPath(buildinfo.DaemonName); err == nil {
		return p, true
	}
	return buildinfo.DaemonName, true
}

// systemdUnit renders the per-user unit (WantedBy=default.target, runs as the
// logged-in user via the user manager).
//
// Restart=always, not on-failure (sty_f20f3f3b): `satelle update` cycles a
// bus-unreachable service by making the process EXIT and letting its supervisor
// respawn it. systemd counts a clean SIGTERM as success, so an on-failure unit
// would STOP rather than reload — forcing the escalation to SIGKILL. Units
// written from here stop at the graceful rung. Units already on disk keep
// on-failure until re-installed (picking up a new policy needs daemon-reload,
// which needs the bus this path exists to avoid), and are handled by that
// escalation instead.
func systemdUnit(binPath, repo, addr string, port int) string {
	return renderUnit(binPath, repo, addr, port, "default.target", "", "always")
}

// systemSystemdUnit renders the persistent SYSTEM unit (WantedBy=multi-user.target)
// that survives session loss, running as runAsUser so it still reaches the user's
// config and repo. Restart=always so a SUDO-FREE restart works: the operator (who
// owns the process via User=) can SIGTERM it and systemd respawns it onto the new
// binary — `satelle update` relies on this (sty_1ac9f095). This is the fleet
// supervisor when the user bus is unreachable.
func systemSystemdUnit(binPath, repo, addr string, port int, runAsUser string) string {
	return renderUnit(binPath, repo, addr, port, "multi-user.target", runAsUser, "always")
}

// installUserUnit writes the unit under the user systemd dir and enables it,
// enabling linger so it survives logout and starts on (WSL) boot. systemctl
// failures return an error (non-zero exit) — a soft "printed guidance" success
// left a stale serve process after make install (footer version drift).
func installUserUnit(out io.Writer, unit string, port int, addr string) error {
	unitPath := userUnitPath()
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("service: mkdir %s: %w", filepath.Dir(unitPath), err)
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("service: write %s: %w", unitPath, err)
	}
	fmt.Fprintf(out, "unit:   %s\n", unitPath)

	// enable (persist) then restart — restart starts the unit if stopped and
	// reloads a NEW binary if it is already running, so re-running install after a
	// rebuild actually redeploys (enable --now alone is a no-op on a live unit).
	steps := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", serviceUnitName},
		{"systemctl", "--user", "restart", serviceUnitName},
	}
	if u, err := user.Current(); err == nil {
		// Linger lets the user service run without an active login + start on boot.
		steps = append(steps, []string{"loginctl", "enable-linger", u.Username})
	}
	env := userEnv() // default the systemd user-session env for a headless shell
	var failed []string
	for _, s := range steps {
		runEnv := env
		if s[0] != "systemctl" {
			runEnv = nil // loginctl talks to system logind — inherit the plain env
		}
		if err := runCaptureEnv(runEnv, s[0], s[1:]...); err != nil {
			failed = append(failed, joinArgs(s)+": "+err.Error())
			fmt.Fprintf(out, "  ! %s failed: %v\n", joinArgs(s), err)
		}
	}
	if len(failed) > 0 {
		// The BINARY installed fine (make install succeeded, the user unit is written);
		// only attaching a persistent supervisor failed. When the cause is an
		// unreachable user bus, lead with the --system remediation and label this as a
		// supervisor-attach gap, not a failed binary (sty_00dadc91, aligns with the
		// mechanism-agnostic release skill sty_dfc73ced).
		fmt.Fprintf(out, "\nBinary installed OK; the user unit is written to %s.\n", unitPath)
		if busUnreachable(failed) {
			fmt.Fprintln(out, "The systemd --user bus is unreachable (no running user manager in this")
			fmt.Fprintln(out, "headless/non-login session). For a PERSISTENT supervisor that survives")
			fmt.Fprintln(out, "session loss, install a system unit:")
			fmt.Fprintln(out, "  satelle service install --system")
			fmt.Fprintln(out, "(or do it by hand:)")
		} else {
			fmt.Fprintln(out, "\nAutomatic enable hit an error. Finish manually, then re-run service install:")
			for _, s := range steps {
				fmt.Fprintf(out, "  %s\n", joinArgs(s))
			}
			fmt.Fprintln(out, "Or install a persistent system unit (survives session loss):")
			fmt.Fprintln(out, "  satelle service install --system")
			fmt.Fprintln(out, "(or by hand:)")
		}
		fmt.Fprintf(out, "  sudo cp %s /etc/systemd/system/%s\n", unitPath, serviceUnitName)
		fmt.Fprintf(out, "  sudo systemctl enable --now %s\n", serviceUnitName)
		if busUnreachable(failed) {
			return fmt.Errorf("service: binary installed OK, but the systemd --user bus is unreachable — run \"satelle service install --system\" for a persistent unit (user-manager detail: %s)",
				strings.Join(failed, "; "))
		}
		return fmt.Errorf("service: systemctl --user could not enable/restart %s: %s",
			serviceUnitName, strings.Join(failed, "; "))
	}
	fmt.Fprintf(out, "\nservice running → http://localhost:%d\n", port)
	if addr == "0.0.0.0" {
		fmt.Fprintln(out, "(reachable from a Windows browser at the same URL when satelle runs in WSL)")
	}
	return nil
}

func printWindowsGuidance(out io.Writer, bin, repo, addr string, port int) {
	fmt.Fprintln(out, "\nNative Windows has no systemd. To run the service on login, create a")
	fmt.Fprintln(out, "Task Scheduler task (Trigger: At log on; Action: Start a program):")
	fmt.Fprintf(out, "  Program:   %s\n", bin)
	fmt.Fprintf(out, "  Arguments: --addr %s --port %d\n", addr, port)
	fmt.Fprintf(out, "  Start in:  %s\n", repo)
	fmt.Fprintf(out, "Then browse http://localhost:%d\n", port)
}

func printNoSystemdGuidance(out io.Writer, unit string) {
	fmt.Fprintln(out, "\nsystemctl not found (systemd not enabled in this environment).")
	fmt.Fprintln(out, "Enable systemd in WSL (/etc/wsl.conf → [boot] systemd=true, then `wsl --shutdown`),")
	fmt.Fprintln(out, "or run satelled in the background yourself. The unit to install once systemd is on:")
	fmt.Fprintln(out, "\n"+unit)
}

// resolveSelfPath returns the absolute path of the running satelle binary, used
// in the unit's ExecStart. Re-run install after moving the binary.
func resolveSelfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("service: resolve binary path: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return exe, nil
}

func userUnitPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "systemd", "user", serviceUnitName)
}

// systemUnitDir is the directory a persistent SYSTEM unit lives in. A package
// var rather than a constant so hermetic tests can point it at a temp dir, the
// same way procRoot redirects /proc reads and restartHooks redirects the
// systemctl seams (sty_d50218d1). PRODUCTION NEVER OVERRIDES IT — there is no
// flag or environment knob, because this is a test-isolation need and a
// production-visible knob would be a surface nobody asked for.
//
// Before this indirection, `userUnitPath()` was hermetic (it follows $HOME, which
// tests redirect) while this path was not, so `go test ./...` read the operator's
// real /etc and passed or failed depending on whether they happened to have a
// system unit installed. CI runners have none, so CI stayed green while a
// developer with a real install saw red.
//
// Overriding tests must not call t.Parallel(): the var is process-global.
var systemUnitDir = "/etc/systemd/system"

// systemUnitPath is where a persistent SYSTEM unit lives (needs root to write).
func systemUnitPath() string {
	return filepath.Join(systemUnitDir, serviceUnitName)
}

// currentUsername returns the login name for a system unit's User=, falling back
// to the numeric uid when the name can't be resolved.
func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return currentUID()
}

// busUnreachable reports whether any systemctl --user failure was a
// bus-connection error (no running user manager), as opposed to a genuine
// enable/restart fault. Pure (unit-tested); case-insensitive.
func busUnreachable(failures []string) bool {
	for _, f := range failures {
		l := strings.ToLower(f)
		if strings.Contains(l, "connect to bus") ||
			strings.Contains(l, "user scope bus") ||
			strings.Contains(l, "no such file or directory") && strings.Contains(l, "bus") {
			return true
		}
	}
	return false
}

// systemInstallSteps is the pure command plan for a persistent system-unit install
// (unit-tested): copy the rendered unit into place, reload, enable+start. Each runs
// under sudo — a system path needs root; it needs neither --user nor linger.
func systemInstallSteps(srcUnit, destUnit string) [][]string {
	return [][]string{
		{"sudo", "install", "-m", "0644", srcUnit, destUnit},
		{"sudo", "systemctl", "daemon-reload"},
		{"sudo", "systemctl", "enable", serviceUnitName},  // persist across boot
		{"sudo", "systemctl", "restart", serviceUnitName}, // load the new binary NOW (enable --now would no-op a running unit)
	}
}

// installSystemUnit writes the rendered system unit to a temp file and runs the
// sudo step plan to install a PERSISTENT supervisor that survives session loss.
// sudo is REQUIRED and never auto-elevated silently: if sudo is absent the manual
// commands are printed and a distinct error returned. sudo may prompt for a
// password (steps inherit the tty). A step failure prints the manual fallback.
func installSystemUnit(out io.Writer, unit string, port int) error {
	dest := systemUnitPath()
	if _, err := exec.LookPath("sudo"); err != nil {
		printManualSystemInstall(out, unit, dest)
		return fmt.Errorf("service: --system needs sudo, which was not found — write %s as root using the printed unit, then `systemctl enable --now %s`", dest, serviceUnitName)
	}
	tmp, err := os.CreateTemp("", "satelle-service-*.service")
	if err != nil {
		return fmt.Errorf("service: create temp unit: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, werr := tmp.WriteString(unit); werr != nil {
		_ = tmp.Close()
		return fmt.Errorf("service: write temp unit: %w", werr)
	}
	_ = tmp.Close()

	fmt.Fprintf(out, "installing persistent system unit %s (sudo may prompt)…\n", dest)
	var failed []string
	for _, s := range systemInstallSteps(tmpPath, dest) {
		if err := runQuiet(s[0], s[1:]...); err != nil { // inherit tty so sudo can prompt
			failed = append(failed, joinArgs(s)+": "+err.Error())
			fmt.Fprintf(out, "  ! %s failed: %v\n", joinArgs(s), err)
		}
	}
	if len(failed) > 0 {
		printManualSystemInstall(out, unit, dest)
		return fmt.Errorf("service: system-unit install failed: %s", strings.Join(failed, "; "))
	}
	fmt.Fprintf(out, "\npersistent system service running → http://localhost:%d\n", port)
	fmt.Fprintln(out, "(survives logout/session loss; manage with `sudo systemctl status/restart "+serviceUnitName+"`)")
	return nil
}

// printManualSystemInstall prints the copy-pasteable root steps to install the
// system unit by hand when sudo is unavailable or a step failed.
func printManualSystemInstall(out io.Writer, unit, dest string) {
	fmt.Fprintln(out, "\nInstall the persistent system unit by hand (as root):")
	fmt.Fprintf(out, "  sudo tee %s >/dev/null <<'UNIT'\n%sUNIT\n", dest, unit)
	fmt.Fprintln(out, "  sudo systemctl daemon-reload")
	fmt.Fprintf(out, "  sudo systemctl enable --now %s\n", serviceUnitName)
}

// userSystemctlEnv defaults the systemd user-session env vars when they are absent,
// so `systemctl --user` can reach a running user manager from a NON-LOGIN shell (a
// headless agent session) that never inherited them — the "Failed to connect to bus"
// breakage. Existing values are preserved. Pure over its inputs (unit-tested).
func userSystemctlEnv(env []string, uid string) []string {
	has := func(key string) bool {
		p := key + "="
		for _, e := range env {
			if strings.HasPrefix(e, p) {
				return true
			}
		}
		return false
	}
	out := append([]string(nil), env...)
	runtimeDir := "/run/user/" + uid
	if !has("XDG_RUNTIME_DIR") {
		out = append(out, "XDG_RUNTIME_DIR="+runtimeDir)
	}
	if !has("DBUS_SESSION_BUS_ADDRESS") {
		out = append(out, "DBUS_SESSION_BUS_ADDRESS=unix:path="+runtimeDir+"/bus")
	}
	return out
}

// currentUID returns the numeric uid as a string (user.Current, falling back to
// os.Getuid), for the /run/user/<uid> defaults.
func currentUID() string {
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return u.Uid
	}
	return fmt.Sprintf("%d", os.Getuid())
}

// userEnv is the process env with the systemd user-session defaults applied.
func userEnv() []string {
	return userSystemctlEnv(os.Environ(), currentUID())
}

// runQuiet runs a command, discarding output; returns the error (if any).
func runQuiet(name string, args ...string) error {
	return runQuietEnv(nil, name, args...)
}

// runQuietEnv runs a command with an explicit env (nil = inherit), discarding
// output. `systemctl --user` calls pass userEnv() so a headless shell reaches the
// user bus; sudo/system calls pass nil so they inherit the tty for a password prompt.
func runQuietEnv(env []string, name string, args ...string) error {
	c := exec.Command(name, args...)
	if env != nil {
		c.Env = env
	}
	return c.Run()
}

// runCaptureEnv runs a command with env (nil = inherit) and folds its STDERR into
// the returned error, so a diagnostic like "Failed to connect to bus" survives for
// the caller to classify — runQuiet discards it, which would make busUnreachable
// dead code (sty_00dadc91). Not used for sudo (it prompts on the tty, not stderr).
func runCaptureEnv(env []string, name string, args ...string) error {
	c := exec.Command(name, args...)
	if env != nil {
		c.Env = env
	}
	var errb bytes.Buffer
	c.Stderr = &errb
	err := c.Run()
	if err != nil {
		if m := strings.TrimSpace(errb.String()); m != "" {
			return fmt.Errorf("%s", m)
		}
	}
	return err
}

func joinArgs(s []string) string {
	out := s[0]
	for _, a := range s[1:] {
		out += " " + a
	}
	return out
}

// installedUnitFile returns the contents of the installed unit file — the user
// unit first, then the system unit — and whether either was readable. It is the
// SINGLE walk over unit paths that the update path shares, so exe identity and
// the Restart policy can never be read from different files (sty_d45618d5).
func installedUnitFile() (string, bool) {
	for _, unitPath := range []string{userUnitPath(), systemUnitPath()} {
		content, err := os.ReadFile(unitPath)
		if err != nil {
			continue
		}
		return string(content), true
	}
	return "", false
}

// unitRestartPolicy returns the installed unit's Restart= value, lower-cased, and
// whether a unit file was readable at all. An unreadable unit is NOT reported as
// a policy of "no": the caller must distinguish "declared not to restart" from
// "we do not know", because both forbid signalling but for different reasons.
func unitRestartPolicy() (string, bool) {
	content, ok := installedUnitFile()
	if !ok {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(unitDirective(content, "Restart"))), true
}
