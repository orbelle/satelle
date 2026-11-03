package cli

// `satelle sync documents` — the CLI counterpart to the hosted workspace
// path-document store. push/pull are PERSONAL ONLY (epic:sync-publish): local
// .satelle/documents ↔ this repo's bound hosted project.
// Team is not a sync destination — use satelle publish for the team catalog.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/subsync"
)

// newSyncDocumentsCmd builds the `satelle sync documents` group.
func newSyncDocumentsCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "documents",
		Short: "Push/pull documents for this repo's bound hosted project (local default: no hosted write)",
		Long: `Move authored documents between this repo and its bound hosted project.

Nothing leaves the machine under a local scope (unbound, signed out, or not a
member of the project's team), so the default is a deliberate no-op. Documents are authored markdown: a pull writes files you
own, so treat it as an incoming edit, not a refresh.`,
	}

	var pushServer, pushWorkspace string
	var dryRun bool
	push := &cobra.Command{
		Use:   "push",
		Short: "Upload authored documents to the workspace store (a new version per file)",
		Long: `push walks the documents area under the resolved scope — nothing when it is
local — and uploads each file as a new version into this repo's bound hosted PROJECT
only (epic:sync-publish), shared with every member of the project's workspace.
Identical content is idempotent.
Files with a .local segment are never uploaded (reported as withheld).
Team is not a sync destination; use satelle publish to expose documents to a team
catalog. Requires "satelle project bind <slug>".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncDocumentsPush(cmd, pushServer, pushWorkspace, dryRun)
		},
	}
	push.Flags().StringVar(&pushServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	push.Flags().StringVar(&pushWorkspace, "workspace", "", "Ignored for push (sync is personal-only; kept for flag compatibility).")
	push.Flags().BoolVar(&dryRun, "dry-run", false, "List what would be pushed; only the scope is asked of the account.")
	push.Flags().Bool("prune", false, pruneFlagUsage)
	push.Flags().Bool("force", false, forceFlagUsage)
	push.Flags().Bool("snapshot-only", false, snapshotOnlyFlagUsage)
	group.AddCommand(push)

	var pullServer, pullWorkspace string
	pull := &cobra.Command{
		Use:   "pull",
		Short: "Pull document changes into this repo (incremental, byte-exact)",
		Long: `pull fetches documents that changed since the last cursor from this repo's
bound hosted PROJECT only (epic:sync-publish) and writes
them byte-for-byte into this repo's documents area. Up-to-date trees report
"up to date". Requires "satelle project bind <slug>".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncDocumentsPull(cmd, pullServer, pullWorkspace)
		},
	}
	pull.Flags().StringVar(&pullServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	pull.Flags().StringVar(&pullWorkspace, "workspace", "", "Ignored for pull (sync is personal-only; kept for flag compatibility).")
	group.AddCommand(pull)

	return group
}

func runSyncDocumentsPush(cmd *cobra.Command, serverArg, workspaceArg string, dryRun bool) error {
	if err := refuseIfPushOff("sync documents push"); err != nil {
		return err
	}
	cfg, repoRoot, dataDir, err := loadRepoConfig()
	if err != nil {
		return err
	}
	if err := refusePullOnly(repoRoot); err != nil {
		return err
	}
	_ = workspaceArg // sync is personal-only (epic:sync-publish)
	server := resolveServer(serverArg)
	if server == "" {
		return fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}
	ign, err := config.LoadIgnore(dataDir)
	if err != nil {
		return err
	}
	res, err := resolveSyncScope(cmd, cfg, repoRoot, server)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if res.Scope == config.LocalScope {
		fmt.Fprintln(out, "documents push: skipping — "+res.Line())
		return nil
	}
	bundle, err := config.DocumentFiles(cfg, res.Scope, repoRoot, ign)
	if err != nil {
		return err
	}
	files := bundle.Files
	printWithheldLocal := func() { printBundleExclusions(out, bundle) }
	if len(files) == 0 {
		if n := bundle.ExcludedCount(); n > 0 {
			printWithheldLocal()
			fmt.Fprintf(out, "nothing to push (%d withheld)\n", n)
			return nil
		}
		fmt.Fprintln(out, "No documents to push — documents area is empty.")
		return nil
	}
	if note := sharedSyncNote(res.Scope, files); note != "" {
		fmt.Fprintln(out, note)
	}
	if dryRun {
		printDryRunChanges(cmd.Context(), out, cfg, server, repoRoot, dataDir, ign, files, "document(s)")
		printWithheldLocal()
		return nil
	}
	// Bound project before any network (AC5).
	project, err := resolveBoundProject(cfg, repoRoot)
	if err != nil {
		return err
	}
	client := newHostedClient(cmd.Context(), server, repoRoot)
	// Publish the area's whole state as a snapshot (sty_fe5a8ed4); a server that
	// cannot hold snapshots falls through to the plain upload below.
	snap, handled, serr := pushDocumentsSnapshot(cmd, cfg, repoRoot, dataDir, ign, client, server, project, files)
	if serr != nil {
		return serr
	}
	if handled {
		printWithheldLocal()
		fmt.Fprintf(out, "Pushed %d of %d document(s) to project %q on %s: %d new, %d unchanged, %d skipped (unchanged, not uploaded).\n",
			snap.created+snap.unchanged, len(files), project, server, snap.created, snap.unchanged, snap.notUploaded)
		return nil
	}
	// Skip unchanged bytes via server document manifest (sty_88e83180 AC6).
	// Empty since = full set; does not touch the pull cursor.
	headSHA := map[string]string{}
	if changes, merr := client.ListDocumentChanges(cmd.Context(), project, ""); merr != nil {
		if errors.Is(merr, hosted.ErrLoginRequired) {
			return merr
		}
		fmt.Fprintf(out, "documents manifest unavailable (%v) — uploading every file.\n", merr)
	} else {
		headSHA = headSHAByPath(changes.Items)
	}
	var created, unchanged, notUploaded int
	for _, f := range files {
		if contentMatchesSHA(headSHA[f.Path], f.Content) {
			notUploaded++
			continue
		}
		res, perr := client.PushDocumentFile(cmd.Context(), project, f.Path, f.Content)
		if perr != nil {
			if errors.Is(perr, hosted.ErrLoginRequired) {
				return perr
			}
			if errors.Is(perr, hosted.ErrDocumentConflict) {
				return fmt.Errorf("push %s: conflict — resolve on the server and retry", f.Path)
			}
			return fmt.Errorf("push %s: %w", f.Path, perr)
		}
		if res.Created {
			created++
		} else {
			unchanged++
		}
	}
	printWithheldLocal()
	uploaded := created + unchanged
	fmt.Fprintf(out, "Pushed %d of %d document(s) to project %q on %s: %d new, %d unchanged, %d skipped (unchanged, not uploaded).\n",
		uploaded, len(files), project, server, created, unchanged, notUploaded)
	return nil
}

