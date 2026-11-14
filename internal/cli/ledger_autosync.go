package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

// autosyncRepoTimeout bounds one project's pass, so one unreachable store
// cannot hold the others past the sync service's own timeout.
const autosyncRepoTimeout = 2 * time.Minute

// ledgerAutosyncCmd is the automatic sync pass satelle-sync.timer runs
// (sto_5c706973): an internal verb, hidden from help.
func ledgerAutosyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "autosync",
		Short:  "Run one automatic sync pass over this machine's projects (run by satelle-sync.timer)",
		Hidden: true,
		Long: `For each of this machine's projects (~/.satelle/config.toml [projects]) whose
[ledger] store is not local, push the items this runtime is ahead on, then
pull the items the store is ahead on, by the rules of satelle ledger push and
pull. An item in sync is not touched; one that diverged or was refused is
left on both sides and named. Each project's outcome is recorded for
satelled's project page, and this machine's satelled's mirror of every
project visited, local store or not, is confirmed: marked fresh when
unchanged, otherwise sent a full snapshot. A satelled that is not answering
confirms nothing. A pre-1.0 project is skipped with one "needs upgrade" line
naming satelle upgrade --archive, is recorded so satelled shows it, and does
not fail the pass. Each other project's untracked harness hook files in use
are refreshed first, as satelle agents refresh does; a refresh failure is
printed and does not fail the pass. A project whose path no longer exists is
removed from this machine's projects, and each partition of this machine's
satelled whose recorded project path no longer exists is removed; a satelled
that is not answering removes no partition. Exits 1 when any project's pass
failed; a confirm or a prune that fails does not fail the pass.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gc, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			gc = pruneGoneProjects(gc, out)
			stale := staleHook(substrateTree(NewRootCmd()))
			var failed []string
			for _, path := range gc.Projects.Paths {
				if !autosyncProject(cmd.Context(), out, path, stale) {
					failed = append(failed, path)
				}
			}
			pruneGonePartitions(cmd.Context(), out)
			if len(failed) > 0 {
				return &exitError{code: ExitError, msg: fmt.Sprintf("ledger autosync: %d of %d project(s) failed: %s",
					len(failed), len(gc.Projects.Paths), strings.Join(failed, ", "))}
			}
			return nil
		},
	}
}

// projectPathGone reports whether path is an absolute path that no longer
// exists: the only state that lets the unattended pass remove a project
// (sto_e3c6bfb6). An empty or relative path, a path that exists (directory or
// file) and any other stat error (permission, I/O) are not gone.
func projectPathGone(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	_, err := os.Stat(path)
	return err != nil && errors.Is(err, fs.ErrNotExist)
}

// pruneGoneProjects removes from this machine's projects (~/.satelle/config.toml
// [projects] paths) each entry whose path is gone, saving the config once. With
// nothing gone it returns gc unchanged and writes nothing; on a save error it
// reports it and returns gc as loaded.
func pruneGoneProjects(gc config.GlobalConfig, out io.Writer) config.GlobalConfig {
	var gone []string
	for _, p := range gc.Projects.Paths {
		if projectPathGone(p) {
			gone = append(gone, p)
		}
	}
	if len(gone) == 0 {
		return gc
	}
	pruned := gc
	pruned.Projects.Paths = slices.Clone(gc.Projects.Paths)
	for _, p := range gone {
		pruned.Projects.Remove(p)
	}
	if err := config.SaveGlobal(pruned); err != nil {
		fmt.Fprintf(out, "projects not pruned: %v\n", err)
		return gc
	}
	for _, p := range gone {
		fmt.Fprintf(out, "%s: removed from this machine's projects: path no longer exists\n", p)
	}
	return pruned
}

// pruneGonePartitions removes, through this machine's satelled only, each
// mirror partition whose recorded project path is gone. It removes by the
// stored repo_key, never falls back to the database directly (a satelled that
// is not answering changes no partition) and never fails the pass.
func pruneGonePartitions(ctx context.Context, out io.Writer) {
	c, ok := uiClient()
	if !ok {
		return
	}
	details, err := homePartitions(ctx)
	if err != nil {
		fmt.Fprintf(out, "partitions not pruned: %v\n", err)
		return
	}
	for _, d := range details {
		if !projectPathGone(d.Path) {
			continue
		}
		if err := c.Remove(ctx, d.RepoKey); err != nil {
			fmt.Fprintf(out, "partitions not pruned: %v\n", err)
			return
		}
		fmt.Fprintf(out, "%s: partition %s pruned: project path no longer exists\n", d.Path, d.RepoKey)
	}
}

// autosyncProject runs one project's pass, records its outcome and confirms
// satelled's mirror of the project (sto_128803b3). A path that is gone or holds no satelle repo is
// skipped with nothing recorded. First it refreshes the project's untracked
// harness files in use by stale, as satelle agents refresh does; a hook
// failure is printed, but is not a sync outcome and does not fail the pass.
// It reports whether the pass succeeded.
func autosyncProject(ctx context.Context, out io.Writer, path string, stale func(string) bool) bool {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		fmt.Fprintf(out, "%s: skipped: no such directory\n", path)
		return true
	}
	r, err := findRepo(path)
	if err != nil {
		fmt.Fprintf(out, "%s: skipped: %v\n", path, err)
		return true
	}
	// A pre-1.0 repo cannot be read by this satelle: it needs upgrading in
	// the repo, which is not a failed pass (sto_0f76dd4e). Nothing is opened,
	// so nothing is written into its runtime dir, and its harness files are
	// left for satelle upgrade --archive.
	if m := kernel.LegacyMarkers(r.DataDir); len(m) > 0 {
		fmt.Fprintln(out, needsUpgradeLine(path, m))
		_ = syncstate.RecordPass(config.GlobalDir(), uiRepoRoot(r), syncstate.Pass{Upgrade: kernel.UpgradeCommand}, time.Now())
		return true
	}
	hooks := refreshProject(r, stale)
	for _, f := range hooks.Changed {
		fmt.Fprintf(out, "%s: refreshed %s hooks\n", f.Path, f.Harness)
	}
	for _, f := range hooks.Failures {
		fmt.Fprintf(out, "%s: harness file not refreshed: %s\n", f.Path, f.Err)
	}
	ctx, cancel := context.WithTimeout(ctx, autosyncRepoTimeout)
	defer cancel()
	pass, summary := syncRepo(ctx, r)
	_ = syncstate.RecordPass(config.GlobalDir(), uiRepoRoot(r), pass, time.Now())
	reason := pass.Reason
	// The kernel is closed: confirm this repo's mirror before the next repo
	// opens, whatever the store outcome — a failed remote sync does not stop
	// the local mirror being confirmed against the local ledger. Its full
	// body, when one is needed, carries the pull's writes.
	confirmUI(ctx, r)
	if reason != "" {
		fmt.Fprintf(out, "%s: sync failing: %s\n", path, reason)
		return false
	}
	fmt.Fprintf(out, "%s: %s\n", path, summary)
	return true
}

// syncRepo opens r's kernel and runs one Sync pass with its store. It
// returns the pass (the store's description, syncstate.LocalStore when
// nothing replicates; why it failed, "" for a success; and its counts) and a
// one-line summary.
func syncRepo(ctx context.Context, r repo) (syncstate.Pass, string) {
	k, err := r.open()
	if err != nil {
		return syncstate.Pass{Reason: err.Error()}, ""
	}
	defer k.Close()
	spec, defaulted := resolveStore(k)
	if spec.Kind == ledgerstore.KindSatelleDev && !spec.External() && defaulted {
		// satelle.dev only because this machine is signed in: an unbound
		// repo has nothing to replicate with, as for the local store.
		if _, ok, err := loadBinding(r); err == nil && !ok {
			return syncstate.Pass{Store: syncstate.LocalStore}, "ledger store is local: nothing replicates"
		}
	}
	s, closeFn, err := openStore(k, r, "autosync")
	defer closeFn()
	if err != nil {
		return syncstate.Pass{Store: spec.Kind, Reason: hosted.Describe("ledger autosync", err).Error()}, ""
	}
	if s == nil {
		return syncstate.Pass{Store: syncstate.LocalStore}, "ledger store is local: nothing replicates"
	}
	res, err := newReplicator(k, s, io.Discard).Sync(ctx)
	if err != nil {
		return syncstate.Pass{Store: s.Describe(), Reason: hosted.Describe("ledger autosync", err).Error()}, ""
	}
	summary := fmt.Sprintf("synced with %s: pushed %d event(s) on %d item(s), pulled %d event(s) on %d item(s)",
		s.Describe(), res.Push.Events, res.Push.Items, res.Pull.Events, res.Pull.Items)
	forked := make([]string, 0, len(res.Unsynced))
	for _, u := range res.Unsynced {
		forked = append(forked, u.Item+" ("+u.State+")")
	}
	return syncstate.Pass{Store: s.Describe(), Reason: syncReason(res), Pushed: res.Push.Items, Pulled: res.Pull.Items, Forked: forked}, summary
}

// syncReason is why a Sync pass failed, "" when it did not: push's and
// pull's diverged and refused items, then the items the pass left unsynced.
func syncReason(res ledgerrepl.SyncResult) string {
	var parts []string
	for _, p := range []string{replicationReason("push", res.Push), replicationReason("pull", res.Pull)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(res.Unsynced) > 0 {
		lines := make([]string, 0, len(res.Unsynced))
		for _, u := range res.Unsynced {
			lines = append(lines, u.Item+" ("+u.State+")")
		}
		parts = append(parts, fmt.Sprintf("%d item(s) not synced: %s", len(res.Unsynced), strings.Join(lines, ", ")))
	}
	return strings.Join(parts, "; ")
}
