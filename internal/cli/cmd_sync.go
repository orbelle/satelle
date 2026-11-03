package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/snapsync"
	"github.com/bobmcallan/satelle/internal/subsync"
	"github.com/bobmcallan/satelle/internal/syncscope"
)

// `satelle sync` is the scoped-sync command group (epic:scoped-sync): scopes
// inspection, config push/deploy, and documents push/pull. Bare `satelle sync`
// is the default execute verb — it runs the configured sync across every
// area (config push, documents push+pull, work-state push) by composing the
// per-kind handlers under the one resolved scope (resolveSyncScope). The per-kind
// subcommands remain for driving one area (or a one-way direction) on its own.
func init() {
	var syncServer string
	var syncDryRun bool
	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Run the sync for every area the resolved scope covers (config, documents, workstate)",
		Long: `sync keeps the synced .satelle areas in step with the bound hosted project, as
git keeps a branch with its remote:

  push        publish this machine's changes (config, documents, work-state)
  pull        bring in what only the hosted copy changed; never overwrites a local edit
  reconcile   choose a side per change when both machines changed the same thing
  status      where each area stands, and the next command

Bare "satelle sync" first writes the project tier's team-owned files into
.satelle, backing up local edits, then runs the configured sync. Otherwise: rehydrate is
the full-tree recovery of a wiped .satelle and overwrites local files; config,
documents, workstate and bindings drive one area; config deploy --version
materialises a pinned version.

The scope comes from your account and the repo's binding, never from a config
key: unbound or signed out is local (nothing leaves the machine), a project of
your personal team is personal, a project of a team you belong to is team. Bind
with "satelle project bind <slug>"; "satelle sync status" names the scope and
where it came from. Files whose name carries a .local segment
(satelle.local.toml, notes.local.md) never leave the machine at any scope —
unconditional, not a setting. Team catalogs are a separate verb: satelle publish.`,
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd, syncServer, syncDryRun)
		},
	}
	syncCmd.Flags().StringVar(&syncServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	syncCmd.Flags().BoolVar(&syncDryRun, "dry-run", false, "Preview what each area would push; only the scope is asked of the account (documents pull is not previewed).")
	syncCmd.Flags().Bool("prune", false, pruneFlagUsage)
	syncCmd.Flags().Bool("force", false, forceFlagUsage)
	syncCmd.Flags().Bool("verbose", false, verboseReachabilityUsage)
	syncCmd.AddCommand(&cobra.Command{
		Use:   "scopes",
		Short: "Print the resolved sync scope, each .satelle area under it, and shared files within a personal scope",
		Long: `Print the resolved sync scope — local, personal or team, and the account,
team and project it came from — then each .satelle area under it and, under a
personal scope, which files are nonetheless shared.

Read this before a sync, not after: scope is what decides whether a file leaves
the machine, and the per-file exceptions inside a personal area are exactly what
a whole-area reading gets wrong. Read-only.`,
		Annotations: needsStore(),
		RunE:        runSyncScopes,
	})
	// The git-aligned verbs are listed first in --help; everything else is the
	// per-area and recovery surface.
	syncCmd.AddGroup(
		&cobra.Group{ID: syncVerbsGroup, Title: "Sync verbs (push, pull, reconcile, status):"},
		&cobra.Group{ID: syncAreaGroup, Title: "Per-area and recovery commands:"},
	)
	for _, c := range []*cobra.Command{newSyncPushCmd(), newSyncPullCmd(), newSyncReconcileCmd(), newSyncStatusCmd()} {
		c.GroupID = syncVerbsGroup
		syncCmd.AddCommand(c)
	}
	for _, c := range []*cobra.Command{newSyncConfigCmd(), newSyncDocumentsCmd(), newSyncWorkstateCmd(), newSyncBindingsCmd(), newSyncRehydrateCmd()} {
		c.GroupID = syncAreaGroup
		syncCmd.AddCommand(c)
	}
	register(syncCmd)
}

// newSyncRehydrateCmd is the recover path (epic:workspace-rehydrate order:4):
// config deploy → documents pull → workstate pull. Does not push.
// Not store-annotated: config deploy must run before agents.toml exists on an
// empty tree; the store is opened after deploy for workstate pull only.
func newSyncRehydrateCmd() *cobra.Command {
	var server string
	var force, verbose bool
	cmd := &cobra.Command{
		Use:         "rehydrate",
		Annotations: hostedOrigin(nil),
		Short:       "Full-tree recover: config deploy + documents pull + workstate pull, overwriting local files (no push)",
		Long: `rehydrate is the empty-tree / wiped-.satelle recover path.

Full operator path:
  1. install satelle          — binary on PATH
  2. satelle login            — global credentials
  3. satelle project bind <slug>
       creates .satelle/satelle.toml with [sync] project when absent
  4. satelle sync rehydrate   — this command, which runs:
       a. config deploy       — restores substrate AND satelle.toml (the settings
          area) from the bound project's personal config collection; the local
          [sync] project binding is preserved (never taken from the deployed
          file)
       b. documents pull
       c. workstate pull      — stories/executions/ledger into the local store

The scope is resolved from your account and the binding, never restored from
another machine's satelle.toml: a deployed file's scope keys are dropped. A
local scope (unbound, signed out, or not a member of the project's team) pulls
nothing, and rehydrate names why.

Does not push; on a healthy machine use "satelle sync pull". Optional --force and --verbose are passed to workstate pull only.`,
		RunE: func(c *cobra.Command, args []string) error {
			return runSyncRehydrate(c, server, force, verbose)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	cmd.Flags().BoolVar(&force, "force", false, "Pass --force to workstate pull (upsert on conflict).")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Pass --verbose to workstate pull (list every story that left work with no recorded head).")
	return cmd
}

func runSyncRehydrate(cmd *cobra.Command, serverArg string, force, verbose bool) error {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "rehydrate: config deploy…")
	outcome, err := runSyncConfigDeployOutcome(cmd, serverArg, "", 0)
	if err != nil {
		return err
	}
	cfg, repoRoot, _, err := loadRepoConfig()
	if err != nil {
		return err
	}
	res, err := resolveSyncScope(cmd, cfg, repoRoot, resolveServer(serverArg))
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "rehydrate: "+res.Line())
	fmt.Fprintln(out, "rehydrate: documents pull…")
	if err := runSyncDocumentsPull(cmd, serverArg, ""); err != nil {
		return err
	}
	// Open store after config deploy so agents.toml (and scopes) may exist.
	if err := openAppForCmd(cmd); err != nil {
		return fmt.Errorf("rehydrate: open store after config deploy: %w — ensure personal config was pushed (or run satelle init), then retry", err)
	}
	defer closeAppForCmd(cmd)
	fmt.Fprintln(out, "rehydrate: workstate pull…")
	if err := runSyncWorkstatePull(cmd, serverArg, false, force, verbose); err != nil {
		return err
	}
	fmt.Fprintln(out, rehydrateDoneSummary(outcome, res))
	return nil
}

