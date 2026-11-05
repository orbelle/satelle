package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// defaultCallbackPort is the loopback port of the registered OAuth redirect.
const defaultCallbackPort = 53682

// resolveServer is the --server flag, else the machine's [hosted] server,
// else hosted.DefaultServer.
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
	return hosted.DefaultServer, nil
}

// dial opens a gRPC client to server with the default credential store
// (SATELLE_TOKEN, else the stored login).
func dial(server string) (*hosted.Client, error) {
	return hosted.Dial(server, hosted.DefaultStore(), hosted.Options{})
}

func loginCmd() *cobra.Command {
	var server string
	var noBrowser bool
	var timeout time.Duration
	var port int
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to satelle-server (OAuth 2.1 + PKCE)",
		Long: `login runs the browser OAuth flow against the server, stores the tokens in the
per-user credential store ($XDG_CONFIG_HOME/satelle) and records the server
in ~/.satelle/config.toml so one sign-in serves every repo. SATELLE_TOKEN, when
set, is used instead of the stored login by every other command.`,
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
			c, err := hosted.Dial(srv, store, hosted.Options{})
			if err != nil {
				return err
			}
			defer c.Close()
			who, err := c.Account.GetMe(cmd.Context(), &satellepb.GetMeRequest{})
			if err != nil {
				var unknown *hosted.UnknownSubjectError
				if errors.As(err, &unknown) {
					return unknown
				}
				fmt.Fprintf(out, "Signed in to %s (could not fetch identity: %v)\n", srv, hosted.Describe("AccountService.GetMe", err))
				return nil
			}
			if fresh, err := store.Load(srv); err == nil {
				fresh.DisplayName, fresh.Email, fresh.PrincipalID = who.GetDisplayName(), who.GetEmail(), who.GetId()
				_ = store.Save(fresh)
			}
			printPrincipal(cmd, srv, who)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "server URL (recorded machine-wide)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the authorize URL instead of opening a browser")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "how long to wait for the browser approval")
	cmd.Flags().IntVar(&port, "callback-port", defaultCallbackPort, "loopback port for the browser callback (0 picks a free port)")
	return cmd
}

func logoutCmd() *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored credential for the server on this machine",
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
	cmd.Flags().StringVar(&server, "server", "", "server URL")
	return cmd
}

func whoamiCmd() *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Print who the credential signs in as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv, err := resolveServer(server)
			if err != nil {
				return err
			}
			c, err := dial(srv)
			if err != nil {
				return err
			}
			defer c.Close()
			who, err := c.Account.GetMe(cmd.Context(), &satellepb.GetMeRequest{})
			if err != nil {
				return hosted.Describe("whoami", err)
			}
			printPrincipal(cmd, srv, who)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "server URL")
	return cmd
}

func printPrincipal(cmd *cobra.Command, server string, p *satellepb.GetMeResponse) {
	name := p.GetDisplayName()
	if name == "" {
		name = p.GetEmail()
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Signed in to %s as %s <%s> (role %s)\n", server, name, p.GetEmail(), p.GetRole())
}
