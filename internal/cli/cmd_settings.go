package cli

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
)

// machineSetting is one key of ~/.satelle/config.toml that `settings` shows
// and sets.
type machineSetting struct {
	key, doc string
	get      func(config.GlobalConfig) (value, effective string)
	set      func(*config.GlobalConfig, string) error
}

var machineSettings = []machineSetting{
	{"server", "the satelle-server this machine signs in to",
		func(g config.GlobalConfig) (string, string) { return g.Hosted.ResolveServer(), "" },
		func(g *config.GlobalConfig, v string) error {
			if v != "" {
				if u, err := url.Parse(v); err != nil || u.Scheme == "" || u.Host == "" {
					return fmt.Errorf("server %q is not a URL", v)
				}
			}
			g.Hosted.Server = strings.TrimRight(v, "/")
			return nil
		}},
	{"service.addr", "the address satelled binds",
		func(g config.GlobalConfig) (string, string) { return g.Service.Addr, g.Service.ResolveAddr() },
		func(g *config.GlobalConfig, v string) error { g.Service.Addr = v; return nil }},
	{"service.port", "the port satelled listens on",
		func(g config.GlobalConfig) (string, string) {
			v := ""
			if g.Service.Port > 0 {
				v = strconv.Itoa(g.Service.Port)
			}
			return v, strconv.Itoa(g.Service.ResolvePort())
		},
		func(g *config.GlobalConfig, v string) error {
			if v == "" {
				g.Service.Port = 0
				return nil
			}
			p, err := strconv.Atoi(v)
			if err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("port %q is not 1-65535", v)
			}
			g.Service.Port = p
			return nil
		}},
	{"service.endpoint", "the satelled URL the CLI pushes to ($" + config.EnvServerEndpoint + " overrides)",
		func(g config.GlobalConfig) (string, string) { return g.Service.Endpoint, g.Service.ResolveEndpoint() },
		func(g *config.GlobalConfig, v string) error {
			if v != "" {
				if u, err := url.Parse(v); err != nil || u.Scheme == "" || u.Host == "" {
					return fmt.Errorf("endpoint %q is not a URL", v)
				}
			}
			g.Service.Endpoint = v
			return nil
		}},
}

func findSetting(key string) (machineSetting, error) {
	keys := make([]string, len(machineSettings))
	for i, s := range machineSettings {
		if s.key == key {
			return s, nil
		}
		keys[i] = s.key
	}
	return machineSetting{}, fmt.Errorf("unknown setting %q (one of %s)", key, strings.Join(keys, ", "))
}

func settingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Print or set this machine's settings (~/.satelle/config.toml)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "# %s\n", config.GlobalConfigPath())
			for _, s := range machineSettings {
				v, eff := s.get(g)
				line := fmt.Sprintf("%-17s %q", s.key, v)
				if eff != "" && eff != v {
					line += "  (in effect: " + eff + ")"
				}
				fmt.Fprintf(out, "%-60s # %s\n", line, s.doc)
			}
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a machine setting; an empty value restores its default",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := findSetting(args[0])
			if err != nil {
				return err
			}
			g, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			if err := s.set(&g, strings.TrimSpace(args[1])); err != nil {
				return err
			}
			if err := config.SaveGlobal(g); err != nil {
				return err
			}
			v, _ := s.get(g)
			fmt.Fprintf(cmd.OutOrStdout(), "%s = %q\n", s.key, v)
			return nil
		},
	})
	return cmd
}