func runSyncDocumentsPull(cmd *cobra.Command, serverArg, workspaceArg string) error {
	cfg, repoRoot, dataDir, err := loadRepoConfig()
	if err != nil {
		return err
	}
	_ = workspaceArg // sync is personal-only (epic:sync-publish)
	server := resolveServer(serverArg)
	if server == "" {
		return fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}
	res, err := resolveSyncScope(cmd, cfg, repoRoot, server)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if res.Scope == config.LocalScope {
		fmt.Fprintln(out, "documents pull: skipping — "+res.Line())
		return nil
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		absRoot = repoRoot
	}
	// Bound project before any network (AC5).
	project, err := resolveBoundProject(cfg, repoRoot)
	if err != nil {
		return err
	}
	client := newHostedClient(cmd.Context(), server, repoRoot)

	// A documents snapshot on the hosted copy governs the pull: it applies what
	// the snapshot names, honours deletions, and parks a file changed on both
	// sides (sty_fe5a8ed4). Without one the cursor-driven pull below runs as before.
	if handled, serr := pullDocumentsSnapshot(cmd, cfg, repoRoot, dataDir, client, server, project, nil); handled || serr != nil {
		return serr
	}

	// Personal only (epic:sync-publish). Team catalog is via publish/adopt.
	// Project-addressed routes (sty_ca64d0cb) need no workspace id.
	written, skipped, unchanged, failed, perr := pullDocumentsFromProject(cmd, client, server, absRoot, dataDir, project, "personal")
	if perr != nil {
		return perr
	}
	// A file that could not be written is REPORTED, never swallowed — but it does
	// not fail the verb, because failing here is what held the cursor back and
	// wedged the pull permanently (sty_4c3729e7 AC1).
	for _, f := range failed {
		fmt.Fprintf(out, "  could not write %s: %v\n", f.Path, f.Err)
	}
	if written == 0 && skipped == 0 && len(failed) == 0 {
		if unchanged > 0 {
			fmt.Fprintf(out, "Documents up to date on %s (%d already identical).\n", server, unchanged)
			return nil
		}
		fmt.Fprintf(out, "Documents up to date on %s.\n", server)
		return nil
	}
	// AC4 (sty_84f14ace): skipped local-only paths must be visible — a skip-only
	// batch must not look identical to "nothing to pull" / "up to date". Same for
	// failures (sty_4c3729e7): a run that could not write must not read as clean.
	var extra []string
	if skipped > 0 {
		extra = append(extra, fmt.Sprintf("%d skipped (local-only path)", skipped))
	}
	if unchanged > 0 {
		extra = append(extra, fmt.Sprintf("%d already identical", unchanged))
	}
	if len(failed) > 0 {
		extra = append(extra, fmt.Sprintf("%d failed (not written)", len(failed)))
	}
	suffix := ""
	if len(extra) > 0 {
		suffix = ", " + strings.Join(extra, ", ")
	}
	if written > 0 {
		fmt.Fprintf(out, "Pulled %d document(s) from project %q on %s into %s%s.\n", written, project, server, dataDir, suffix)
	} else {
		fmt.Fprintf(out, "Documents pull on %s%s; cursor advanced.\n", server, suffix)
	}
	return nil
}