// rehydrateDoneSummary is the truthful closing line for rehydrate: what was
// and was not restored, and the scope the account resolved.
func rehydrateDoneSummary(o deployOutcome, res syncscope.Resolution) string {
	settings := "settings: not restored"
	if o.SettingsRestored {
		settings = "settings: restored"
	}
	return fmt.Sprintf("rehydrate: done — %s | %s | config files: %d | (documents + workstate reported above)",
		res.Line(), settings, o.Written)
}

// deployOutcome is the structured result of a config deploy so rehydrate can
// report facts rather than guess (sty_ea18294f AC4).
type deployOutcome struct {
	Written          int
	Missing          int
	SkippedLocal     int
	SettingsRestored bool
}

// runSync is the bare `satelle sync` default action: run the configured sync
// across every area by composing the per-kind handlers, in a fixed order —
// authored config up, documents both ways, work-state up. Each handler runs
// under the one resolved scope and prints its own result; a local scope is a
// no-op. `sync config deploy` is intentionally NOT part of the bundle — it
// materialises a whole partition over local files ("set up X like Y"), which is
// a deliberate operation, not a routine sync. Any handler error stops the run
// (fail-fast) so a partial sync surfaces rather than hides.
func runSync(cmd *cobra.Command, serverArg string, dryRun bool) error {
	a, err := appFrom(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if err := syncTeamWriteDown(cmd, a, dryRun); err != nil {
		return err
	}
	if scope, err := syncScopeGate(cmd, a.Config, a.RepoRoot, resolveServer(serverArg), "Nothing to sync"); err != nil || scope == config.LocalScope {
		return err
	}
	if err := refusePullOnly(a.RepoRoot); errors.Is(err, errPullOnly) {
		fmt.Fprintf(out, "push skipped: %v\n", err)
		return runSyncPullsOfBareSync(cmd, serverArg, dryRun)
	} else if err != nil {
		return err
	}
	return runSyncPushLegs(cmd, serverArg, dryRun, func() error {
		return runSyncPullsOfBareSync(cmd, serverArg, dryRun)
	})
}

// syncTeamWriteDown is bare sync's first, local step (sty_4cc971f7): the tree is
// brought to the project tier before any push leg, so the config push sends the
// team version and never publishes a stray local edit. Under --dry-run it lists
// what it would write and writes nothing.
func syncTeamWriteDown(cmd *cobra.Command, a *app.App, dryRun bool) error {
	return writeDownAndReport(cmd, a.ProcessConfig, a.ProcessRoot, a.RuntimeDir, a.RepoRoot, a.Config, dryRun)
}

// writeDownAndReport runs writeDownProjectTier and reports what it wrote: each
// warning to stderr, the count (or, under dryRun, the paths) to stdout.
func writeDownAndReport(cmd *cobra.Command, process config.Config, processRoot, runtimeDir, repoRoot string, tree config.Config, dryRun bool) error {
	res, err := writeDownProjectTier(process, processRoot, runtimeDir, repoRoot, tree, dryRun)
	for _, w := range res.Warnings() {
		fmt.Fprintln(cmd.ErrOrStderr(), w)
	}
	if err != nil {
		return err
	}
	if len(res.Written) == 0 {
		return nil
	}
	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "team substrate: would write %d file(s) from the project tier: %s\n", len(res.Written), strings.Join(res.Written, ", "))
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "team substrate: %d file(s) written from the project tier\n", len(res.Written))
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

// runSyncPushLegs is the one push composition (sty_0fa4f732): the push of every
// synced area, in a fixed order — authored config, documents, then work-state.
// Each leg runs under the resolved scope and reads --dry-run, --force,
// --prune, --verbose and --server as the per-area pushes do. Bare `sync` and `sync push`
// both run it; between documents and work-state it calls between (bare sync's
// pulls, nil for a push, which pulls nothing), so bare sync's order is unchanged.
func runSyncPushLegs(cmd *cobra.Command, serverArg string, dryRun bool, between func() error) error {
	if off, setting := config.MachinePushOff(); off {
		if between == nil {
			return refuseIfPushOff("sync push")
		}
		fmt.Fprintln(cmd.OutOrStdout(), "push skipped: hosted push is off for this machine ("+setting+")")
		return between()
	}
	ref, err := runSyncConfigPushLeg(cmd, serverArg, "", dryRun)
	if err != nil {
		return err
	}
	if ref.refused() {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+ref.message())
	}
	if err := runSyncDocumentsPush(cmd, serverArg, "", dryRun); err != nil {
		return err
	}
	if between != nil {
		if err := between(); err != nil {
			return err
		}
	}
	return runSyncWorkstatePush(cmd, serverArg, dryRun, false, verboseRequested(cmd))
}

// runSyncPullsOfBareSync is the pulling half of bare `satelle sync`, run between
// its document and work-state pushes.
func runSyncPullsOfBareSync(cmd *cobra.Command, serverArg string, dryRun bool) error {
	out := cmd.OutOrStdout()
	// The documents PULL stays in bare sync — deliberately, unlike config deploy
	// and workstate pull, which sit behind `sync rehydrate` (sty_4c3729e7 AC7).
	// The difference is what each one does to local state. Deploy and workstate
	// pull MATERIALISE a partition over local files; they answer "make this repo
	// look like that one", so they are recovery, not routine. The documents pull
	// is INCREMENTAL and cursor-driven: it applies only what changed since this
	// repo's own cursor, and after this story it neither rewrites bytes it
	// already has nor stops on a file it cannot write. That makes documents the
	// one area that is genuinely two-way in normal use — an operator writing a
	// document on another machine expects it here without invoking recovery.
	// Moving it behind rehydrate would make routine convergence an explicit
	// recovery step, which is the wrong default for the only bidirectional area.
	if dryRun {
		fmt.Fprintln(out, "documents pull: skipped under --dry-run (pull has no preview).")
	} else if err := runSyncDocumentsPull(cmd, serverArg, ""); err != nil {
		return err
	}
	// The workspace bindings layer (sty_01949949) rides the aggregate (its
	// caller already refused a local scope); it applies from the TEAM catalog and
	// is a no-op for a repo with no team workspace. Pull, not push: the aggregate
	// converges this repo onto the workspace's decision; publishing bindings
	// is an explicit `sync bindings push`.
	if dryRun {
		fmt.Fprintln(out, "bindings pull: skipped under --dry-run (pull has no preview).")
		return nil
	}
	return runSyncBindingsPull(cmd, serverArg, "")
}

