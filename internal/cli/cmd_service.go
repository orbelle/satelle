package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

// serviceUnitName is the unit that runs satelled.
const serviceUnitName = "satelle.service"

// The units that run the automatic ledger sync pass (sto_5c706973): a timer
// starting a oneshot service that runs `satelle ledger autosync` syncPeriod
// after the last run ended. A write therefore reaches the store within one
// period plus a pass, and another machine within two periods plus its pass:
// the 90-second bound the sync-architecture principle states.
const (
	syncServiceName = "satelle-sync.service"
	syncTimerName   = "satelle-sync.timer"
	syncPeriod      = 30 * time.Second
)

// serviceUnits are the unit files service install and refresh write: satelled's and the
// sync pass's service and timer.
type serviceUnits struct {
	Daemon, SyncService, SyncTimer string
}

func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install, remove, inspect or restart the satelled background service",
		Long: `service manages satelled, the local web UI, as a per-user systemd unit whose
ExecStart runs satelled, and satelle-sync.timer, which runs
'satelle ledger autosync' every 30 seconds: each of this machine's projects
whose [ledger] store is not local pushes, then pulls, by the rules of
'satelle ledger push' and 'pull'. See 'satelle help ledger-store'. Port and
address are machine settings in ~/.satelle/config.toml [service].

Installing or updating satelle (scripts/install.sh, satelle update) installs
and refreshes both, so syncing is the default: it installs a missing unit,
repoints one naming another binary, restarts satelled onto the new build,
starts the sync timer and prints 'satelle service status'. --no-service or
SATELLE_NO_SERVICE=1 skips every unit change. With no systemd user manager the
units and the autosync command are printed instead.

refresh does the same by hand. install writes every unit afresh and records
[service] (port, addr, and this directory as the repo).`,
	}
	var port int
	var addr, bin string
	install := &cobra.Command{
		Use:   "install",
		Short: "Write and start the per-user units running satelled and the sync timer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gc, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			if port > 0 {
				gc.Service.Port = port
			}
			if addr != "" {
				gc.Service.Addr = addr
			}
			if wd, err := os.Getwd(); err == nil {
				gc.Service.Repo = wd
			}
			if err := config.SaveGlobal(gc); err != nil {
				return err
			}
			daemon := resolveDaemon(bin)
			return applyUnits(cmd.OutOrStdout(), serviceUnits{
				Daemon:      renderUnit(daemon, gc.Service.ResolveAddr(), gc.Service.ResolvePort()),
				SyncService: renderSyncService(resolveCLI()),
				SyncTimer:   renderSyncTimer(),
			}, true)
		},
	}
	install.Flags().IntVar(&port, "port", 0, "listen port (recorded in ~/.satelle/config.toml)")
	install.Flags().StringVar(&addr, "addr", "", "bind address (recorded in ~/.satelle/config.toml)")
	install.Flags().StringVar(&bin, "satelled", "", "satelled binary (default: beside satelle, else on PATH)")
	cmd.AddCommand(install,
		&cobra.Command{
			Use: "uninstall", Short: "Stop and remove the units", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return uninstallUnit(cmd.OutOrStdout()) },
		},
		&cobra.Command{
			Use: "restart", Short: "Restart the unit", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return restartUnit(cmd.OutOrStdout()) },
		},
		&cobra.Command{
			Use:   "refresh",
			Short: "Install missing units, repoint any naming another binary, restart satelled and start the sync timer",
			Long: `refresh is what installing or updating satelle runs: it writes each unit that
is missing or whose ExecStart names another binary than this satelle (and the
satelled beside it, else on PATH), keeps the rest, restarts satelled and
enables and starts satelle-sync.timer, then prints 'satelle service status'.
It never changes ~/.satelle/config.toml; 'satelle service install' records
[service]. With no systemd user manager it prints the units and the autosync
command instead.`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return refreshService(cmd.Context(), cmd.OutOrStdout(), resolveCLI(), resolveDaemon(""))
			},
		},
		&cobra.Command{
			Use: "status", Short: "Report the unit and whether satelled answers", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return printServiceStatus(cmd.Context(), cmd.OutOrStdout())
			},
		})
	return cmd
}

// errNoSystemd is applyUnits' answer where no systemd user manager is
// reachable: the units were printed instead of installed.
var errNoSystemd = errors.New("no systemd user manager is reachable here; the units were printed, not installed: run satelled and 'satelle ledger autosync' yourself")

// printUnits prints the units applyUnits would install and the command the
// sync timer would run, for a machine without systemd. A unit with no body is
// left out.
func printUnits(w io.Writer, u serviceUnits) {
	fmt.Fprintf(w, "no systemd user manager here; run satelled directly")
	if u.Daemon != "" {
		fmt.Fprintf(w, ". The unit would be:\n\n%s", u.Daemon)
	} else {
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "\nand run satelle ledger autosync every %d s. The sync units would be:\n\n%s\n%s",
		int(syncPeriod.Seconds()), u.SyncService, u.SyncTimer)
}