// pullDocumentsFromProject runs one project incremental pull: load cursor, list
// changes, fetch content, Restore, THEN save cursor (only after a successful
// restore so a crash re-fetches rather than silently drops files). Excluded
// (local-only) paths are filtered before fetch (sty_0fd04503) and still skipped
// by Restore as defence in depth, so a partition already poisoned with backups/
// can unwedge without hard-erroring (sty_84f14ace). Project-addressed routes
// (sty_ca64d0cb) key the cursor on (server, project, repo) only.
func pullDocumentsFromProject(cmd *cobra.Command, client *hosted.Client, server, absRoot, dataDir, project, label string) (written, skipped, unchanged int, failed []subsync.FileError, err error) {
	cursor, err := hosted.LoadDocumentCursor(server, project, absRoot)
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("load document cursor (%s): %w", label, err)
	}
	changes, err := client.ListDocumentChanges(cmd.Context(), project, cursor)
	if err != nil {
		if errors.Is(err, hosted.ErrLoginRequired) {
			return 0, 0, 0, nil, err
		}
		return 0, 0, 0, nil, fmt.Errorf("list document changes (%s): %w", label, err)
	}
	if len(changes.Items) == 0 {
		// Still advance the cursor when the server issues a new one with an empty batch.
		if changes.Cursor != "" && changes.Cursor != cursor {
			if err := hosted.SaveDocumentCursor(server, project, absRoot, changes.Cursor); err != nil {
				return 0, 0, 0, nil, fmt.Errorf("save document cursor (%s): %w", label, err)
			}
		}
		return 0, 0, 0, nil, nil
	}
	var files []subsync.File
	preSkipped := 0
	for _, item := range changes.Items {
		// Skip before fetch so a poisoned partition does not pay bandwidth for
		// bodies Restore would refuse (sty_0fd04503 AC1). Restore remains the
		// enforcement point for any path that still reaches it.
		if subsync.ExcludedLocal(item.Path) {
			preSkipped++
			continue
		}
		// Already identical? Don't fetch it, don't rewrite it (sty_4c3729e7).
		// A cursor batch is "what changed since your cursor", not "what differs
		// from your disk": after a push, the very files this client just
		// uploaded come back in the next batch with bytes it already has. The
		// push leg has always compared SHAs before uploading — this is the same
		// comparison on the way down, and it is the reason the wedged file was
		// being rewritten at all. Content equality is the authority; an absent
		// or malformed sha simply falls through to the fetch.
		if localContentMatches(dataDir, item.Path, item.BlobSHA256) {
			unchanged++
			continue
		}
		content, _, ferr := client.DocumentFileContent(cmd.Context(), project, item.Path)
		if ferr != nil {
			if errors.Is(ferr, hosted.ErrLoginRequired) {
				return 0, 0, 0, nil, ferr
			}
			if errors.Is(ferr, hosted.ErrDocumentFileMissing) {
				continue // listed then deleted — skip
			}
			return 0, 0, 0, nil, fmt.Errorf("fetch document %s (%s): %w", item.Path, label, ferr)
		}
		files = append(files, subsync.File{Path: item.Path, Content: content})
	}
	// Totalling sits outside if len(files)>0: an all-excluded batch has files==nil
	// and preSkipped>0; leaving skipped only inside the if would print "up to date"
	// and regress sty_84f14ace AC4.
	skipped = preSkipped
	if len(files) > 0 {
		res, rerr := subsync.Restore(dataDir, files)
		if rerr != nil {
			return 0, 0, 0, nil, fmt.Errorf("restore documents (%s): %w", label, rerr)
		}
		written = res.Written
		skipped += len(res.Skipped)
		// A file that could not be written does NOT abort the pull and does not
		// hold the cursor back (sty_4c3729e7). Returning here is what wedged the
		// pull permanently: the cursor save below never ran, so every later pull
		// re-fetched the same batch and failed on the same file, forever. The
		// failures are surfaced to the caller instead, which prints them.
		failed = res.Failed
	}
	// Cursor advances after a successful restore — including skip-only batches —
	// so already-poisoned partitions unwedge on the next pull (sty_84f14ace AC2),
	// and batches carrying an unwritable file unwedge too (sty_4c3729e7 AC2).
	if changes.Cursor != "" {
		if err := hosted.SaveDocumentCursor(server, project, absRoot, changes.Cursor); err != nil {
			return written, skipped, unchanged, failed, fmt.Errorf("save document cursor (%s): %w", label, err)
		}
	}
	return written, skipped, unchanged, failed, nil
}

// localContentMatches reports whether the file already on disk at rel has
// exactly the bytes the manifest names by sha. False for an absent/unreadable
// file or an absent/malformed sha, so the caller falls through to the fetch —
// this is an optimisation and a rewrite-avoider, never an authority on skipping.
func localContentMatches(dataDir, rel, sha string) bool {
	if strings.TrimSpace(sha) == "" {
		return false
	}
	if subsync.ExcludedLocal(rel) {
		return false
	}
	content, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	return contentMatchesSHA(sha, content)
}