// newSyncConfigCmd builds the `satelle sync config` group — the CLI counterpart
// to the hosted server's versioned, per-user config store (epic:scoped-sync,
// order:5). push uploads authored config per its RESOLVED scope (skipping local,
// honoring the per-file shared flag) as new per-file versions; deploy/pull
// materializes the latest (or a pinned) version into this repo byte-for-byte —
// "set up project X like project Y". These commands are not store-backed: they
// load the repo config directly and talk to the hosted server, like login/project.
func newSyncConfigCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "config",
		Short: "Push/deploy authored config for this repo's bound hosted project (local default: no hosted write)",
		Long: `Move authored config between this repo and its bound hosted project: push what
you have, or deploy what the project holds.

Nothing leaves the machine by default — a hosted write happens only under a
personal or team scope, so running this in an unbound or signed-out repo is a
no-op by design. Deploy OVERWRITES the local authored copy.`,
	}

	var pushServer, pushWorkspace string
	var dryRun bool
	push := &cobra.Command{
		Use:   "push",
		Short: "Upload authored config to the versioned store (a new version per file)",
		Long: `push walks the authored-config areas (workflows/principles/skills/constitution/
agents/tasks/settings) under the resolved scope — nothing when it is local — and uploads
each file as a new version into this repo's bound hosted PROJECT only
(epic:sync-publish). Files go as on disk, except satelle.toml
loses its project binding. Before sending, a shared agents.toml
with machine-specific content is refused (see 'satelle help agent-dispatch') and
the deployed system must validate, as for 'satelle init'. Never uploaded: .local
files, config/.env, paths in <data dir>/.satelleignore. Team is not a sync
destination (use satelle publish). Requires "satelle project bind <slug>".

Pushes publish each area as one snapshot (deletes propagate), refused when another machine pushed since your last sync: deploy first, or pass --force.

Only the substrate holder publishes team-owned files; a member's stay local.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncConfigPush(cmd, pushServer, pushWorkspace, dryRun)
		},
	}
	push.Flags().StringVar(&pushServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	push.Flags().StringVar(&pushWorkspace, "workspace", "", "Ignored for push (sync is personal-only; kept for flag compatibility).")
	push.Flags().BoolVar(&dryRun, "dry-run", false, "List what would be pushed, and run the refusal and validation checks; only the scope is asked of the account.")
	push.Flags().Bool("prune", false, pruneFlagUsage)
	push.Flags().Bool("force", false, forceFlagUsage)
	push.Flags().Bool("snapshot-only", false, snapshotOnlyFlagUsage)
	group.AddCommand(push)

	var deployServer, deployWorkspace string
	var version int
	deploy := &cobra.Command{
		Use:   "deploy",
		Short: "Materialize config from the store into this repo (set up X like Y)",
		Long: `deploy fetches this repo's bound hosted PROJECT's config
(or an explicit --workspace when reading another developer's personal set) and
writes every file byte-for-byte into this repo's data dir — including satelle.toml
(settings), less any removed scope key. The local [sync] project
binding is preserved after restore (never taken from the deployed file).
--version N pins a per-file version (default: latest). This is the "set up
project X like project Y" operation for process config. Requires "satelle
project bind <slug>". Deploy always materializes from the bound project
partition, whatever the resolved scope.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncConfigDeploy(cmd, deployServer, deployWorkspace, version)
		},
	}
	deploy.Flags().StringVar(&deployServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	deploy.Flags().StringVar(&deployWorkspace, "workspace", "", "Workspace to deploy from (overrides the active workspace).")
	deploy.Flags().IntVar(&version, "version", 0, "Pin a per-file version (default: latest head of each path).")
	group.AddCommand(deploy)

	return group
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

// sharedSyncNote warns, under a personal scope, when a file is frontmarked
// shared:true: that flag does not route sync to a team (epic:sync-publish).
// Under a team scope every file is the team's, and nothing needs saying.
func sharedSyncNote(scope config.Scope, files []config.ConfigFile) string {
	if scope != config.PersonalScope {
		return ""
	}
	for _, f := range files {
		if f.Tier == config.SharedTier {
			return "note: shared:true does not route sync to a team workspace — pushing to the bound personal project. Use satelle publish to expose artifacts to a team catalog."
		}
	}
	return ""
}

// resolveDeploySourceName is the workspace a deploy reads from: the --workspace
// flag wins, else the configured active workspace (personal default is valid —
// deploy your own personal config).
func resolveDeploySourceName(cfg config.Config, workspaceArg string) string {
	if ws := strings.TrimSpace(workspaceArg); ws != "" {
		return ws
	}
	return cfg.ResolveActiveWorkspace().Name
}

// runSyncConfigPush is `satelle sync config push`, the publish: team-owned
// files the holder gate kept local fail it, naming the holder.
func runSyncConfigPush(cmd *cobra.Command, serverArg, workspaceArg string, dryRun bool) error {
	ref, err := runSyncConfigPushLeg(cmd, serverArg, workspaceArg, dryRun)
	if err != nil {
		return err
	}
	if ref.refused() {
		return errors.New(ref.message())
	}
	return nil
}

// runSyncConfigPushLeg is the config push, returning the team-owned files the
// holder gate or the server kept local for the caller to report.
func runSyncConfigPushLeg(cmd *cobra.Command, serverArg, workspaceArg string, dryRun bool) (holderRefusal, error) {
	var ref holderRefusal
	if err := refuseIfPushOff("sync config push"); err != nil {
		return ref, err
	}
	cfg, repoRoot, dataDir, err := loadRepoConfig()
	if err != nil {
		return ref, err
	}
	if err := refusePullOnly(repoRoot); err != nil {
		return ref, err
	}
	_ = workspaceArg // sync is personal-only (epic:sync-publish)
	server := resolveServer(serverArg)
	if server == "" {
		return ref, fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}
	ign, err := config.LoadIgnore(dataDir)
	if err != nil {
		return ref, err
	}
	res, err := resolveSyncScope(cmd, cfg, repoRoot, server)
	if err != nil {
		return ref, err
	}
	out := cmd.OutOrStdout()
	if res.Scope == config.LocalScope {
		fmt.Fprintln(out, "No config to push — "+res.Line())
		return ref, nil
	}
	bundle, err := config.ConfigFiles(cfg, res.Scope, repoRoot, ign)
	if err != nil {
		return ref, err
	}
	files := bundle.Files
	printWithheldLocal := func() { printBundleExclusions(out, bundle) }
	if len(files) == 0 && !holdsStampOnly(cfg, repoRoot, ign) {
		if n := bundle.ExcludedCount(); n > 0 {
			printWithheldLocal()
			fmt.Fprintf(out, "nothing to push (%d withheld)\n", n)
			return ref, nil
		}
		fmt.Fprintln(out, "No config to push — the config areas are empty.")
		return ref, nil
	}
	if note := sharedSyncNote(res.Scope, files); note != "" {
		fmt.Fprintln(out, note)
	}
	return pushConfigFileSet(cmd, cfg, repoRoot, dataDir, ign, server, files, pushSetOpts{
		Verb: "sync config push", DryRun: dryRun, withheldLocal: printWithheldLocal,
	})
}

// holdsStampOnly reports whether any config area holds a stamp-only default
// (config.AreaStampOnlyDefaults). The snapshot push still publishes one that
// differs from what was synced, so a bundle without it is not nothing to push.
func holdsStampOnly(cfg config.Config, repoRoot string, ign config.Ignore) bool {
	for _, area := range config.ConfigAreas {
		if files, err := config.AreaStampOnlyDefaults(cfg, repoRoot, area, ign); err == nil && len(files) > 0 {
			return true
		}
	}
	return false
}

// pushRefusal is a push its checks refused before any upload: the
// configuration itself must be fixed.
type pushRefusal struct{ error }

func (r pushRefusal) Unwrap() error { return r.error }

// pushSetOpts is how pushConfigFileSet sends its set.
type pushSetOpts struct {
	// Verb names the command in a refusal.
	Verb   string
	DryRun bool
	// IgnoreScope sends the whole set as plain files, outside every area's
	// snapshot, whatever the resolved scope: a project's first publish of
	// its substrate, which has no snapshot to be behind.
	IgnoreScope bool
	// withheldLocal reports what the bundle left out; nil reports nothing.
	withheldLocal func()
}

// pushConfigFileSet is the one upload tail of every publish of configuration to
// the bound project: the transport and deployed-system checks first, then the
// holder gate, then the snapshot push or the plain upload. It returns the
// team-owned files the holder gate or the server kept local.
func pushConfigFileSet(cmd *cobra.Command, cfg config.Config, repoRoot, dataDir string, ign config.Ignore, server string, files []config.ConfigFile, opts pushSetOpts) (holderRefusal, error) {
	var ref holderRefusal
	if err := refusePullOnly(repoRoot); err != nil {
		return ref, err
	}
	out := cmd.OutOrStdout()
	printWithheldLocal := func() {
		if opts.withheldLocal != nil {
			opts.withheldLocal()
		}
	}
	// Refuse before anything else — a dry run included — so a configuration that
	// would not work on another machine is never published (sty_89b9df20).
	if err := checkPushFiles(opts.Verb, files); err != nil {
		return ref, pushRefusal{err}
	}
	if err := preflightPush(out, dataDir); err != nil {
		return ref, pushRefusal{err}
	}
	printWithheldBinding(out, files)
	if opts.DryRun {
		if opts.IgnoreScope {
			printDryRunAll(out, server, "config file(s)", files)
		} else {
			printDryRunChanges(cmd.Context(), out, cfg, server, repoRoot, dataDir, ign, files, "config file(s)")
		}
		printWithheldLocal()
		return ref, nil
	}
	// Bound project before any network (AC5).
	project, err := resolveBoundProject(cfg, repoRoot)
	if err != nil {
		return ref, err
	}
	client := newHostedClient(cmd.Context(), server, repoRoot)
	// Prefer skipping unchanged bytes via server manifest (sty_88e83180 AC6).
	// On manifest failure: degrade to full upload with a printed note.
	headSHA := map[string]string{}
	manifest, merr := client.ConfigManifest(cmd.Context(), project)
	if merr != nil {
		if errors.Is(merr, hosted.ErrLoginRequired) {
			return ref, merr
		}
		if snapshotOnlyRequested(cmd) {
			return ref, snapshotOnlyRefusal(merr)
		}
		fmt.Fprintf(out, "config manifest unavailable (%v) — uploading every file.\n", merr)
	} else {
		headSHA = headSHAByPath(manifest)
	}
	// Every upload below sends only what the holder gate lets through.
	files, ref, err = holderGate(cmd.Context(), client, cfg, repoRoot, server, project, files, headSHA)
	if err != nil {
		return ref, err
	}
	// notStored is every file the server refused to store: the push still sends
	// every other file and area, then exits non-zero naming each (sty_195dd215).
	var notStored error
	if merr == nil && !opts.IgnoreScope {
		// A push publishes each area's whole state as a snapshot (sty_fe5a8ed4):
		// refused when this machine is behind, so it never overwrites a newer
		// push from another machine. A server that cannot hold snapshots falls
		// through to the plain upload below.
		snap, handled, serr := pushConfigSnapshots(cmd, cfg, repoRoot, dataDir, ign, client, server, project, manifest, files)
		if serr != nil {
			var nh *hosted.NotHolderError
			var ue *snapsync.UploadError
			switch {
			case errors.As(serr, &nh):
				ref.add(nh.Path, nh.Holder)
			case errors.As(serr, &ue):
				notStored = serr
			default:
				return ref, serr
			}
		}
		if handled {
			// Team-owned files no area holds (scripts, hooks) are in no snapshot:
			// they go up as plain files beside it.
			c, u, n, perr := pushPlainConfigFiles(cmd, client, project, headSHA, teamAreaFiles(files), &ref)
			if perr = collectNotStored(&notStored, perr); perr != nil {
				return ref, perr
			}
			snap.created, snap.unchanged, snap.notUploaded = snap.created+c, snap.unchanged+u, snap.notUploaded+n
			printWithheldLocal()
			fmt.Fprintf(out, "Pushed %d of %d config file(s) to project %q on %s: %d new, %d unchanged, %d skipped (unchanged, not uploaded).\n",
				snap.created+snap.unchanged, len(files), project, server, snap.created, snap.unchanged, snap.notUploaded)
			return ref, notStored
		}
	}
	created, unchanged, notUploaded, err := pushPlainConfigFiles(cmd, client, project, headSHA, files, &ref)
	if err = collectNotStored(&notStored, err); err != nil {
		return ref, err
	}
	printWithheldLocal()
	uploaded := created + unchanged
	fmt.Fprintf(out, "Pushed %d of %d config file(s) to project %q on %s: %d new, %d unchanged, %d skipped (unchanged, not uploaded).\n",
		uploaded, len(files), project, server, created, unchanged, notUploaded)
	return ref, notStored
}

// collectNotStored folds a plain upload's *snapsync.UploadError into the
// refusals gathered so far; any other error is returned, for the caller to stop on.
func collectNotStored(notStored *error, err error) error {
	var ue *snapsync.UploadError
	if err != nil && !errors.As(err, &ue) {
		return err
	}
	*notStored = errors.Join(*notStored, err)
	return nil
}

// pushPlainConfigFiles uploads each file whose bytes the hosted head does not
// already hold, with no snapshot check. A path the server refuses because the
// caller is not the substrate holder is recorded in ref, and the rest still go.
// So does every other file after one the server refuses to store: those come
// back as one *snapsync.UploadError. A missing login stops at once.
func pushPlainConfigFiles(cmd *cobra.Command, client *hosted.Client, project string, headSHA map[string]string, files []config.ConfigFile, ref *holderRefusal) (created, unchanged, notUploaded int, err error) {
	var failures []snapsync.UploadFailure
	for _, f := range files {
		if contentMatchesSHA(headSHA[f.Path], f.Content) {
			notUploaded++
			continue
		}
		res, perr := client.PushConfigFile(cmd.Context(), project, f.Path, f.Content)
		if perr != nil {
			var nh *hosted.NotHolderError
			if errors.As(perr, &nh) {
				ref.add(nh.Path, nh.Holder)
				continue
			}
			if errors.Is(perr, hosted.ErrLoginRequired) {
				return created, unchanged, notUploaded, perr
			}
			failures = append(failures, snapsync.UploadFailure{Path: f.Path, Err: perr})
			continue
		}
		if res.Created {
			created++
		} else {
			unchanged++
		}
	}
	if len(failures) > 0 {
		return created, unchanged, notUploaded, &snapsync.UploadError{Failures: failures}
	}
	return created, unchanged, notUploaded, nil
}

// teamAreaFiles is the files of the bundle labelled config.TeamArea.
func teamAreaFiles(files []config.ConfigFile) []config.ConfigFile {
	var out []config.ConfigFile
	for _, f := range files {
		if f.Area == config.TeamArea {
			out = append(out, f)
		}
	}
	return out
}

// printDryRunAll is a dry run's listing of every file of the set, for a push
// with no last push to compare it with. noun names the set ("document(s)").
func printDryRunAll(out io.Writer, server, noun string, files []config.ConfigFile) {
	fmt.Fprintf(out, "Would push %d %s to the bound project on %s:\n", len(files), noun, server)
	for _, f := range files {
		fmt.Fprintf(out, "  %-40s -> personal\n", f.Path)
	}
}

// printDryRunChanges is a dry run's listing of what a push would send. Each
// snapshot area is asked where it stands against the hosted copy — the
// push-planning path a real push runs (snapsync.Driver.Status, via
// probeSubstrate) — so a file whose hosted head differs from the tree is listed
// even when this checkout's last-push record matches it (sty_195dd215). An area
// the hosted copy cannot answer for falls back to the comparison with its
// recorded base that doctor and `sync status` make offline (snapsync.OfflineDrift),
// and says so. Team files ride no snapshot and the hosted head decides them, so
// they are listed apart. noun names the set ("config file(s)", "document(s)").
func printDryRunChanges(ctx context.Context, out io.Writer, cfg config.Config, server, repoRoot, dataDir string, ign config.Ignore, files []config.ConfigFile, noun string) {
	project, err := resolveBoundProject(cfg, repoRoot)
	if err != nil {
		fmt.Fprintln(out, "(no bound project — cannot compare with the last push; listing every file)")
		printDryRunAll(out, server, noun, files)
		return
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		abs = repoRoot
	}
	var areas, snapAreas []string
	byArea := map[string][]config.ConfigFile{}
	for _, f := range files {
		if _, seen := byArea[f.Area]; !seen {
			areas = append(areas, f.Area)
			if f.Area != config.TeamArea {
				snapAreas = append(snapAreas, f.Area)
			}
		}
		byArea[f.Area] = append(byArea[f.Area], f)
	}
	online := map[string]snapsync.AreaStatus{}
	var offline error
	if len(snapAreas) > 0 {
		// No repo root: a dry run only reads, so it registers no checkout location.
		client := newHostedClient(ctx, server, "")
		for _, st := range probeSubstrate(ctx, cfg, repoRoot, dataDir, server, project, client, snapAreas) {
			if st.State == snapsync.StateUnavailable {
				offline = st.Err
				continue
			}
			online[st.Area] = st
		}
	}
	var body strings.Builder
	listAll := func(area, note string) int {
		fmt.Fprintf(&body, "  %s: %d changed (%s)\n", area, len(byArea[area]), note)
		for _, f := range byArea[area] {
			fmt.Fprintf(&body, "    %-40s -> personal (%s)\n", f.Path, note)
		}
		return len(byArea[area])
	}
	n, refused := 0, 0
	for _, area := range snapAreas {
		local, err := snapsync.LocalShas(cfg, repoRoot, area, ign)
		var defaults map[string]map[string]bool
		if err == nil {
			defaults, err = config.AreaDefaultShas(cfg, repoRoot, area, ign)
		}
		var stampOnly map[string]string
		if err == nil {
			stampOnly, err = snapsync.StampOnlyShas(cfg, repoRoot, area, ign)
		}
		var drift snapsync.Drift
		if err == nil {
			drift, err = snapsync.OfflineDrift(server, project, abs, dataDir, area, local, stampOnly, defaults, ign)
		}
		listChanged := func(changed []string, sinceBase map[string]bool) int {
			fmt.Fprintf(&body, "  %s: %d changed\n", area, len(changed))
			for _, p := range changed {
				switch _, ok := local[p]; {
				case !ok:
					fmt.Fprintf(&body, "    %-40s (deleted)\n", p)
				case sinceBase != nil && !sinceBase[p]:
					fmt.Fprintf(&body, "    %-40s -> personal (hosted head differs)\n", p)
				default:
					fmt.Fprintf(&body, "    %-40s -> personal\n", p)
				}
			}
			return len(changed)
		}
		if st, ok := online[area]; ok {
			switch st.State {
			case snapsync.StateInStep:
			case snapsync.StateNeverSynced:
				n += listAll(area, "never synced")
			case snapsync.StateUnpushed:
				sinceBase := map[string]bool{}
				for _, p := range drift.Changed {
					sinceBase[p] = true
				}
				n += listChanged(st.Changed, sinceBase)
			default:
				refused++
				fmt.Fprintf(&body, "  %s: %s — a push would be refused (%s)\n", area, st.State, nextCommand(st))
			}
			continue
		}
		switch {
		case err != nil:
			n += listAll(area, fmt.Sprintf("cannot compare with the last push: %v", err))
		case !drift.Synced:
			n += listAll(area, "never synced")
		case len(drift.Changed) > 0:
			n += listChanged(drift.Changed, nil)
		}
	}
	team := byArea[config.TeamArea]
	if len(team) > 0 {
		fmt.Fprintln(&body, "  team files (sent only if the hosted bytes differ):")
		for _, f := range team {
			fmt.Fprintf(&body, "    %-40s -> personal\n", f.Path)
		}
	}
	if offline != nil {
		fmt.Fprintf(out, "(offline — compared with the last push only: %v)\n", offline)
	}
	against := "against the hosted copy"
	if offline != nil {
		against = "since each area's last push"
	}
	if n == 0 && refused == 0 && len(team) == 0 {
		if offline != nil {
			fmt.Fprintln(out, "nothing to push — every area matches its last push")
		} else {
			fmt.Fprintln(out, "nothing to push — every area matches its last push and the hosted copy")
		}
		return
	}
	fmt.Fprintf(out, "Would push %d %s to the bound project on %s — changed %s:\n", n, noun, server, against)
	fmt.Fprint(out, body.String())
}

// printBundleExclusions names every path a push left out of the bundle, each
// under its own reason: files that never leave the machine (.local, or named by
// .satelleignore), and unedited
// embedded defaults the installed binary supplies (sty_097833e8).
func printBundleExclusions(out io.Writer, b config.Bundle) {
	for _, p := range b.Withheld {
		if config.LocalOnlyPath(p) {
			fmt.Fprintf(out, "  withheld (never syncs, .local): %s\n", p)
		} else {
			fmt.Fprintf(out, "  withheld (never syncs, %s): %s\n", config.IgnoreFileName, p)
		}
	}
	for _, p := range b.UneditedDefaults {
		fmt.Fprintf(out, "  withheld (unedited embedded default — comes from the binary): %s\n", p)
	}
}

// headSHAByPath indexes ConfigItem.BlobSHA256 by path.
func headSHAByPath(items []hosted.ConfigItem) map[string]string {
	out := make(map[string]string, len(items))
	for _, it := range items {
		if it.Path != "" && it.BlobSHA256 != "" {
			out[it.Path] = it.BlobSHA256
		}
	}
	return out
}

// contentMatchesSHA reports whether content's sha256 matches the server value.
// Normalises quotes and an optional sha256: prefix; unknown formats return
// false so we never skip on doubt (sty_88e83180 AC6).
func contentMatchesSHA(sha string, content []byte) bool {
	sha = strings.TrimSpace(sha)
	sha = strings.Trim(sha, `"'`)
	sha = strings.TrimPrefix(sha, "sha256:")
	sha = strings.ToLower(sha)
	if len(sha) != 64 {
		return false
	}
	for _, c := range sha {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) == sha
}

// checkPushFiles refuses a push whose agents layer carries machine-specific
// content (config.CheckAgents owns the rules). The bytes are checked, never
// altered: what a push sends is exactly what is on disk, so the file itself must
// be clean, and the refusal points at agents.local.toml (sty_89b9df20). verb names
// the command in the refusal.
func checkPushFiles(verb string, files []config.ConfigFile) error {
	for _, f := range files {
		if f.Area != "agents" {
			continue
		}
		refusals, err := config.CheckAgentsTransport(f.Content)
		if err != nil {
			return fmt.Errorf("%s refused: %s: %w", verb, f.Path, err)
		}
		if err := config.RefusalsError(verb, refusals); err != nil {
			return err
		}
	}
	return nil
}

// printWithheldBinding names each satelle.toml key a push leaves out because it
// binds this checkout to a hosted project — the one thing a push still withholds.
func printWithheldBinding(out io.Writer, files []config.ConfigFile) {
	for _, f := range files {
		for _, key := range f.Withheld {
			fmt.Fprintf(out, "settings: withheld %s from %s (this machine's project binding)\n", key, config.ConfigName)
		}
	}
}

// ingestDeployedAgents drops the workspace layer and the legacy bare agents.toml
// (config.LegacyAgentsKey) from a deploy set if an old manifest carries them, and
// refuses an agents layer that carries machine-specific content (ingestAgents).
func ingestDeployedAgents(out io.Writer, files []subsync.File) ([]subsync.File, error) {
	kept := files[:0]
	for _, f := range files {
		if config.LegacyAgentsKey(f.Path) {
			fmt.Fprintf(out, "agents: %s is a legacy hosted key — the agents layer deploys only from %s (removing it from the hosted copy is sty_0214369e).\n", f.Path, config.AgentsRel)
			continue
		}
		switch f.Path {
		case config.WorkspaceAgentsRel:
			fmt.Fprintf(out, "agents: %s is the synced workspace layer — not deployed from the personal store (use `satelle sync bindings pull`).\n", f.Path)
			continue
		case config.AgentsRel:
			if err := ingestAgents("sync config deploy", f.Content); err != nil {
				return nil, err
			}
		}
		kept = append(kept, f)
	}
	return kept, nil
}

// ingestAgents is the ONE ingest check for a hosted agents layer about to be
// written over the authored .satelle/workflows/agents.toml (`sync config deploy`,
// `publish adopt`, `publish check --update`, `sync bindings pull`). The layer is
// written as-is — the file carries what the operator pushed — so a body that
// carries machine-specific content (one an older push left in the store) is
// refused with the pointer to agents.local.toml instead of being altered or
// landed as live bindings. agents.local.toml itself is never touched.
func ingestAgents(verb string, content []byte) error {
	refusals, err := config.CheckAgentsTransport(content)
	if err != nil {
		return fmt.Errorf("%s refused: the hosted %s: %w", verb, config.AgentsRel, err)
	}
	return config.RefusalsError(verb+" (hosted copy)", refusals)
}

func runSyncConfigDeploy(cmd *cobra.Command, serverArg, workspaceArg string, version int) error {
	_, err := runSyncConfigDeployOutcome(cmd, serverArg, workspaceArg, version)
	return err
}

func runSyncConfigDeployOutcome(cmd *cobra.Command, serverArg, workspaceArg string, version int) (deployOutcome, error) {
	return runSyncConfigDeployWith(cmd, serverArg, workspaceArg, version, nil)
}

// runSyncConfigDeployWith is the deploy. A reconcile (rc non-nil, sty_b0873ec3)
// runs it for the areas it names, over their snapshot plans alone, so a file the
// plan leaves be is not rewritten; the per-file handling — the agents ingest
// check, the settings binding — is the deploy's own.
func runSyncConfigDeployWith(cmd *cobra.Command, serverArg, workspaceArg string, version int, rc *reconcileApply) (deployOutcome, error) {
	cfg, repoRoot, dataDir, err := loadRepoConfig()
	if err != nil {
		return deployOutcome{}, err
	}
	server := resolveServer(serverArg)
	if server == "" {
		return deployOutcome{}, fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}
	// Project-addressed config routes (sty_ca64d0cb). --workspace is accepted
	// but inert for routing (workspace is derived from the project server-side);
	// keep it in user-facing messages for the deprecation window.
	project, err := resolveBoundProject(cfg, repoRoot)
	if err != nil {
		return deployOutcome{}, err
	}
	sourceName := resolveDeploySourceName(cfg, workspaceArg)
	client := newHostedClient(cmd.Context(), server, repoRoot)
	manifest, err := client.ConfigManifest(cmd.Context(), project)
	if err != nil {
		if errors.Is(err, hosted.ErrLoginRequired) {
			return deployOutcome{}, err
		}
		return deployOutcome{}, fmt.Errorf("fetch manifest: %w", err)
	}
	out := cmd.OutOrStdout()
	// A deploy also refreshes the project tier the team-owned substrate resolves
	// from; a reconcile is scoped to its chosen areas and leaves it alone.
	writeTier := func(snaps *configDeploySnapshots) error {
		if rc != nil {
			return nil
		}
		return materialiseProjectTier(cmd, cfg, repoRoot, dataDir, client, server, manifest, version, snaps)
	}
	if len(manifest) == 0 {
		fmt.Fprintf(out, "No config in workspace %q project %q on %s.\n", sourceName, project, server)
		return deployOutcome{}, writeTier(nil)
	}
	// Areas the hosted copy holds a snapshot for deploy what the snapshot names —
	// a file deleted or renamed elsewhere stays gone — and a file changed on both
	// sides is parked for merging instead of overwritten (sty_fe5a8ed4). A pinned
	// --version is a deliberate historical read of every head, so it bypasses
	// snapshots. The records themselves are never deployed.
	var snaps *configDeploySnapshots
	if version == 0 {
		snaps, err = planConfigDeploySnapshots(cmd, cfg, repoRoot, dataDir, client, server, project, manifest, rc)
		if err != nil {
			return deployOutcome{}, err
		}
	}
	var files []subsync.File
	var missing int
	var settingsInManifest bool
	if snaps != nil {
		staged, serr := snaps.stage(cmd.Context())
		if serr != nil {
			return deployOutcome{}, serr
		}
		files = append(files, staged...)
	}
	for _, f := range files {
		if f.Path == config.ConfigName {
			settingsInManifest = true
		}
	}
	for _, item := range manifest {
		if _, isRecord := snapsync.AreaOfRecordPath(item.Path); isRecord || snaps.owns(cfg, repoRoot, item.Path) || rc != nil {
			continue
		}
		if item.Path == config.ConfigName {
			settingsInManifest = true
		}
		content, _, ferr := client.ConfigFileContent(cmd.Context(), project, item.Path, version)
		if ferr != nil {
			if errors.Is(ferr, hosted.ErrConfigFileMissing) && version > 0 {
				missing++ // a file this version predates — skip it
				continue
			}
			if errors.Is(ferr, hosted.ErrLoginRequired) {
				return deployOutcome{}, ferr
			}
			return deployOutcome{}, fmt.Errorf("fetch %s: %w", item.Path, ferr)
		}
		if config.UneditedDefault(item.Path, content) {
			fmt.Fprintf(out, "  kept binary default (hosted copy is an unedited embedded default): %s\n", item.Path)
			continue
		}
		files = append(files, subsync.File{Path: item.Path, Content: content})
	}
	if len(files) == 0 && (snaps == nil || len(snaps.jobs) == 0) {
		if rc != nil {
			if !rc.fastForward { // a pull reports each area itself
				fmt.Fprintln(out, "Nothing to reconcile — the hosted copy holds no snapshot for the chosen config areas.")
			}
			return deployOutcome{}, nil
		}
		fmt.Fprintf(out, "Nothing to deploy — version %d matched no files in workspace %q.\n", version, sourceName)
		return deployOutcome{}, writeTier(snaps)
	}
	// The agents layer is written as hosted — a push sends the file's bytes — so
	// one that carries machine-specific content is refused, never altered. The
	// synced workspace layer is never deployed from the personal store.
	files, err = ingestDeployedAgents(out, files)
	if err != nil {
		return deployOutcome{}, err
	}
	// Capture the local binding before restore; re-apply after so a hosted
	// satelle.toml (even an older one that carries it) cannot rebind this repo.
	boundProject := project
	cfgPath := filepath.Join(dataDir, config.ConfigName)
	res, err := subsync.Restore(dataDir, files)
	if err != nil {
		return deployOutcome{}, fmt.Errorf("deploy: %w", err)
	}
	// A deploy materialises a whole partition deliberately — a file it could not
	// write is a FAILED deploy, not a partial one. Restore stopped hard-erroring
	// on an unwritable file so a cursor-driven pull can continue past it
	// (sty_4c3729e7); this caller keeps the loud behaviour it always had.
	if err := res.Err(); err != nil {
		return deployOutcome{}, fmt.Errorf("deploy: %w", err)
	}
	if snaps != nil {
		if err := snaps.commit(res); err != nil {
			return deployOutcome{}, fmt.Errorf("deploy: record synced snapshot: %w", err)
		}
		snaps.recordStates(cmd.Context(), cfg, repoRoot)
	}
	if err := writeTier(snaps); err != nil {
		return deployOutcome{}, fmt.Errorf("deploy: %w", err)
	}
	outcome := deployOutcome{
		Written:      res.Written,
		Missing:      missing,
		SkippedLocal: len(res.Skipped),
	}
	if settingsInManifest {
		// Did Restore actually write satelle.toml (not skip it as local-only)?
		settingsWritten := true
		for _, sk := range res.Skipped {
			if sk == config.ConfigName {
				settingsWritten = false
				break
			}
		}
		if settingsWritten {
			outcome.SettingsRestored = true
			removed, err := stripRestoredSyncScope(cfgPath)
			if err != nil {
				return outcome, fmt.Errorf("deploy: drop [sync] scope keys: %w", err)
			}
			if err := config.SaveConfigValues(cfgPath, []config.KeyEdit{
				config.BoundProjectEdit(boundProject),
			}); err != nil {
				return outcome, fmt.Errorf("deploy: preserve [sync] project: %w", err)
			}
			note := ""
			if len(removed) > 0 {
				note = fmt.Sprintf("; removed [sync] scope keys %s (scope comes from the account)", strings.Join(removed, ", "))
			}
			fmt.Fprintf(out, "settings: satelle.toml restored; [sync] project preserved as %q (not taken from the deployed file)%s.\n", boundProject, note)
		}
	}
	pinned := "latest"
	if version > 0 {
		pinned = fmt.Sprintf("version %d", version)
	}
	tail := ""
	if missing > 0 {
		tail = fmt.Sprintf(", %d skipped (not present at that version)", missing)
	}
	if n := len(res.Skipped); n > 0 {
		tail += fmt.Sprintf(", %d skipped (local-only path)", n)
	}
	if rc != nil {
		verb := "Reconciled"
		if rc.fastForward {
			verb = "Pulled"
		}
		fmt.Fprintf(out, "%s %d file(s) from project %q on %s into %s.\n", verb, res.Written, project, server, dataDir)
		return outcome, nil
	}
	fmt.Fprintf(out, "Deployed %d file(s) (%s%s) from %s workspace %q into %s.\n", res.Written, pinned, tail, server, sourceName, dataDir)
	return outcome, nil
}

// stripRestoredSyncScope drops the removed [sync] scope keys an older hosted
// satelle.toml carries from the file a deploy just wrote, so it still loads and
// another machine's scope never lands here. It names the keys it removed.
func stripRestoredSyncScope(cfgPath string) ([]string, error) {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	next, removed := config.RemoveSyncScopeKeys(string(b))
	if len(removed) == 0 {
		return nil, nil
	}
	return removed, os.WriteFile(cfgPath, []byte(next), 0o644)
}

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
	scope := res.Scope
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	// What this checkout last synced for each area (sty_fe5a8ed4): shown only
	// once an area has a recorded base, so an area never synced prints as before.
	lastSynced, unmerged := lastSyncedNotes(a.Config, a.RepoRoot)
	dataDir := a.DataDir
	if dataDir == "" {
		dataDir = a.Config.ResolveDataDir(a.RepoRoot)
	}
	ign, err := config.LoadIgnore(dataDir)
	if err != nil {
		return err
	}
	for _, area := range config.SyncAreas {
		if note := lastSynced[area]; note != "" && scope != config.LocalScope {
			fmt.Fprintf(w, "%s\t%s\t%s\n", area, scope, note)
		} else {
			fmt.Fprintf(w, "%s\t%s\n", area, scope)
		}
		for _, p := range unmerged[area] {
			fmt.Fprintf(w, "\tunmerged: %s\n", p)
		}
		// Local areas push nothing — nothing to report. Non-local areas can leave
		// the machine, so report both shared: promotions and withheld .local files.
		if scope == config.LocalScope {
			continue
		}
		path, isDir := syncAreaPath(a, area)
		if path == "" {
			continue
		}
		shared, withheld, err := areaFileNotes(dataDir, path, isDir, scope, ign)
		if err != nil {
			return fmt.Errorf("sync area %q: %w", area, err)
		}
		for _, f := range shared {
			fmt.Fprintf(w, "\tshared: %s\n", f)
		}
		for _, f := range withheld {
			fmt.Fprintf(w, "\twithheld: %s\n", f)
		}
	}
	return w.Flush()
}

