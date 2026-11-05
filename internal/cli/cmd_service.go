package cli

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

// serviceUnitName is the unit that runs satelled.
const serviceUnitName = "satelle.service"

func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install, remove, inspect or restart the satelled background service",
		Long: `service manages satelled, the local web UI, as a per-user systemd unit whose
ExecStart runs satelled. Port and address are machine settings in
~/.satelle/config.toml [service].`,
	}
	var port int
	var addr, bin string
	install := &cobra.Command{
		Use:   "install",
		Short: "Write and start the per-user unit running satelled",
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
			unit := renderUnit(daemon, gc.Service.ResolveAddr(), gc.Service.ResolvePort())
			return installUnit(cmd.OutOrStdout(), unit)
		},
	}
	install.Flags().IntVar(&port, "port", 0, "listen port (recorded in ~/.satelle/config.toml)")
	install.Flags().StringVar(&addr, "addr", "", "bind address (recorded in ~/.satelle/config.toml)")
	install.Flags().StringVar(&bin, "satelled", "", "satelled binary (default: beside satelle, else on PATH)")
	cmd.AddCommand(install,
		&cobra.Command{
			Use: "uninstall", Short: "Stop and remove the unit", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return uninstallUnit(cmd.OutOrStdout()) },
		},
		&cobra.Command{
			Use: "restart", Short: "Restart the unit", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return restartUnit(cmd.OutOrStdout()) },
		},
		&cobra.Command{
			Use: "status", Short: "Report the unit and whether satelled answers", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				gc, err := config.LoadGlobal()
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "unit      %s (%s)\n", unitPath(), unitState())
				fmt.Fprintf(out, "config    %s (port %d, addr %s)\n", config.GlobalConfigPath(), gc.Service.ResolvePort(), gc.Service.ResolveAddr())
				ep := fmt.Sprintf("http://127.0.0.1:%d/healthz", gc.Service.ResolvePort())
				client := http.Client{Timeout: 2 * time.Second}
				if resp, err := client.Get(ep); err == nil {
					resp.Body.Close()
					fmt.Fprintf(out, "satelled  answering at %s (%s)\n", ep, resp.Status)
				} else {
					fmt.Fprintf(out, "satelled  not answering at %s\n", ep)
				}
				return nil
			},
		})
	return cmd
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

func unitPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "systemd", "user", serviceUnitName)
}
