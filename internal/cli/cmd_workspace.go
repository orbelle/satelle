// `satelle workspace` manages the connected-repo registry the /workspace view
// aggregates (build order step 6). The registry lives in the global config
// (~/.satelle/config.toml [workspace] repos); per-repo databases stay the source
// of truth. `workspace add` also seeds the push-fed serve mirror against the
// machine [service] endpoint (sty_805bee9c / sty_0122610a / sty_21a7d16d).
//
// Mirror hygiene (epic:mirror-hygiene / sty_5aa08259 / sty_eb61be02):
//   - SATELLE_SERVER_ENDPOINT=none disables discovery and push (hermetic tests).
//   - Auto-bootstrap only adopts a serve whose X-Satelle-Instance matches this
//     SATELLE_HOME — foreign :8787 answers are not seeded.
//   - workspace remove purges the mirror partition; partitions / prune manage
//     orphans (test pollution).
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func init() {
	ws := &cobra.Command{
		Use:   "workspace",
		Short: "Manage the connected-repo registry the workspace view aggregates",
		Long: `Manage the registry of repos this machine's workspace view aggregates.

Local and machine-wide: registering a repo lets one serve show every project's
stories together. It is not a hosted concept and copies no work — a repo that is
not registered is simply absent from the view, not ungoverned.`,
	}

	add := &cobra.Command{
		Use:   "add [path]",
		Short: "Join the workspace: register and seed the serve mirror",
		Long: `workspace add registers a repo in the machine-local workspace registry
(~/.satelle/config.toml [workspace] repos) and seeds the push-fed serve mirror
so the project appears on the landing.

The seed target is the machine [service] endpoint in ~/.satelle/config.toml
(env SATELLE_SERVER_ENDPOINT > [service] endpoint > http://127.0.0.1:<port>).
A live serve must answer that URL with a matching X-Satelle-Instance for this
SATELLE_HOME. SATELLE_SERVER_ENDPOINT=none|off|- disables discovery and push
(used by hermetic tests).

If no matching serve answers, the verb still registers the path, prints that
seed was skipped, and exits 0 — re-run after starting serve, or set
` + "`satelle settings --global endpoint`" + `.

The landing URL slug is the repo directory's basename (e.g. /r/my-app/ for a
repo at …/my-app). Seeding a second repo whose basename already belongs to
another partition is rejected (HTTP 409) with a message naming the conflict —
rename the directory so basenames are unique and re-run. There is no
slug-override flag today.

This is the single join verb for the local UI (epic:serve-adoption). Later
mutations are pushed automatically by the CLI change publisher — re-run
workspace add only to re-seed the mirror manually.

It is also the MANUAL RECOVERY for a mirror showing a stale frame: a push that
never landed (the service was restarting, down, or unreachable) is repaired by
re-running this verb from the repo. You rarely need to: a running serve already
re-requests a snapshot for every partition it renders at startup and on an
interval, and a view it could not reconcile says so with its last-ingest time
rather than presenting itself as current.

Path defaults to the current directory. When the path is a different repo than
the one opened by the CLI, only registration runs — seed from inside that repo.`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: needsStore(),
		RunE:        runWorkspaceAdd,
	}

	remove := &cobra.Command{
		Use:   "remove [path]",
		Short: "Unregister a repo and purge its mirror partition (defaults to the current directory)",
		Long: `Unregister a repo from the workspace registry and purge its mirror partition,
defaulting to the current directory.

It removes the repo from the VIEW only: the repo keeps its own database, its
substrate and its stories, and satelle governs it exactly as before. Re-add it
with satelle workspace add.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runWorkspaceRemove,
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List registered repos",
		Long: `List the repos registered in this machine's workspace registry.

Registration is by PATH: a repo that moved is still listed under the old one and
will read as unhealthy until it is re-added there. Use satelle doctor --all to
see which registered repos are actually in good shape.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			gc, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			if len(gc.Workspace.Repos) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no repos registered — add one with `satelle workspace add`")
				return nil
			}
			for _, r := range gc.Workspace.Repos {
				fmt.Fprintln(cmd.OutOrStdout(), r)
			}
			return nil
		},
	}

	parts := &cobra.Command{
		Use:   "partitions",
		Short: "List mirror partitions (repo_key, slug, path, counts) on the local serve",
		Long: `List the mirror partitions the local serve holds — repo_key, slug, path and row
counts, one per registered repo.

This is the serve's own view, not the registry's: a partition with no matching
repo is an orphan (usually a test or a moved checkout), and satelle workspace
prune removes it by repo_key.`,
		Args: cobra.NoArgs,
		RunE: runWorkspacePartitions,
	}

	var pruneForce bool
	prune := &cobra.Command{
		Use:   "prune <repo_key>",
		Short: "Remove a mirror partition by repo_key (orphans from tests; --force if path still exists)",
		Long: `Remove one mirror partition from the local serve by repo_key.

For orphans — partitions whose repo is gone. If the path still exists it refuses
without --force, because pruning a live repo's partition drops it out of the
workspace view until it is re-seeded. The repo's own database is never touched.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspacePrune(cmd, args[0], pruneForce)
		},
	}
	prune.Flags().BoolVar(&pruneForce, "force", false, "allow prune when the partition's repo path still exists on disk")

	ws.AddCommand(add, remove, list, parts, prune)
	register(ws)
}

// runWorkspaceAdd registers the path then seeds the mirror when endpoint is set
// (or can be bootstrapped from a matching live local serve) and the path is the
// active repo (sty_805bee9c / sty_0122610a / sty_5aa08259).
func runWorkspaceAdd(cmd *cobra.Command, args []string) error {
	a, err := appFrom(cmd)
	if err != nil {
		return err
	}
	// Default path is the opened repo (config-resolved), not process cwd — so
	// SATELLE_CONFIG / sitting in a subdir still seeds the right store.
	var path string
	if len(args) == 1 {
		path, err = filepath.Abs(args[0])
		if err != nil {
			return err
		}
	} else {
		// Identity is the MAIN working tree, so a linked worktree registers the
		// repository it belongs to and is reported as already registered rather
		// than becoming a second project (sty_cd219594).
		path, err = filepath.Abs(config.CanonicalRepoRoot(a.RepoRoot))
		if err != nil {
			return err
		}
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	added := gc.Workspace.AddRepo(path)
	if added {
		if err := config.SaveGlobal(gc); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "added %s (%d repos)\n", path, len(gc.Workspace.Repos))
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "%s already registered\n", path)
	}

	// Snapshot only the active repo's store — other paths are register-only.
	activeRoot, _ := filepath.Abs(config.CanonicalRepoRoot(a.RepoRoot))
	if filepath.Clean(path) != filepath.Clean(activeRoot) {
		fmt.Fprintf(cmd.OutOrStdout(), "workspace add: registered %s; run `satelle workspace add` from inside that repo to seed its mirror\n", path)
		return nil
	}
	ep, skipReason, err := resolveSeedEndpoint(gc)
	if err != nil {
		return err
	}
	if ep == "" {
		fmt.Fprintln(cmd.OutOrStdout(), seedSkipNotice(skipReason))
		return nil
	}
	snap, err := buildUISnapshot(cmd.Context(), a)
	if err != nil {
		return err
	}
	if err := postUISnapshot(ep, snap); err != nil {
		return postTrimmedAfterTimeout(cmd.Context(), a, ep, snap, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "workspace add: ok repo_key=%s stories=%d tasks=%d → %s/ingest/snapshot\n",
		snap.RepoKey, len(snap.Stories), len(snap.Tasks), strings.TrimRight(ep, "/"))
	return nil
}

// postTrimmedAfterTimeout handles a failed full snapshot POST. When it timed out,
// the light drain snapshot (no doc bodies) is posted so the page still gets the
// current stories and seats; the full body's error is still returned, so the
// reconciler logs the failure and retries the full snapshot. Any other failure
// (a slug-conflict 409, a refused ingest) is returned as it is.
func postTrimmedAfterTimeout(ctx context.Context, a *app.App, ep string, full *mirror.Snapshot, fullErr error) error {
	var ne net.Error
	if !errors.Is(fullErr, context.DeadlineExceeded) && !(errors.As(fullErr, &ne) && ne.Timeout()) {
		return fullErr
	}
	light, err := buildUIDrainSnapshot(ctx, a)
	if err == nil {
		light.Origin = full.Origin
		err = postUISnapshot(ep, light)
	}
	if err != nil {
		return fmt.Errorf("full snapshot timed out; trimmed snapshot also failed (%v): %w", err, fullErr)
	}
	return fmt.Errorf("full snapshot timed out; posted trimmed snapshot: %w", fullErr)
}

// resolveSeedEndpoint returns the serve base URL to seed against.
// Design (sty_5aa08259 / sty_21a7d16d): SATELLE_SERVER_ENDPOINT=none disables;
// otherwise the machine [service] endpoint (explicit or derived) is used.
// A derived default still requires a matching live serve. Empty endpoint with
// skipReason is not an error — registration already succeeded (AC3: exit 0).
func resolveSeedEndpoint(gc config.GlobalConfig) (endpoint string, skipReason string, err error) {
	if ep, disabled, set := resolveServerEndpointEnv(); set {
		if disabled {
			return "", EnvServerEndpoint + "=none (discovery and push disabled)", nil
		}
		return ep, "", nil
	}
	if ep := strings.TrimSpace(gc.Service.Endpoint); ep != "" {
		return ep, "", nil
	}
	candidate := gc.Service.ResolveEndpoint()
	if candidate == "" {
		return "", "no service endpoint", nil
	}
	want := config.CurrentInstanceID()
	hz := strings.TrimRight(candidate, "/") + "/healthz"
	if !healthzInstanceOK(hz, want) {
		if !healthzOK(hz) {
			return "", fmt.Sprintf(
				"no serve answered %s — start serve then re-run, or set endpoint with `satelle settings --global endpoint`",
				hz), nil
		}
		return "", instanceMismatchReason(candidate, want), nil
	}
	return candidate, "", nil
}

// resolveRepoArg returns the absolute repo path from an optional arg, defaulting
// to the current directory.
func resolveRepoArg(args []string) (string, error) {
	p := "."
	if len(args) == 1 {
		p = args[0]
	}
	return filepath.Abs(p)
}

func runWorkspaceRemove(cmd *cobra.Command, args []string) error {
	path, err := resolveRepoArg(args)
	if err != nil {
		return err
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	repoKey := config.RepoKey(path)
	removed := gc.Workspace.RemoveRepo(path)
	if removed {
		if err := config.SaveGlobal(gc); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %s from registry (%d repos left)\n", path, len(gc.Workspace.Repos))
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "%s was not registered\n", path)
	}

	ep, epSrc := resolveOperatorEndpoint(cmd, path)
	if ep != "" {
		if err := postUIRemove(ep, repoKey); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "notice: registry updated but mirror partition purge failed (%v) — run `satelle workspace prune %s`\n",
				err, repoKey)
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "purged mirror partition repo_key=%s via %s/ingest/remove\n",
			repoKey, strings.TrimRight(ep, "/"))
		return nil
	}
	// No matching serve: purge local mirror store so the landing card disappears
	// even when the unit is pre-identity or down (sty_eb61be02).
	ms, err := mirror.Open(mirror.DefaultPath(config.GlobalDir()))
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "notice: registry updated; could not open local mirror (%v) — %s\n", err, epSrc)
		return nil
	}
	defer ms.Close()
	if err := ms.DeletePartition(cmd.Context(), repoKey); err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "notice: registry updated; local partition purge failed (%v)\n", err)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "purged mirror partition repo_key=%s from local store (no matching serve — %s)\n",
		repoKey, epSrc)
	return nil
}

func runWorkspacePartitions(cmd *cobra.Command, args []string) error {
	// List always reads the local mirror plane (same GlobalDir as serve when
	// co-located). No serve endpoint required — operators repair pollution
	// even when the unit is down or still on a pre-identity binary.
	ms, err := mirror.Open(mirror.DefaultPath(config.GlobalDir()))
	if err != nil {
		return fmt.Errorf("workspace partitions: open mirror: %w", err)
	}
	defer ms.Close()
	details, err := ms.ListPartitionDetails(cmd.Context())
	if err != nil {
		return err
	}
	if len(details) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no mirror partitions")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-20s %6s %5s %4s  %s\n", "REPO_KEY", "SLUG", "STORY", "TASK", "DOC", "PATH")
	for _, d := range details {
		path := d.Path
		if path == "" {
			path = "-"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-20s %6d %5d %4d  %s\n",
			d.RepoKey, d.Slug, d.Stories, d.Tasks, d.Docs, path)
	}
	return nil
}

func runWorkspacePrune(cmd *cobra.Command, repoKey string, force bool) error {
	repoKey = strings.TrimSpace(repoKey)
	if repoKey == "" {
		return fmt.Errorf("workspace prune: repo_key required")
	}
	ms, err := mirror.Open(mirror.DefaultPath(config.GlobalDir()))
	if err != nil {
		return fmt.Errorf("workspace prune: open mirror: %w", err)
	}
	defer ms.Close()
	details, err := ms.ListPartitionDetails(cmd.Context())
	if err != nil {
		return err
	}
	var found *mirror.PartitionDetail
	for i := range details {
		if details[i].RepoKey == repoKey {
			found = &details[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("workspace prune: unknown repo_key %q — run `satelle workspace partitions`", repoKey)
	}
	if path := strings.TrimSpace(found.Path); path != "" && !force {
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			return fmt.Errorf("workspace prune: path %s still exists — pass --force to remove the partition anyway, or use `satelle workspace remove` from that repo", path)
		}
	}
	ep, epSrc := resolveOperatorEndpoint(cmd, "")
	if ep != "" {
		// Prefer ingest path so a live serve's in-memory/WAL view updates and
		// SSE rings. Close our handle first to reduce lock contention.
		_ = ms.Close()
		if err := postUIRemove(ep, repoKey); err != nil {
			return fmt.Errorf("workspace prune via %s: %w (source %s)", ep, err, epSrc)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "pruned partition repo_key=%s via %s/ingest/remove\n",
			repoKey, strings.TrimRight(ep, "/"))
		return nil
	}
	// No matching serve (down, foreign instance, or pre-identity binary): delete
	// from the local mirror store directly. The live unit will see the purge on
	// next open/query of the shared DB file (WAL-friendly).
	if err := ms.DeletePartition(cmd.Context(), repoKey); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "pruned partition %s from local mirror store (no matching serve endpoint — %s)\n",
		repoKey, epSrc)
	return nil
}

// resolveOperatorEndpoint picks a serve base URL for remove/prune/list side
// effects. Prefer env, then opened app config when available, then default
// probe with instance match.
func resolveOperatorEndpoint(cmd *cobra.Command, hintPath string) (endpoint, source string) {
	if ep, disabled, set := resolveServerEndpointEnv(); set {
		if disabled {
			return "", EnvServerEndpoint + " disables push"
		}
		return ep, EnvServerEndpoint
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		return "", "no global config"
	}
	if ep := strings.TrimSpace(gc.Service.Endpoint); ep != "" {
		return ep, "[service] endpoint"
	}
	candidate := gc.Service.ResolveEndpoint()
	if candidate == "" {
		return "", "no service endpoint"
	}
	want := config.CurrentInstanceID()
	hz := strings.TrimRight(candidate, "/") + "/healthz"
	if healthzInstanceOK(hz, want) {
		return candidate, "live serve " + candidate
	}
	return "", "no matching serve at " + candidate
}

// postUIRemove POSTs /ingest/remove for repoKey.
func postUIRemove(endpoint, repoKey string) error {
	return postUIRemoveContext(context.Background(), endpoint, repoKey)
}

func postUIRemoveContext(ctx context.Context, endpoint, repoKey string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	body, err := json.Marshal(mirror.RemoveEvent{RepoKey: repoKey})
	if err != nil {
		return err
	}
	url := strings.TrimRight(strings.TrimSpace(endpoint), "/") + "/ingest/remove"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}