// syncAreaPath resolves the on-disk location a sync area's files live under.
// isDir reports whether path is a directory to scan (vs. a single file).
// "ledger" is DB-only and "executions" nest under their parent task's
// directory — neither has an area-level location of its own, so both resolve
// to "" (no file scan, scope still printed).
func syncAreaPath(a *app.App, area string) (path string, isDir bool) {
	// Authored areas resolve under DataDir; stories (attachment cache) under RuntimeDir
	// (sty_4660bbe1). Never derive either from DBPath — the planes diverge after migrate.
	dataDir := a.DataDir
	if dataDir == "" {
		dataDir = a.Config.ResolveDataDir(a.RepoRoot)
	}
	runtimeDir := a.RuntimeDir
	if runtimeDir == "" {
		runtimeDir = a.Config.ResolveRuntimeDir(a.RepoRoot).Dir
	}
	switch area {
	case "constitution":
		return a.Config.ResolveConstitution(a.RepoRoot), false
	case "agents":
		path, _ := config.AgentsPath(dataDir)
		return path, false
	case "tasks":
		return filepath.Join(dataDir, "tasks"), true
	case "settings":
		return filepath.Join(dataDir, config.ConfigName), false
	case "stories":
		return filepath.Join(runtimeDir, "stories"), true
	case "ledger", "executions":
		return "", false
	default:
		if dir := a.Config.ResolveAuthoredDirs(a.RepoRoot)[area]; dir != "" {
			return dir, true
		}
		return "", false
	}
}

