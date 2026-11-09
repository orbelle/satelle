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
satelled's project page, and the items a pull wrote are pushed to this
machine's satelled. Exits 1 when any project's pass failed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gc, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var failed []string
			for _, path := range gc.Projects.Paths {
				if !autosyncProject(cmd.Context(), out, path) {
					failed = append(failed, path)
				}
			}
			if len(failed) > 0 {
				return &exitError{code: ExitError, msg: fmt.Sprintf("ledger autosync: %d of %d project(s) failed: %s",
					len(failed), len(gc.Projects.Paths), strings.Join(failed, ", "))}
			}
			return nil
		},
	}
}

// autosyncProject runs one project's pass, records its outcome and pushes
// what it wrote to satelled. A path that is gone or holds no satelle repo is
// skipped with nothing recorded. It reports whether the pass succeeded.
func autosyncProject(ctx context.Context, out io.Writer, path string) bool {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		fmt.Fprintf(out, "%s: skipped: no such directory\n", path)
		return true
	}
	r, err := findRepo(path)
	if err != nil {
		fmt.Fprintf(out, "%s: skipped: %v\n", path, err)
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, autosyncRepoTimeout)
	defer cancel()
	store, summary, reason := syncRepo(ctx, r)
	_ = syncstate.RecordSync(config.GlobalDir(), uiRepoRoot(r), store, reason, time.Now())
	// The kernel is closed: push this repo's writes before the next repo
	// opens, since written holds one repo at a time.
	flushUI(ctx)
	if reason != "" {
		fmt.Fprintf(out, "%s: sync failing: %s\n", path, reason)
		return false
	}
	fmt.Fprintf(out, "%s: %s\n", path, summary)
	return true
}

// syncRepo opens r's kernel and runs one Sync pass with its store. It
// returns the store's description (syncstate.LocalStore when nothing
// replicates), a one-line summary, and why the pass failed ("" for a
// success).
func syncRepo(ctx context.Context, r repo) (store, summary, reason string) {
	k, err := r.open()
	if err != nil {
		return "", "", err.Error()
	}
	defer k.Close()
	spec, defaulted := resolveStore(k)
	if spec.Kind == ledgerstore.KindSatelleDev && !spec.External() && defaulted {
		// satelle.dev only because this machine is signed in: an unbound
		// repo has nothing to replicate with, as for the local store.
		if _, ok, err := loadBinding(r); err == nil && !ok {
			return syncstate.LocalStore, "ledger store is local: nothing replicates", ""
		}
	}
	s, closeFn, err := openStore(k, r, "autosync")
	defer closeFn()
	if err != nil {
		return spec.Kind, "", hosted.Describe("ledger autosync", err).Error()
	}
	if s == nil {
		return syncstate.LocalStore, "ledger store is local: nothing replicates", ""
	}
	res, err := newReplicator(k, s, io.Discard).Sync(ctx)
	if err != nil {
		return s.Describe(), "", hosted.Describe("ledger autosync", err).Error()
	}
	summary = fmt.Sprintf("synced with %s: pushed %d event(s) on %d item(s), pulled %d event(s) on %d item(s)",
		s.Describe(), res.Push.Events, res.Push.Items, res.Pull.Events, res.Pull.Items)
	return s.Describe(), summary, syncReason(res)
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
