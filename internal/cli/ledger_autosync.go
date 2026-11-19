package cli

import (
	"context"
	"fmt"
	"io"
	"os"
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

const autosyncRepoTimeout = 2 * time.Minute

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
printed and does not fail the pass. Before any project is visited the pass
runs doctor's projects check and its fix, as satelle doctor --resolve does: a
project whose path no longer exists is removed from this machine's projects.
Each partition of this machine's satelled whose recorded project path no
longer exists is removed; a satelled that is not answering removes no
partition. Exits 1 when any project's pass
failed; a confirm or a prune that fails does not fail the pass.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gc, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			gc = runProjectsFix(cmd.Context(), gc, out)
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

func runProjectsFix(ctx context.Context, gc config.GlobalConfig, out io.Writer) config.GlobalConfig {
	issues, notes := checkProjects(ctx, nil)
	for _, n := range notes {
		fmt.Fprintf(out, "projects not checked: %s\n", n)
	}
	if len(issues) == 0 {
		return gc
	}
	var rep resolveReport
	resolveProjects(&rep, issues)
	for _, a := range rep.Actions {
		if a.Result == resultFailed {
			fmt.Fprintf(out, "%s: not fixed: %s\n", a.Problem, a.Detail)
			continue
		}
		fmt.Fprintln(out, a.Detail)
	}
	if fresh, err := config.LoadGlobal(); err == nil {
		return fresh
	}
	return gc
}

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
	pass, summary := syncPass(ctx, r)
	if pass.Reason != "" {
		fmt.Fprintf(out, "%s: sync failing: %s\n", path, pass.Reason)
		return false
	}
	fmt.Fprintf(out, "%s: %s\n", path, summary)
	return true
}

func syncPass(ctx context.Context, r repo) (syncstate.Pass, string) {
	ctx, cancel := context.WithTimeout(ctx, autosyncRepoTimeout)
	defer cancel()
	pass, summary := syncRepo(ctx, r)
	_ = syncstate.RecordPass(config.GlobalDir(), uiRepoRoot(r), pass, time.Now())
	confirmUI(ctx, r)
	return pass, summary
}

func syncRepo(ctx context.Context, r repo) (syncstate.Pass, string) {
	const op = "ledger autosync"
	k, err := r.open()
	if err != nil {
		return syncstate.Pass{Reason: err.Error()}, ""
	}
	defer k.Close()
	spec, defaulted := resolveStore(k)
	if spec.Kind == ledgerstore.KindSatelleDev && !spec.External() && defaulted {
		if _, ok, err := loadBinding(r); err == nil && !ok {
			return syncstate.Pass{Store: syncstate.LocalStore}, "ledger store is local: nothing replicates"
		}
	}
	s, closeFn, err := openStore(k, r, "autosync")
	defer closeFn()
	if err != nil {
		return syncstate.Pass{Store: spec.Kind, Reason: hosted.Describe(op, err).Error()}, ""
	}
	if s == nil {
		return syncstate.Pass{Store: syncstate.LocalStore}, "ledger store is local: nothing replicates"
	}
	res, err := newReplicator(k, s, io.Discard).Sync(ctx)
	if err != nil {
		return syncstate.Pass{Store: s.Describe(), Reason: hosted.Describe(op, err).Error()}, ""
	}
	summary := fmt.Sprintf("synced with %s: pushed %d event(s) on %d item(s), pulled %d event(s) on %d item(s)",
		s.Describe(), res.Push.Events, res.Push.Items, res.Pull.Events, res.Pull.Items)
	forked := make([]string, 0, len(res.Unsynced))
	for _, u := range res.Unsynced {
		forked = append(forked, u.Item+" ("+u.State+")")
	}
	return syncstate.Pass{Store: s.Describe(), Reason: syncReason(res), Pushed: res.Push.Items, Pulled: res.Pull.Items, Forked: forked}, summary
}

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