// ignoredUnder reports whether the file at absolute-or-repo path p, taken
// relative to dataDir, is one ign keeps out of the sync. A file outside dataDir
// has no server key, so it is never ignored.
func ignoredUnder(dataDir, p string, ign config.Ignore) bool {
	rel, err := filepath.Rel(dataDir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return ign.Match(filepath.ToSlash(rel))
}

// areaFileNotes walks an area's on-disk location and returns two lists:
//   - shared: markdown files with frontmatter shared:true (personal-scope only;
//     same filter as the historical sharedFilesIn)
//   - withheld: any file whose basename/path carries a .local segment, every
//     extension, or that .satelleignore names, so operators see what the
//     bundler will keep back
//
// A non-existent path is benign (empty results, not an error).
func areaFileNotes(dataDir, path string, isDir bool, scope config.Scope, ign config.Ignore) (shared, withheld []string, err error) {
	noteOne := func(p string) error {
		base := filepath.Base(p)
		// Basename is enough: LocalOnlyPath matches on any segment's .local
		// component (directory segments like secrets.local/ are covered when
		// walking files under them, since each file path is checked fully).
		// A .satelleignore match is judged on the data-dir-relative path, the
		// same key the bundler withholds it under.
		if config.LocalOnlyPath(filepath.ToSlash(p)) || config.LocalOnlyPath(base) || ignoredUnder(dataDir, p, ign) {
			withheld = append(withheld, p)
			return nil // withheld files are not also listed as shared
		}
		if scope != config.PersonalScope {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		name := strings.TrimSuffix(base, filepath.Ext(p))
		if name == "index" || name == "log" || strings.EqualFold(name, "README") {
			return nil
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if config.FileShared(config.PersonalScope, string(body)) {
			shared = append(shared, p)
		}
		return nil
	}
	if !isDir {
		if _, serr := os.Stat(path); os.IsNotExist(serr) {
			return nil, nil, nil
		}
		if err := noteOne(path); err != nil {
			return nil, nil, err
		}
		return shared, withheld, nil
	}
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			if os.IsNotExist(werr) && p == path {
				return nil
			}
			return werr
		}
		if d.IsDir() {
			// Descend into every directory; LocalOnlyPath matches each file.
			return nil
		}
		return noteOne(p)
	})
	if err != nil {
		return nil, nil, err
	}
	return shared, withheld, nil
}
