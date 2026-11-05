package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
)

// defaultCallbackPort is the loopback port of the registered OAuth redirect.
const defaultCallbackPort = 53682

// resolveServer is the --server flag, else the machine's [hosted] server.
func resolveServer(flag string) (string, error) {
	if s := strings.TrimRight(strings.TrimSpace(flag), "/"); s != "" {
		return s, nil
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		return "", err
	}
	if s := gc.Hosted.ResolveServer(); s != "" {
		return s, nil
	}
	return "", errors.New("no hosted server configured; pass --server <url> (it is recorded machine-wide)")
}

func loginCmd() *cobra.Command {
	var server string
	var noBrowser bool
	var timeout time.Duration
	var port int
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to a hosted satelle-server (OAuth 2.1 + PKCE)",
		Long: `login runs the browser OAuth flow against the hosted server, stores the
tokens in the per-user credential store ($XDG_CONFIG_HOME/satelle) and records
the server in ~/.satelle/config.toml so one sign-in serves every repo.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv, err := resolveServer(server)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			opts := hosted.LoginOptions{Timeout: timeout, Out: out, Port: port}
			if !noBrowser {
				opts.OpenBrowser = hosted.OpenBrowser
			}
			cred, err := hosted.Login(cmd.Context(), nil, srv, opts)
			if err != nil {
				return err
			}
			store := hosted.FileStore{}
			if err := store.Save(cred); err != nil {
				return err
			}
			if err := config.SaveGlobalHostedServer(srv); err != nil {
				return err
			}
			who, err := hosted.NewClient(srv, store, nil).Me(cmd.Context())
			if err != nil {
				var unknown *hosted.UnknownSubjectError
				if errors.As(err, &unknown) {
					return unknown
				}
				fmt.Fprintf(out, "Signed in to %s (could not fetch identity: %v)\n", srv, err)
				return nil
			}
			if fresh, err := store.Load(srv); err == nil {
				fresh.DisplayName, fresh.Email, fresh.PrincipalID = who.DisplayName, who.Email, who.ID
				_ = store.Save(fresh)
			}
			printPrincipal(cmd, srv, who)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "hosted server URL (recorded machine-wide)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the authorize URL instead of opening a browser")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "how long to wait for the browser approval")
	cmd.Flags().IntVar(&port, "callback-port", defaultCallbackPort, "loopback port for the browser callback (0 picks a free port)")
	return cmd
}

func logoutCmd() *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored hosted-server credential on this machine",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv, err := resolveServer(server)
			if err != nil {
				return err
			}
			if err := (hosted.FileStore{}).Delete(srv); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logged out of %s.\n", srv)
			if hosted.SessionToken() != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is still set; the session token remains active.\n", hosted.SessionTokenEnv)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "hosted server URL")
	return cmd
}

func whoamiCmd() *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Print who the stored credential signs in as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv, err := resolveServer(server)
			if err != nil {
				return err
			}
			who, err := hosted.NewClient(srv, hosted.DefaultStore(), nil).Me(cmd.Context())
			if err != nil {
				return fmt.Errorf("whoami: %w", err)
			}
			printPrincipal(cmd, srv, who)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "hosted server URL")
	return cmd
}

func printPrincipal(cmd *cobra.Command, server string, p hosted.Principal) {
	name := p.DisplayName
	if name == "" {
		name = p.Email
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Signed in to %s as %s <%s> (role %s)\n", server, name, p.Email, p.Role)
}
