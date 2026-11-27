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
	"github.com/bobmcallan/satelle/internal/mirror"
)

const serviceUnitName = "satelle.service"

const (
	syncServiceName   = "satelle-sync.service"
	syncTimerName     = "satelle-sync.timer"
	followServiceName = "satelle-follow.service"
)

type serviceUnits struct {
	Daemon, SyncService, SyncTimer, Follow string
}

type serviceSpec struct {
	Daemon, Addr string
	Port         int
	CLI          string
}

func unitsFor(s serviceSpec) serviceUnits {
	u := serviceUnits{SyncService: renderSyncService(s.CLI), SyncTimer: renderSyncTimer(), Follow: renderFollowService(s.CLI)}
	if s.Daemon != "" {
		u.Daemon = renderUnit(s.Daemon, s.Addr, s.Port)
	}
	return u
}

func writeDefinition(w io.Writer, path, body string, force bool, binOf func(string) string) (bool, error) {
	if body == "" {
		return false, nil
	}
	why := ""
	if !force {
		old, err := os.ReadFile(path)
		switch {
		case err != nil:
			why = " (missing)"
		case !sameBinary(binOf(string(old)), binOf(body)):
			why = " (was " + binOf(string(old)) + ")"
		default:
			fmt.Fprintf(w, "kept %s\n", path)
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	fmt.Fprintf(w, "wrote %s%s\n", path, why)
	return true, nil
}

func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install, remove, inspect or restart the satelled background service",
		Long: `service manages satelled, the local web UI, as a per-user systemd unit whose
ExecStart runs satelled, satelle-sync.timer, which runs
'satelle ledger autosync' every 30 seconds: each of this machine's projects
whose [ledger] store is not local pushes, then pulls, by the rules of
'satelle ledger push' and 'pull', and satelle-follow.service, which keeps
'satelle ledger follow' running: each satelle.dev-bound project pushes a
write as it is made and pulls other locations' changes within seconds, by
the same pass, with the timer as the fallback. See 'satelle help ledger-store'. Port and
address are machine settings in ~/.satelle/config.toml [service].

Installing or updating satelle (scripts/install.sh, satelle update) installs
and refreshes both, so syncing is the default: it installs a missing unit,
repoints one naming another binary, restarts satelled onto the new build,
starts the sync timer and prints 'satelle service status'. --no-service or
SATELLE_NO_SERVICE=1 skips every unit change. With no systemd user manager the
units and the autosync command are printed instead.

On macOS the same commands manage three launchd agents in the gui/<uid> domain,
~/Library/LaunchAgents/dev.satelle.satelled.plist (RunAtLoad, KeepAlive) and
dev.satelle.sync.plist (satelle ledger autosync every 30 seconds) and
dev.satelle.follow.plist (satelle ledger follow; RunAtLoad, KeepAlive),
through launchctl.

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
			return installService(cmd.OutOrStdout(), port, addr, bin)
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
satelle-follow.service and enables and starts satelle-sync.timer, then prints
'satelle service status'.
It never changes ~/.satelle/config.toml; 'satelle service install' records
[service]. With no systemd user manager it prints the units and the autosync
command instead. On macOS it does the same with the dev.satelle.satelled,
dev.satelle.sync and dev.satelle.follow launch agents.`,
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

var errNoSystemd = errors.New("no systemd user manager is reachable here; the units were printed, not installed: run satelled and 'satelle ledger autosync' yourself")

func printUnits(w io.Writer, u serviceUnits) {
	fmt.Fprintf(w, "no systemd user manager here; run satelled directly")
	if u.Daemon != "" {
		fmt.Fprintf(w, ". The unit would be:\n\n%s", u.Daemon)
	} else {
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "\nand run satelle ledger autosync every %d s. The sync units would be:\n\n%s\n%s",
		int(mirror.SyncPeriod.Seconds()), u.SyncService, u.SyncTimer)
	fmt.Fprintf(w, "\nand keep satelle ledger follow running. The follow unit would be:\n\n%s", u.Follow)
}

func sameBinary(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func refreshService(ctx context.Context, w io.Writer, cli, daemon string) error {
	gc, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	s := serviceSpec{Addr: gc.Service.ResolveAddr(), Port: gc.Service.ResolvePort(), CLI: cli}
	if st, err := os.Stat(daemon); err == nil && !st.IsDir() && filepath.IsAbs(daemon) {
		s.Daemon = daemon
	} else if _, err := os.Stat(daemonDefPath()); err != nil {
		fmt.Fprintf(w, "%s: no binary beside satelle or on PATH; its %s was not installed\n", buildinfo.DaemonName, defNoun)
	}
	if err := applyService(w, s, false); err != nil {
		if errors.Is(err, errNoSystemd) {
			return nil
		}
		return err
	}
	return printServiceStatus(ctx, w)
}

func printServiceStatus(ctx context.Context, out io.Writer) error {
	gc, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	printManagerStatus(out)
	fmt.Fprintf(out, "config    %s (port %d, addr %s)\n", config.GlobalConfigPath(), gc.Service.ResolvePort(), gc.Service.ResolveAddr())
	ep := fmt.Sprintf("http://127.0.0.1:%d/healthz", gc.Service.ResolvePort())
	path := ""
	if body, err := os.ReadFile(daemonDefPath()); err == nil {
		path = defDaemon(string(body))
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

func installService(out io.Writer, port int, addr, bin string) error {
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
	return applyService(out, serviceSpec{
		Daemon: resolveDaemon(bin),
		Addr:   gc.Service.ResolveAddr(),
		Port:   gc.Service.ResolvePort(),
		CLI:    resolveCLI(),
	}, true)
}

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

func resolveCLI() string {
	if self, err := os.Executable(); err == nil {
		return self
	}
	return "satelle"
}

func renderSyncService(cli string) string {
	return fmt.Sprintf(`[Unit]
Description=satelle ledger sync pass (push then pull each project on this machine)
After=network-online.target

[Service]
Type=oneshot
ExecStart=%s ledger autosync
TimeoutStartSec=%d
`, cli, int(mirror.SyncPassTimeout/time.Second))
}

func renderFollowService(cli string) string {
	return fmt.Sprintf(`[Unit]
Description=satelle ledger follow (push this machine's writes and pull other locations' changes within seconds)
After=network-online.target

[Service]
Type=simple
ExecStart=%s ledger follow
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, cli)
}

func renderSyncTimer() string {
	secs := int(mirror.SyncPeriod / time.Second)
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

func userUnitPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "systemd", "user", name)
}
