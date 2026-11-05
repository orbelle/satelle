package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
)

// satelle sync carries work state only: items, ledger and attachments
// (REBUILD Q-config-sync). .satelle/ configuration is in git.
func init() {
	var syncServer string
	var syncDryRun bool
	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Push this repo's work state (items, ledger, attachments) to the bound hosted project",
		Long: `sync keeps this repo's work state — items, their ledger and attachments — in
step with the bound hosted project. Configuration (.satelle/) is not synced: it
lives in git.

  push        publish this machine's work state
  pull        bring in the hosted work state
  workstate   the per-story work-state verbs
  scopes      print the resolved sync scope

Bare "satelle sync" is "satelle sync push". The scope comes from your account
and the repo's binding: unbound or signed out is local (nothing leaves the
machine).`,
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncPush(cmd, syncServer, syncDryRun)
		},
	}
	syncCmd.Flags().StringVar(&syncServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	syncCmd.Flags().BoolVar(&syncDryRun, "dry-run", false, "Preview what would be pushed.")
	syncCmd.Flags().Bool("verbose", false, verboseReachabilityUsage)
	syncCmd.AddCommand(&cobra.Command{
		Use:         "scopes",
		Short:       "Print the resolved sync scope",
		Long:        "Print the sync scope this repo resolves to: which work state push and pull carry.",
		Annotations: needsStore(),
		RunE:        runSyncScopes,
	})
	syncCmd.AddCommand(newSyncPushCmd(), newSyncPullCmd(), newSyncWorkstateCmd())
	register(syncCmd)
}

func newSyncPushCmd() *cobra.Command {
	var server string
	var dryRun bool
	cmd := &cobra.Command{
		Use:         "push",
		Short:       "Publish this machine's work state (items, ledger, attachments)",
		Long:        "Publish this machine's work state (items, ledger, attachments) to the bound hosted project.",
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncPush(cmd, server, dryRun)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview what would be pushed.")
	cmd.Flags().Bool("verbose", false, verboseReachabilityUsage)
	return cmd
}

func runSyncPush(cmd *cobra.Command, serverArg string, dryRun bool) error {
	a, err := appFrom(cmd)
	if err != nil {
		return err
	}
	if err := refusePullOnly(a.RepoRoot); err != nil {
		return err
	}
	if err := refuseIfPushOff("sync push"); err != nil {
		return err
	}
	if scope, err := syncScopeGate(cmd, a.Config, a.RepoRoot, resolveServer(serverArg), "Nothing to push"); err != nil || scope == config.LocalScope {
		return err
	}
	return runSyncWorkstatePush(cmd, serverArg, dryRun, false, verboseRequested(cmd))
}

func newSyncPullCmd() *cobra.Command {
	var server string
	var dryRun, force bool
	cmd := &cobra.Command{
		Use:         "pull",
		Short:       "Bring in the hosted work state (items, ledger, attachments)",
		Long:        "Bring in the hosted project's work state (items, ledger, attachments) and apply it locally.",
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			if scope, err := syncScopeGate(cmd, a.Config, a.RepoRoot, resolveServer(server), "Nothing to pull"); err != nil || scope == config.LocalScope {
				return err
			}
			return runSyncWorkstatePull(cmd, server, dryRun, force, verboseRequested(cmd))
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview what would be pulled.")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite local work state with the hosted copy.")
	cmd.Flags().Bool("verbose", false, verboseReachabilityUsage)
	return cmd
}

// refuseIfPushOff is the one guard every hosted push verb calls first: a
// machine whose global config turns push off makes no hosted write.
func refuseIfPushOff(verb string) error {
	if off, setting := config.MachinePushOff(); off {
		return fmt.Errorf("%s refused: hosted push is off for this machine (%s); pulls still run", verb, setting)
	}
	return nil
}

// syncScopeGate is the pre-flight every sync verb shares: it resolves the
// scope and, when it is local, prints one line — what skipped and why. The
// caller stops on a local scope. Unbound or signed out touches no network.
func syncScopeGate(cmd *cobra.Command, cfg config.Config, repoRoot, server, what string) (config.Scope, error) {
	res, err := resolveSyncScope(cmd, cfg, repoRoot, server)
	if err != nil {
		return config.LocalScope, err
	}
	if res.Scope == config.LocalScope {
		fmt.Fprintf(cmd.OutOrStdout(), "%s — %s\n", what, res.Line())
	}
	return res.Scope, nil
}

// loadRepoConfig loads the repo config and the resolved repo root + data dir for
// a (non-store-backed) sync config command. A missing config is not an error —
// the zero config runs on defaults (all-local -> nothing to push).
func loadRepoConfig() (config.Config, string, string, error) {
	cfg, cfgPath, err := config.Load("")
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return config.Config{}, "", "", fmt.Errorf("load config: %w", err)
	}
	repoRoot := "."
	if cfgPath != "" {
		repoRoot = config.RepoRootFromConfigPath(cfgPath)
	} else if cwd, e := os.Getwd(); e == nil {
		repoRoot = cwd
	}
	return cfg, repoRoot, cfg.ResolveDataDir(repoRoot), nil
}

// runSyncScopes prints the resolved sync scope.
func runSyncScopes(cmd *cobra.Command, args []string) error {
	a, err := appFrom(cmd)
	if err != nil {
		return err
	}
	res, err := resolveSyncScope(cmd, a.Config, a.RepoRoot, resolveServer(""))
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), res.Line())
	return nil
}