// sameBinary reports whether a and b name one binary: the same path, or paths
// resolving to the same file through symlinks.
func sameBinary(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// refreshService installs and refreshes the units for cli and daemon without
// touching ~/.satelle/config.toml: a missing unit is written, one naming
// another binary is repointed, satelled is restarted and the sync timer
// started, then the status is printed. satelled's unit is written only when
// daemon is an existing absolute path; the sync units do not need it. Without
// systemd the units are printed and refreshService succeeds, since what
// installed satelle did succeed.
func refreshService(ctx context.Context, w io.Writer, cli, daemon string) error {
	gc, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	u := serviceUnits{SyncService: renderSyncService(cli), SyncTimer: renderSyncTimer()}
	if st, err := os.Stat(daemon); err == nil && !st.IsDir() && filepath.IsAbs(daemon) {
		u.Daemon = renderUnit(daemon, gc.Service.ResolveAddr(), gc.Service.ResolvePort())
	} else if _, err := os.Stat(unitPath()); err != nil {
		fmt.Fprintf(w, "%s: no binary beside satelle or on PATH; its unit was not installed\n", buildinfo.DaemonName)
	}
	if err := applyUnits(w, u, false); err != nil {
		if errors.Is(err, errNoSystemd) {
			return nil
		}
		return err
	}
	return printServiceStatus(ctx, w)
}

// printServiceStatus is 'satelle service status': the units' states, the
// config, and whether satelled answers and with which build.
func printServiceStatus(ctx context.Context, out io.Writer) error {
	gc, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "unit      %s (%s)\n", unitPath(), unitState())
	fmt.Fprintf(out, "sync      %s (%s)\n", syncTimerName, syncTimerState())
	fmt.Fprintf(out, "config    %s (port %d, addr %s)\n", config.GlobalConfigPath(), gc.Service.ResolvePort(), gc.Service.ResolveAddr())
	ep := fmt.Sprintf("http://127.0.0.1:%d/healthz", gc.Service.ResolvePort())
	path := ""
	if body, err := os.ReadFile(unitPath()); err == nil {
		path = unitDaemon(string(body))
	}
	if path == "" {
		path = resolveDaemon("")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	home := config.GlobalDir()
	installed := buildinfo.Identify(ctx, home, path)
	cli := buildinfo.Lookup(home, buildinfo.CLIBeside(path))
	reportDaemon(out, &http.Client{Timeout: 2 * time.Second}, ep, installed, path, cli)
	return nil
}

// resolveDaemon picks the satelled binary: the flag, the sibling of this
// executable, else satelled on PATH.
func resolveDaemon(flag string) string {
	if flag != "" {
		return flag
	}
	if self, err := os.Executable(); err == nil {
		sib := filepath.Join(filepath.Dir(self), buildinfo.DaemonName)
		if st, err := os.Stat(sib); err == nil && !st.IsDir() {
			return sib
		}
	}
	if p, err := exec.LookPath(buildinfo.DaemonName); err == nil {
		return p
	}
	return buildinfo.DaemonName
}

// renderUnit is the systemd unit running satelled.
func renderUnit(daemon, addr string, port int) string {
	return fmt.Sprintf(`[Unit]
Description=satelled (satelle local web UI)
After=network.target

[Service]
ExecStart=%s --addr %s --port %d
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, daemon, addr, port)
}

// resolveCLI is the satelle binary the sync service runs: this executable.
// satelle update replaces that file, so the next pass runs the new build.
func resolveCLI() string {
	if self, err := os.Executable(); err == nil {
		return self
	}
	return "satelle"
}

// renderSyncService is the oneshot unit running one automatic sync pass.
func renderSyncService(cli string) string {
	return fmt.Sprintf(`[Unit]
Description=satelle ledger sync pass (push then pull each project on this machine)
After=network-online.target

[Service]
Type=oneshot
ExecStart=%s ledger autosync
TimeoutStartSec=300
`, cli)
}

// renderSyncTimer is the timer starting the sync pass syncPeriod after boot
// and after each run ends; a oneshot still running is never started twice.
func renderSyncTimer() string {
	secs := int(syncPeriod / time.Second)
	return fmt.Sprintf(`[Unit]
Description=satelle ledger sync every %[1]d s

[Timer]
OnBootSec=%[1]ds
OnUnitInactiveSec=%[1]ds
AccuracySec=1s
Unit=%[2]s

[Install]
WantedBy=timers.target
`, secs, syncServiceName)
}

// unitDaemon is the binary a unit body's ExecStart runs (renderUnit's format),
// or "" when there is none.
func unitDaemon(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart="); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}

// reportDaemon prints whether satelled answers at ep, the build it runs (from
// /healthz), the installed binary's build, the installed satelle CLI beside it
// (cli, as recorded; "" when not reported), and one warning per disagreement by
// buildinfo.Drift, the rule satelled's footer shows: a running satelled left
// behind after an install keeps serving the old build until it is restarted, and
// a CLI whose version differs from satelled's is a half-done update.
func reportDaemon(out io.Writer, client *http.Client, ep, installed, path, cli string) {
	resp, err := client.Get(ep)
	if err != nil {
		fmt.Fprintf(out, "satelled  not answering at %s\n", ep)
		return
	}
	defer resp.Body.Close()
	fmt.Fprintf(out, "satelled  answering at %s (%s)\n", ep, resp.Status)
	running := "unknown (this satelled reports no build identity on /healthz)"
	var info buildinfo.Info
	if json.NewDecoder(resp.Body).Decode(&info) == nil && info.Version != "" {
		running = info.String()
	}
	fmt.Fprintf(out, "running   %s\n", running)
	fmt.Fprintf(out, "installed %s [%s]\n", installed, path)
	if cli == "" {
		fmt.Fprintf(out, "satelle   unknown (not reported)\n")
	} else {
		fmt.Fprintf(out, "satelle   %s [%s]\n", cli, buildinfo.CLIBeside(path))
	}
	for _, m := range buildinfo.Drift(cli, running, installed) {
		fmt.Fprintf(out, "warning   %s; run '%s'\n", m.Text, m.Remedy)
	}
}

func unitPath() string { return userUnitPath(serviceUnitName) }

// userUnitPath is name's file among this user's systemd units.
func userUnitPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "systemd", "user", name)
}
