package cli

// `satelle sync workstate` — work-state push (backup) and pull (rehydrate).
// epic:scoped-sync order:7 delivered push; epic:workspace-rehydrate order:3 adds
// pull. Always targets this repo's bound hosted PROJECT: the server stores
// work state per project, so a project in a team workspace is shared with every
// member of that workspace and a personal project is yours alone; a team
// active-workspace binding never redirects work-state. The one addition is story
// attachments: under a team scope with a team workspace resolved they also go to
// (and come back from) that workspace's documents collection (sty_61872918). The
// resolved scope gates whether the work-state areas (stories, ledger,
// executions) are transferred (local → skip; personal|team → the project).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// WorkstateAreas are the areas that form the work-state kind. Rows go to the
// bound project under any non-local scope; the one other destination is the team
// workspace's documents collection, which a team scope also sends attachments to.
var WorkstateAreas = config.WorkstateAreas

// ErrWorkstatePullConflict is returned when local and hosted both have data for
// a synced area and --force was not set. Nothing from the refused page on is
// written; earlier pages of the pass stay (they passed the same check) and the
// next pass resumes at the refused page.
var ErrWorkstatePullConflict = errors.New("workstate pull conflict")

// newSyncWorkstateCmd builds the `satelle sync workstate` group (push + pull).
func newSyncWorkstateCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "workstate",
		Short: "Push/pull work state for this repo's bound hosted project (local default skips)",
		Long: `Move work state — stories, tasks, ledger, attachments — between this repo and
its bound hosted project.

The destination is the bound project, so everyone with a role on that project
shares it: a project in a team workspace is shared with every member of that
workspace, a personal project is yours alone. A story has two copies — local and
the project — and a pull merges into local rows rather than replacing them. Under
a team scope with a team workspace set, story attachments are also copied to that
team workspace's documents collection, which is what a team remote reads. Skipped
entirely under a local scope.`,
	}

	var pushServer string
	var dryRun, full, pushVerbose bool
	push := &cobra.Command{
		Use:         "push [story-or-epic-id...]",
		Short:       "Replicate work-state to the bound hosted project under the resolved scope",
		Annotations: needsStore(),
		Args:        cobra.ArbitraryArgs,
		Long: `push upserts local stories, task-executions and ledger entries, under a
personal or team scope, into the bound hosted project. Only records
changed since the last push are sent; --full re-sends everything. Requires
"satelle project bind <slug>".

It also uploads each story's markdown attachments, change patches included;
binary ones stay local. Under a team scope with [hosted] workspace set they also
go to the team workspace's documents collection. A refused
upload is retried next push.

Given story ids or an epic id it pushes only those, whole, leaving the cursor.

A story in progress is held until its engagement tree is clean and HEAD is on a
remote-tracking ref ([sync] hold_unpushed = false turns this off).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncWorkstatePush(cmd, pushServer, dryRun, full, pushVerbose, args...)
		},
	}
	push.Flags().StringVar(&pushServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	push.Flags().BoolVar(&dryRun, "dry-run", false, "List which areas would be pushed, and which stories would be held; only the scope is asked of the account.")
	push.Flags().BoolVar(&full, "full", false, "Ignore the stored cursor and push the complete set (then advance the cursor).")
	push.Flags().BoolVar(&pushVerbose, "verbose", false, verboseReachabilityUsage)
	group.AddCommand(push)

	var pullServer string
	var pullDryRun, force, pullVerbose bool
	pull := &cobra.Command{
		Use:         "pull [story-or-epic-id...]",
		Short:       "Restore work-state from the bound hosted project into the local store",
		Annotations: hostedOrigin(needsStore()),
		Args:        cobra.ArbitraryArgs,
		Long: `pull fetches the bound hosted project's stories, executions and ledger
entries under a personal or team scope, and merges them into the local DB by
id. It also writes their markdown attachments into the attachment store (binary
ones stay local); under a team scope it reads the team workspace's documents
collection too, and --force takes a differing team copy.

It refuses only where it would lose local information: a story changed locally
since hosted, or a ledger row whose payload differs. --force lets hosted win.

It then names each pulled story whose status claims code the git remote lacks
(--verbose adds stories that left work with no head).

With story ids or an epic id it pulls only those. Requires "satelle project bind".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncWorkstatePull(cmd, pullServer, pullDryRun, force, pullVerbose, args...)
		},
	}
	pull.Flags().StringVar(&pullServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	pull.Flags().BoolVar(&pullDryRun, "dry-run", false, "List which areas would be pulled; only the scope is asked of the account.")
	pull.Flags().BoolVar(&force, "force", false, "Materialize even where a same-id local row differs from hosted (hosted wins; local-only rows kept).")
	pull.Flags().BoolVar(&pullVerbose, "verbose", false, "List every story that left work with no recorded head, not just a count.")
	group.AddCommand(pull)

	var snapServer string
	var snapForce bool
	snap := &cobra.Command{
		Use:         "snapshot [story-or-epic-id...]",
		Short:       "Pull current hosted work-state into the local store (lazy Snapshot)",
		Annotations: hostedOrigin(needsStore()),
		Args:        cobra.ArbitraryArgs,
		Long: `snapshot fetches the bound project's work-state via gRPC Sync/Snapshot and
materializes the work-state areas locally. A local scope is a no-op.
satelled may exec this verb; it does not talk to the hosted server itself.

A hosted story older than the local row, or than the local ledger's last
status_transition, is skipped so a stale copy cannot rewind status. --force
upserts hosted over those rows.

The project arrives in pages, each applied as it lands. The last completed
pass's token is kept per hosted-server, project and repo, so the next pass
receives only changes; a cut-short pass resumes at its last page. A full pass
runs with no token, with --force, or when the areas, store or token change.

Story or epic ids scope it as for pull.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncWorkstateSnapshot(cmd, snapServer, snapForce, args...)
		},
	}
	snap.Flags().StringVar(&snapServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	snap.Flags().BoolVar(&snapForce, "force", false, "Upsert hosted rows even when the local copy or ledger is newer.")
	group.AddCommand(snap)
	group.AddCommand(newSyncWorkstateRechainCmd())
	group.AddCommand(newSyncWorkstateFetchCmd())
	group.AddCommand(newSyncWorkstateFollowCmd())

	return group
}

func runSyncWorkstateSnapshot(cmd *cobra.Command, serverArg string, force bool, ids ...string) error {
	a, err := appFrom(cmd)
	if err != nil {
		return err
	}
	if repoNotBound(cmd.OutOrStdout(), a, "workstate snapshot") {
		return nil
	}
	server := resolveServer(serverArg)
	syncScope, err := syncScopeGate(cmd, a.Config, a.RepoRoot, server, "workstate snapshot: nothing to pull")
	if err != nil || syncScope == config.LocalScope {
		return err
	}
	optIn := workstateOptIn(syncScope)
	sc, err := resolveScope(cmd.Context(), a, ids, false)
	if err != nil {
		return err
	}
	if server == "" {
		return fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}
	project, err := resolveBoundProject(a.Config, a.RepoRoot)
	if err != nil {
		return err
	}
	client := newHostedClient(cmd.Context(), server, a.RepoRoot)
	var rep passReport
	if sc.scoped() {
		rep, err = runScopedWorkstateSnapshot(cmd.Context(), a, cmd.ErrOrStderr(), client, server, project, optIn, force, snapshotModeSnapshot, sc)
	} else {
		rep, err = runPagedWorkstateSnapshot(cmd.Context(), a, cmd.ErrOrStderr(), client, server, project, optIn, force, snapshotModeSnapshot)
	}
	recordStoryConflicts(cmd.Context(), a, client, project, rep)
	if err != nil {
		return err
	}
	if err := pushMirrorAfterWorkstate(cmd.Context(), a, rep.Items+rep.Ledger); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "workstate: mirror push warning: %v\n", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Snapshot work-state from project %q (%s): %d item(s), %d ledger.\n",
		project, rep.label(), rep.Items, rep.Ledger)
	if rep.Kept > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "%d item(s) kept — local copy is newer than hosted\n", rep.Kept)
	}
	if optIn["stories"] {
		pullStoryAttachments(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), a, syncScope, client, server, project, sc.Set, force)
	}
	return nil
}

// pushMirrorAfterWorkstate posts a light drain snapshot to the local serve
// mirror after workstate materialise so newly pulled stories appear on the
// project page without a fake story mutation (sty_e4e1a008). No-op when nothing
// was upserted or no serve endpoint resolves. Failures are returned for the
// caller to warn on; they must not fail the sync.
func pushMirrorAfterWorkstate(ctx context.Context, a *app.App, upserted int) error {
	if upserted <= 0 || a == nil {
		return nil
	}
	var ep string
	if gc, err := config.LoadGlobal(); err == nil {
		ep = gc.Service.ResolveEndpoint()
	}
	if ep == "" {
		return nil
	}
	pushCtx, cancel := context.WithTimeout(ctx, uiDrainBudget)
	defer cancel()
	snap, err := buildUIDrainSnapshot(pushCtx, a)
	if err != nil {
		return err
	}
	if snap == nil {
		return nil
	}
	snap.Origin = mirror.OriginHosted
	return postUISnapshotContext(pushCtx, ep, snap)
}

// Chunk sizes for work-state push (package vars so tests can shrink them).
var (
	workstateItemChunk   = 500
	workstateLedgerChunk = 1000
)

// verboseReachabilityUsage is the one help text every push that reaches the
// work-state leg gives --verbose.
const verboseReachabilityUsage = "List every story whose code reachability could not be checked, not just a count."

// verboseRequested reads --verbose from the command that is running. A command
// that does not define it (a test's bare command) reads as false.
func verboseRequested(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("verbose")
	return f != nil && f.Value.String() == "true"
}

func runSyncWorkstatePush(cmd *cobra.Command, serverArg string, dryRun, full, verbose bool, ids ...string) (retErr error) {
	if err := refuseIfPushOff("sync workstate push"); err != nil {
		return err
	}
	a, err := appFrom(cmd)
	if err != nil {
		return err
	}
	if err := refusePullOnly(a.RepoRoot); err != nil {
		return err
	}
	server := resolveServer(serverArg)
	if server == "" {
		return fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}
	out := cmd.OutOrStdout()

	// An unbound repo says so before the scope, whose own line would only repeat it.
	if !dryRun && repoNotBound(out, a, "workstate push") {
		return nil
	}
	syncScope, err := syncScopeGate(cmd, a.Config, a.RepoRoot, server, "No work-state to push")
	if err != nil || syncScope == config.LocalScope {
		return err
	}
	optIn := workstateOptIn(syncScope)
	sc, err := resolveScope(cmd.Context(), a, ids, true)
	if err != nil {
		return err
	}
	scope := sc.Set // nil unless ids were named
	if scope != nil {
		defer func() {
			if retErr != nil {
				return
			}
			for _, id := range sc.EmptyEpics {
				emptyEpicNotice(cmd.ErrOrStderr(), id)
			}
		}()
	}
	if dryRun {
		fmt.Fprintf(out, "Would push work-state areas to the bound project on %s:\n", server)
		for _, area := range WorkstateAreas {
			if optIn[area] {
				fmt.Fprintf(out, "  %s -> %s\n", area, syncScope)
			}
		}
		if sc.scoped() {
			fmt.Fprintf(out, "  scoped to %d id(s): %s\n", len(sc.IDs), strings.Join(sc.IDs, ", "))
		}
		return dryRunHolds(cmd, a, optIn, out, server)
	}

	project, err := resolveBoundProject(a.Config, a.RepoRoot)
	if err != nil {
		return err
	}

	repoRoot := a.RepoRoot
	// Story attachments go on every exit of the push that got this far — the early
	// "up to date" and everything-held returns included — because the uploaded-state
	// record, not the rows sent, says what is left to upload. The client is shared
	// with the main flow when it was built, else made only if a file needs it.
	var client *hosted.Client
	clientFor := func() *hosted.Client {
		if client == nil {
			client = newHostedClient(cmd.Context(), server, a.RepoRoot)
		}
		return client
	}
	// It runs after error exits too (a failed Apply, a bad cursor): the files owe
	// nothing to the rows. It is fault-isolated — its failures go to stderr and
	// never touch retErr, so it can neither fail a clean push nor mask a real error.
	defer func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "attachments: %v\n", r)
			}
		}()
		pushStoryAttachments(cmd.Context(), out, cmd.ErrOrStderr(), a, syncScope, optIn, clientFor, server, project, scope)
	}()
	// The repo's recorded push state is the project push's; a scoped push does not
	// make the repo "pushed".
	recordOK := func() {
		if scope == nil {
			recordWorkstatePush(a.RepoRoot, true, "", syncScope.String())
		}
	}
	// A scoped push sends its stories whole and reads no cursor: the stored cursor
	// describes the project push, and a scoped one must not move it.
	var cursor hosted.WorkstateCursor
	var hadCursor bool
	if scope == nil {
		cursor, err = hosted.LoadWorkstateCursor(server, project, repoRoot)
		if err != nil {
			return fmt.Errorf("load workstate cursor: %w", err)
		}
		hadCursor = !cursor.ItemsUpdatedAt.IsZero() || !cursor.LedgerCreatedAt.IsZero() || cursor.LedgerSeq > 0
		if full {
			cursor = hosted.WorkstateCursor{}
		}
	}
	if optIn["ledger"] {
		var reset bool
		cursor, reset, err = validateLedgerCursor(cmd.Context(), a, cursor)
		if err != nil {
			return err
		}
		if reset {
			fmt.Fprintln(cmd.ErrOrStderr(), "ledger cursor reset: store changed; sending full ledger")
		}
	}

	batch, maxItems, lmark, ledgerRows, err := collectWorkstateRows(cmd.Context(), a, optIn, cursor, scope)
	if err != nil {
		return err
	}
	startMark := ledgerMark{
		createdAt: cursor.LedgerCreatedAt,
		seq:       cursor.LedgerSeq,
		anchorID:  cursor.LedgerAnchorID,
		storeID:   lmark.storeID,
	}
	// SQL uses a whole-second lower bound so boundary-second rows reappear;
	// drop anything not strictly after the cursor so a true no-op sends nothing
	// (AC2/AC7) while same-second new nanos still After() and ride (sty_88e83180).
	// The ledger needs no such filter: it is paged strictly after an insertion
	// position, not a timestamp (sty_4a31e1ed).
	if !full && !cursor.ItemsUpdatedAt.IsZero() {
		batch.Items = filterItemsAfter(batch.Items, cursor.ItemsUpdatedAt)
	}
	// The stories whose events are not yet on the server (sty_9bdf08c4). Heads are
	// read apart from the cursor: a scoped push reads no cursor, and --full resends
	// rows but not what the server already accepted.
	var plan []storyAppend
	if optIn["ledger"] {
		stored, lerr := hosted.LoadWorkstateCursor(server, project, repoRoot)
		if lerr != nil {
			return fmt.Errorf("load workstate cursor: %w", lerr)
		}
		if plan, err = planStoryAppends(cmd.Context(), a, scope, stored.StoryHeads); err != nil {
			return err
		}
	}
	// In hosted mode a story's row leaves only when this machine authored it
	// (sty_0dc600e1): a row rewritten by a pull, or one whose updated_at moved with
	// no local append, is the server's copy and is not sent back.
	hostedMode := verb.HostedMode(a.Config)
	if hostedMode && optIn["stories"] {
		if batch.Items, err = authoredItems(cmd.Context(), a, batch.Items, plan, scope); err != nil {
			return err
		}
	}

	if len(batch.Items) == 0 && len(batch.Ledger) == 0 && len(plan) == 0 {
		if hadCursor && !full {
			fmt.Fprintf(out, "Work-state up to date on %s — no records changed since the last push.\n", server)
			recordWorkstatePush(a.RepoRoot, true, "", syncScope.String())
			return nil
		}
		fmt.Fprintln(out, "No work-state rows to push (the work-state areas are empty).")
		return nil
	}

	client = clientFor()
	errw := cmd.ErrOrStderr()

	// Hold a story whose code is not on the git remote (sty_7361569a). Held rows
	// stay behind the cursors, so the next push re-checks and sends them.
	holdOn, err := config.HoldUnpushed(a.Config)
	if err != nil {
		return err
	}
	var held map[string]string
	var earliestHeld time.Time
	codeHeld := 0 // stories held for unpushed code, apart from those withheld for drift
	// A story's cached row is validated against the fold of its ledger chain before
	// any of its events leave (sty_d84be8bc). One that disagrees is withheld like a
	// held story — its rows stay behind the cursors — and the push reports it and
	// fails once the others have been sent, so a withheld story is never silent.
	// A story with events to append is classified whether or not the batch names it:
	// its rows may sit behind the cursors while its events still leave.
	classifyRows := slices.Clone(ledgerRows)
	for _, p := range plan {
		classifyRows = append(classifyRows, ledgerRowMeta{storyID: p.id})
	}
	drift, derr := classifyFoldDrift(cmd.Context(), a, batch.Items, classifyRows)
	if derr != nil {
		return derr
	}
	for _, l := range drift.lines {
		fmt.Fprintln(out, l)
		fmt.Fprintln(errw, l)
	}
	if len(drift.held) > 0 {
		driftErr := fmt.Errorf("%d stor(y/ies) withheld: the cached row disagrees with the ledger chain — run `satelle story reconcile --repair`, then push again", len(drift.held))
		defer func() {
			if retErr == nil {
				retErr = driftErr
			}
		}()
	}
	if holdOn || len(drift.held) > 0 {
		res := holdResult{held: map[string]string{}, unavailable: map[string]string{}}
		if holdOn {
			var herr error
			if res, herr = classifyHolds(cmd.Context(), a, batch.Items, classifyRows); herr != nil {
				return herr
			}
			for _, l := range res.lines(verbose) {
				fmt.Fprintln(out, l)
			}
		}
		codeHeld = len(res.held)
		withheld := maps.Clone(res.held)
		for id, why := range drift.held {
			withheld[id] = why
		}
		if len(withheld) > 0 {
			held = withheld
			batch.Items, batch.Ledger, earliestHeld = withoutHeld(held, batch.Items, batch.Ledger, ledgerRows)
			lmark = ledgerMarkBeforeHold(startMark, held, ledgerRows)
			if !earliestHeld.IsZero() {
				maxItems = itemsCursorBefore(batch.Items, earliestHeld, cursor.ItemsUpdatedAt)
			}
		}
	}

	// Story events go by compare-and-append, never through Apply (sty_9bdf08c4).
	withStories := len(batch.Ledger)
	batch.Ledger = withoutStoryEntries(batch.Ledger)
	storyRowsDropped := len(batch.Ledger) < withStories
	published, appendErr := publishStoryEvents(cmd.Context(), errw, a, client, server, project, plan, held)
	if published > 0 {
		fmt.Fprintf(out, "Published the events of %d stor(y/ies) to project %q on %s.\n", published, project, server)
	}
	if appendErr != nil {
		if errors.Is(appendErr, hosted.ErrLoginRequired) {
			recordWorkstatePush(a.RepoRoot, false, appendErr.Error(), syncScope.String())
			return appendErr
		}
		fmt.Fprintln(out, appendErr.Error())
		recordOK = func() { recordWorkstatePush(a.RepoRoot, false, appendErr.Error(), syncScope.String()) }
		defer func() {
			if retErr == nil {
				retErr = appendErr
			}
		}()
	}
	if held != nil && len(batch.Items) == 0 && len(batch.Ledger) == 0 {
		fmt.Fprintln(out, "No work-state rows to push after holding unpushed stories.")
		recordOK()
		return nil
	}

	// Chunked push; cursor advances only after every chunk confirms (AC3).
	// Prefer a single POST when both sides fit in one chunk (preserves the
	// small-batch shape tests and production already rely on).
	if len(batch.Items) > 0 && earliestHeld.IsZero() && !hostedMode {
		maxItems = maxItemUpdatedAt(batch.Items)
	}
	var totalItems, totalLedger int
	apply := func(items, ledgerRaw []json.RawMessage) error {
		if items == nil {
			items = []json.RawMessage{}
		}
		if ledgerRaw == nil {
			ledgerRaw = []json.RawMessage{}
		}
		res, perr := client.Apply(cmd.Context(), project, hosted.WorkstateIngest{Items: items, Ledger: ledgerRaw})
		if perr != nil {
			recordWorkstatePush(a.RepoRoot, false, perr.Error(), syncScope.String())
			if errors.Is(perr, hosted.ErrLoginRequired) {
				return perr
			}
			return fmt.Errorf("apply workstate: %w", perr)
		}
		totalItems += res.Items
		totalLedger += res.Ledger
		return nil
	}
	// Story rows were sent as events, so the ledger cursor still moves past them.
	if len(batch.Items) == 0 && len(batch.Ledger) == 0 && !storyRowsDropped {
		fmt.Fprintln(out, "No work-state rows to push after the story events.")
		return nil
	}
	type partial struct {
		items  []json.RawMessage
		ledger []json.RawMessage
	}
	var parts []partial
	switch {
	case len(batch.Items) == 0 && len(batch.Ledger) == 0:
		// Only story rows were due and they went as events.
	case len(batch.Items) <= workstateItemChunk && len(batch.Ledger) <= workstateLedgerChunk:
		parts = []partial{{items: batch.Items, ledger: batch.Ledger}}
	default:
		for _, c := range chunkRaw(batch.Items, workstateItemChunk) {
			parts = append(parts, partial{items: c})
		}
		for _, c := range chunkRaw(batch.Ledger, workstateLedgerChunk) {
			parts = append(parts, partial{ledger: c})
		}
	}
	for _, p := range parts {
		if err := apply(p.items, p.ledger); err != nil {
			return err
		}
		if hostedMode {
			if err := clearAuthored(cmd.Context(), a, p.items); err != nil {
				return fmt.Errorf("clear pushed story marks: %w", err)
			}
		}
	}
	if scope != nil {
		fmt.Fprintf(out, "scoped push to project %q on %s: %d item(s), %d ledger entr(y/ies).\n",
			project, server, totalItems, totalLedger)
	} else {
		// Advance cursor only after full success.
		next := hosted.WorkstateCursor{
			ItemsUpdatedAt:  maxItems,
			LedgerCreatedAt: lmark.createdAt,
			LedgerSeq:       lmark.seq,
			LedgerAnchorID:  lmark.anchorID,
			LedgerStoreID:   lmark.storeID,
		}
		if err := hosted.SaveWorkstateCursor(server, project, repoRoot, next); err != nil {
			return fmt.Errorf("save workstate cursor: %w", err)
		}
		fmt.Fprintf(out, "Pushed work-state to project %q on %s: %d item(s), %d ledger entr(y/ies).\n",
			project, server, totalItems, totalLedger)
	}
	if codeHeld > 0 {
		fmt.Fprintf(out, "%d stor(y/ies) held until their code is on the remote.\n", codeHeld)
	}
	recordOK()
	return nil
}

func recordWorkstatePush(repoPath string, success bool, reason, scope string) {
	_ = syncstate.RecordPush(config.GlobalDir(), repoPath, success, reason, scope, time.Now())
}

func chunkRaw(in []json.RawMessage, size int) [][]json.RawMessage {
	if size <= 0 {
		size = 1
	}
	if len(in) == 0 {
		return nil
	}
	var out [][]json.RawMessage
	for i := 0; i < len(in); i += size {
		end := i + size
		if end > len(in) {
			end = len(in)
		}
		out = append(out, in[i:end])
	}
	return out
}

// filterItemsAfter keeps only marshaled workstate items whose updated_at is
// strictly after since. Records at the cursor high-water mark (re-selected by
// the second-truncated SQL bound) drop out so a no-op push issues no request.
func filterItemsAfter(items []json.RawMessage, since time.Time) []json.RawMessage {
	if since.IsZero() || len(items) == 0 {
		return items
	}
	var out []json.RawMessage
	for _, raw := range items {
		var w struct {
			UpdatedAt time.Time `json:"updated_at"`
		}
		if err := json.Unmarshal(raw, &w); err != nil || !w.UpdatedAt.After(since) {
			continue
		}
		out = append(out, raw)
	}
	if out == nil {
		return []json.RawMessage{}
	}
	return out
}

func runSyncWorkstatePull(cmd *cobra.Command, serverArg string, dryRun, force, verbose bool, ids ...string) error {
	a, err := appFrom(cmd)
	if err != nil {
		return err
	}
	server := resolveServer(serverArg)
	if server == "" {
		return fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}
	out := cmd.OutOrStdout()
	if !dryRun && repoNotBound(out, a, "workstate pull") {
		return nil
	}
	syncScope, err := syncScopeGate(cmd, a.Config, a.RepoRoot, server, "No work-state to pull")
	if err != nil || syncScope == config.LocalScope {
		return err
	}
	optIn := workstateOptIn(syncScope)
	ctx := cmd.Context()
	sc, err := resolveScope(ctx, a, ids, false)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(out, "Would pull work-state areas from the bound project on %s:\n", server)
		for _, area := range WorkstateAreas {
			if optIn[area] {
				fmt.Fprintf(out, "  personal -> %s\n", area)
			}
		}
		if sc.scoped() {
			fmt.Fprintf(out, "  scoped to %d id(s): %s\n", len(sc.IDs), strings.Join(sc.IDs, ", "))
		}
		return nil
	}

	project, err := resolveBoundProject(a.Config, a.RepoRoot)
	if err != nil {
		return err
	}

	client := newHostedClient(ctx, server, a.RepoRoot)

	var rep passReport
	if sc.scoped() {
		sc.ExpandRemoteEpics = true
		rep, err = runScopedWorkstateSnapshot(ctx, a, cmd.ErrOrStderr(), client, server, project, optIn, force, snapshotModePull, sc)
		if err == nil && rep.Scope.scoped() {
			sc = rep.Scope // the members taken from the server, for the attachment step
		}
	} else {
		rep, err = runPagedWorkstateSnapshot(ctx, a, cmd.ErrOrStderr(), client, server, project, optIn, force, snapshotModePull)
	}
	recordStoryConflicts(ctx, a, client, project, rep)
	if err != nil {
		if errors.Is(err, hosted.ErrLoginRequired) || errors.Is(err, ErrWorkstatePullConflict) {
			return err
		}
		return fmt.Errorf("snapshot workstate: %w", err)
	}
	// The pulled stories' attachments follow them, whether or not any row moved.
	if optIn["stories"] {
		defer pullStoryAttachments(ctx, out, cmd.ErrOrStderr(), a, syncScope, client, server, project, sc.Set, force)
	}
	nItems, nLedger, nKept := rep.Items, rep.Ledger, rep.Kept
	if nItems == 0 && nLedger == 0 && nKept == 0 {
		if rep.complete() {
			fmt.Fprintf(out, "No work-state rows to pull (%s; the hosted work-state areas are empty).\n", rep.label())
		} else {
			fmt.Fprintf(out, "No work-state rows to pull (%s; nothing changed on hosted since the last pass).\n", rep.label())
		}
		return nil
	}
	if err := pushMirrorAfterWorkstate(ctx, a, nItems+nLedger); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "workstate: mirror push warning: %v\n", err)
	}
	fmt.Fprintf(out, "Pulled work-state from project %q on %s (%s): %d item(s), %d ledger entr(y/ies).\n",
		project, server, rep.label(), nItems, nLedger)
	if nKept > 0 {
		fmt.Fprintf(out, "%d item(s) kept — local copy is newer than hosted\n", nKept)
	}
	if optIn["stories"] {
		var ids []string
		for _, hi := range rep.Hosted {
			if hi.Kind == string(workitem.KindStory) {
				ids = append(ids, hi.ID)
			}
		}
		for _, line := range reconcilePulledStories(ctx, a, ids, verbose) {
			fmt.Fprintln(out, line)
		}
	}
	return nil
}

// workstateOptIn is the work-state areas a sync under scope moves: every one
// under a non-local scope, none under a local one.
func workstateOptIn(scope config.Scope) map[string]bool {
	optIn := map[string]bool{}
	if scope == config.LocalScope {
		return optIn
	}
	for _, area := range WorkstateAreas {
		optIn[area] = true
	}
	return optIn
}

// maxConflictIDs caps how many conflicting ids per area the refusal names.
const maxConflictIDs = 10

// checkWorkstatePullConflicts refuses a pull only where it would lose local
// information. Merge is by id, so a local row the hosted copy has never seen, or
// a same-id row that is identical to or older than hosted, is safe. What is not:
// a same-id story the local side has changed since hosted (newer and differing),
// and a same-id ledger row whose kind or payload differs (a ledger row is never
// rewritten, so the hosted version would be dropped).
func checkWorkstatePullConflicts(ctx context.Context, a *app.App, optIn map[string]bool, items []hosted.WorkstateItem, ledgerRows []hosted.WorkstateLedgerRow) error {
	found := map[string][]string{}
	for _, hi := range items {
		area := workstateAreaForKind(hi.Kind)
		if area == "" || !optIn[area] {
			continue
		}
		incoming, err := parseWorkstateItem(hi)
		if err != nil {
			continue // materialize names the undecodable row
		}
		local, err := a.Store.Stories.Get(ctx, incoming.ID)
		if err != nil {
			if errors.Is(err, workitem.ErrNotFound) {
				continue
			}
			return err
		}
		if local.UpdatedAt.After(incoming.UpdatedAt) && workstateItemsDiffer(local, incoming) {
			found[area] = append(found[area], incoming.ID)
		}
	}
	if optIn["ledger"] {
		for _, hr := range ledgerRows {
			incoming, err := parseWorkstateLedger(hr)
			if err != nil {
				continue
			}
			local, ok, err := a.Store.Ledger.GetByID(ctx, incoming.ID)
			if err != nil {
				return err
			}
			if ok && workstateLedgerDiffers(local, incoming) {
				found["ledger"] = append(found["ledger"], incoming.ID)
			}
		}
	}
	var conflicts []string
	stories := slices.Clone(found["stories"])
	slices.Sort(stories)
	for _, area := range WorkstateAreas {
		ids := found[area]
		if len(ids) == 0 {
			continue
		}
		slices.Sort(ids)
		extra := ""
		if len(ids) > maxConflictIDs {
			extra = fmt.Sprintf(" (+%d more)", len(ids)-maxConflictIDs)
			ids = ids[:maxConflictIDs]
		}
		conflicts = append(conflicts, fmt.Sprintf("%s %s%s", area, strings.Join(ids, ", "), extra))
	}
	if len(conflicts) == 0 {
		return nil
	}
	return &workstatePullConflict{
		err: fmt.Errorf("%w: %s — re-run with --force to let hosted win (local-only rows kept; an existing ledger row is never rewritten)",
			ErrWorkstatePullConflict, strings.Join(conflicts, "; ")),
		Stories: stories,
	}
}

// workstatePullConflict is the refusal checkWorkstatePullConflicts returns. It
// reads and unwraps as ErrWorkstatePullConflict; Stories carries the story ids
// that differ so the pass can record them (sty_6e5fc41c).
type workstatePullConflict struct {
	err     error
	Stories []string
}

func (e *workstatePullConflict) Error() string { return e.err.Error() }
func (e *workstatePullConflict) Unwrap() error { return e.err }

// workstateItemsDiffer compares the fields a work-state record carries, leaving
// the timestamps out: they say which side is newer, not whether the content differs.
func workstateItemsDiffer(a, b workitem.Item) bool {
	return a.Kind != b.Kind || a.Title != b.Title || a.Body != b.Body || a.Status != b.Status ||
		a.Priority != b.Priority || a.Category != b.Category || a.ParentID != b.ParentID ||
		a.AcceptanceCriteria != b.AcceptanceCriteria || a.Archived != b.Archived ||
		a.ParkOrigin != b.ParkOrigin || !slices.Equal(a.Tags, b.Tags)
}

// workstateLedgerDiffers reports whether two same-id ledger rows disagree on
// kind or payload. Payloads are compared as JSON values, so key order and
// whitespace do not count, and an empty payload equals {}.
func workstateLedgerDiffers(a, b ledger.Entry) bool {
	if a.Kind != b.Kind {
		return true
	}
	return !jsonPayloadEqual(a.Payload, b.Payload)
}

func jsonPayloadEqual(a, b json.RawMessage) bool {
	var av, bv any
	if len(a) == 0 {
		a = json.RawMessage("{}")
	}
	if len(b) == 0 {
		b = json.RawMessage("{}")
	}
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(av, bv)
}

// upsertWorkstatePage upserts hosted rows into the local store for the synced
// areas and returns the items it applied, with counts of rows applied and of
// local rows kept because they (or their ledger) are newer than hosted. Status
// forwarding and the story-view regeneration are the caller's: a paged pass
// writes each page here and regenerates the views once at the end. On a
// mid-batch error it reports how many landed and that re-run is safe.
//
// out, when set, collects what the page says about story disagreements: a
// local row kept whose content differs from the hosted one is a conflict, and a
// story applied or kept with identical content clears any recorded one. The
// local copy is never changed to resolve it (sty_6e5fc41c).
// orderLedgerRows is the order a page's ledger rows are inserted in: the order the
// events were made (created_at, ties as they came), except that an event whose base
// is another row of the page follows that row. Ties on created_at, and a clock that
// ran behind within one command, would otherwise put an event ahead of the one it
// builds on, and the story's chain would no longer be one a push can continue
// (sty_40853d02: a machine that pulls a story and then works it).
func orderLedgerRows(rows []hosted.WorkstateLedgerRow) []hosted.WorkstateLedgerRow {
	type parsed struct {
		row hosted.WorkstateLedgerRow
		e   ledger.Entry
		ok  bool
	}
	ps := make([]parsed, len(rows))
	for i, r := range rows {
		e, err := parseWorkstateLedger(r)
		ps[i] = parsed{row: r, e: e, ok: err == nil}
	}
	slices.SortStableFunc(ps, func(x, y parsed) int {
		if !x.ok || !y.ok {
			return 0
		}
		return x.e.CreatedAt.Compare(y.e.CreatedAt)
	})
	at := make(map[string]int, len(ps))
	for i, p := range ps {
		if p.ok {
			at[p.e.ID] = i
		}
	}
	out := make([]hosted.WorkstateLedgerRow, 0, len(ps))
	placed := make([]bool, len(ps))
	var place func(i int)
	place = func(i int) {
		if placed[i] {
			return
		}
		placed[i] = true // before the base, so a cycle in the data cannot recurse forever
		if p := ps[i]; p.ok && p.e.BaseID != "" {
			if j, in := at[p.e.BaseID]; in {
				place(j)
			}
		}
		out = append(out, ps[i].row)
	}
	for i := range ps {
		place(i)
	}
	return out
}

func upsertWorkstatePage(ctx context.Context, a *app.App, optIn map[string]bool, items []hosted.WorkstateItem, ledgerRows []hosted.WorkstateLedgerRow, force bool, now time.Time, out *storyOutcome, chain *chainPull) (applied []workitem.Item, nItems, nLedger, nKept int, err error) {
	priorStatus := map[string]string{}
	for _, hi := range items {
		area := workstateAreaForKind(hi.Kind)
		if area == "" || !optIn[area] {
			continue
		}
		it, perr := parseWorkstateItem(hi)
		if perr != nil {
			return applied, nItems, nLedger, nKept, fmt.Errorf("materialize item %s after %d item(s), %d ledger: %w — re-run is safe (upsert-by-id)", hi.ID, nItems, nLedger, perr)
		}
		if !force {
			keep, local, kerr := keepLocalWorkstateItem(ctx, a, it)
			if kerr != nil {
				return applied, nItems, nLedger, nKept, fmt.Errorf("compare item %s after %d item(s), %d ledger: %w — re-run is safe (upsert-by-id)", hi.ID, nItems, nLedger, kerr)
			}
			if keep && chain.keepsLocal(it) {
				nKept++
				if it.Kind == workitem.KindStory {
					if workstateItemsDiffer(local, it) {
						out.conflict(it.ID, conflictLocalAhead)
					} else {
						out.clear(it.ID)
					}
				}
				continue
			}
		}
		// --force means the hosted copy wins outright, including a status the
		// local row has already moved past — UpsertForce. The default path uses
		// Upsert, which preserves stored status (sty_38915987); status is
		// applied below after the hosted ledger lands.
		upsert := a.Store.Stories.Upsert
		if force || chain.remote(it) {
			// A story whose history stays on the server is the server's row.
			upsert = a.Store.Stories.UpsertForce
		}
		// The status the row holds now, before UpsertForce can replace it: the
		// rebuild below names a terminal story it moves off it (foldPulledStories).
		if it.Kind == workitem.KindStory {
			if local, gerr := a.Store.Stories.Get(ctx, it.ID); gerr == nil {
				priorStatus[it.ID] = local.Status
			} else if !errors.Is(gerr, workitem.ErrNotFound) {
				return applied, nItems, nLedger, nKept, fmt.Errorf("read item %s after %d item(s), %d ledger: %w — re-run is safe (upsert-by-id)", hi.ID, nItems, nLedger, gerr)
			}
		}
		if _, uerr := upsert(ctx, it, now); uerr != nil {
			return applied, nItems, nLedger, nKept, fmt.Errorf("upsert item %s after %d item(s), %d ledger: %w — re-run is safe (upsert-by-id)", hi.ID, nItems, nLedger, uerr)
		}
		applied = append(applied, it)
		nItems++
		if it.Kind == workitem.KindStory {
			out.clear(it.ID)
		}
	}
	if optIn["ledger"] {
		// A row that arrives without a base is linked to the story's tail as it is
		// inserted, so arrival order becomes its place in the chain the fold reads.
		// Insert in the order the events were made, whatever order the page lists
		// them in (sty_d84be8bc). A transition linked after the story has moved on
		// (an earlier attempt's, pulled late) names a from the chain is no longer at,
		// so the fold skips it rather than letting it become the status.
		for _, hr := range orderLedgerRows(ledgerRows) {
			e, perr := parseWorkstateLedger(hr)
			if perr != nil {
				return applied, nItems, nLedger, nKept, fmt.Errorf("materialize ledger %s after %d item(s), %d ledger: %w — re-run is safe (upsert-by-id)", hr.ID, nItems, nLedger, perr)
			}
			if _, uerr := a.Store.Ledger.Upsert(ctx, e, now); uerr != nil {
				return applied, nItems, nLedger, nKept, fmt.Errorf("upsert ledger %s after %d item(s), %d ledger: %w — re-run is safe (upsert-by-id)", hr.ID, nItems, nLedger, uerr)
			}
			nLedger++
		}
	}
	// The events of a story are not on the page: the hosted chain of each story it
	// applied is read before the rows are folded.
	if cerr := chain.materialize(ctx, a, optIn, applied, out, now); cerr != nil {
		return applied, nItems, nLedger, nKept, fmt.Errorf("%w — re-run is safe (upsert-by-id)", cerr)
	}
	folded, rerr := leaveRemote(ctx, a, chain, applied)
	if rerr != nil {
		return applied, nItems, nLedger, nKept, fmt.Errorf("%w — re-run is safe (upsert-by-id)", rerr)
	}
	// The page's events are in: the rows it applied are now a cache of their chains.
	if err := foldPulledStories(ctx, a, folded, priorStatus, out, force, now); err != nil {
		return applied, nItems, nLedger, nKept, fmt.Errorf("%w — re-run is safe (upsert-by-id)", err)
	}
	return applied, nItems, nLedger, nKept, nil
}

// foldPulledStories brings the stories a pull applied under their chains
// (sty_d84be8bc): each cached row is rebuilt from the fold of its chain. The
// chain decides status — a transition that disagrees with the hosted row's status
// moves the row, and a story with no transition takes the hosted row's — so no
// separate status forwarding exists. A task or execution is not folded.
//
// The pass appends nothing to a chain (sty_199ad82b): a chain moves only by the
// hosted events it took. A story whose hosted chain was not read is
// left as its row, never seeded here; a seed would be a local-only event that
// forks the chain and blocks the next push.
//
// A forced pull lets the hosted copy win on the row. When the chain's definition
// or tags disagree with the hosted row, the row keeps the hosted copy UpsertForce
// wrote, the chain is left as it is, and the disagreement is recorded as a
// conflict rather than an event made up to set the hosted copy back.
//
// A story that was done or cancelled here and whose rebuild leaves it in a
// non-terminal status is named in out — the story, both statuses and the event
// that set the new one — whichever path wrote the row first (priorStatus is read
// before either), so a pull never moves a closed story silently.
func foldPulledStories(ctx context.Context, a *app.App, applied []workitem.Item, priorStatus map[string]string, out *storyOutcome, force bool, now time.Time) error {
	for _, hosted := range applied {
		if hosted.Kind != workitem.KindStory {
			continue
		}
		row, err := a.Store.Stories.Get(ctx, hosted.ID)
		if err != nil {
			return fmt.Errorf("read %s after pull: %w", hosted.ID, err)
		}
		st, _, ferr := verb.FoldStory(ctx, a.Store.Ledger, row)
		if ferr != nil {
			return fmt.Errorf("fold %s: %w", hosted.ID, ferr)
		}
		if force {
			if _, differs := verb.HostedWinsInput(st, hosted, "sync workstate pull --force"); differs {
				out.conflict(hosted.ID, conflictForcedHostedWins)
				continue
			}
		}
		// A chain that sets no status leaves the row's to the cache: it takes the
		// hosted row's, which Upsert kept off an existing row.
		if !st.Defined[storyfold.FieldStatus] && hosted.Status != "" && row.Status != hosted.Status {
			if row, err = a.Store.Stories.SetStatus(ctx, hosted.ID, hosted.Status, now); err != nil {
				return fmt.Errorf("take the hosted status of %s: %w", hosted.ID, err)
			}
		}
		rebuilt, err := verb.RebuildRow(ctx, a.Store.Stories, a.Store.Ledger, row, now)
		if err != nil {
			return fmt.Errorf("rebuild %s from its chain: %w", hosted.ID, err)
		}
		prior, had := priorStatus[hosted.ID]
		if !had || prior == rebuilt.Status || !verb.EndsEngagementForGood(ctx, rebuilt, prior) || verb.EndsEngagementForGood(ctx, rebuilt, rebuilt.Status) {
			continue
		}
		st, _, ferr = verb.FoldStory(ctx, a.Store.Ledger, rebuilt)
		if ferr != nil {
			return fmt.Errorf("fold %s: %w", hosted.ID, ferr)
		}
		by := "the hosted row"
		if st.StatusEvent != "" {
			by = "event " + st.StatusEvent
		}
		out.moved(fmt.Sprintf("%s was %s and the pull moved it to %s (set by %s)", hosted.ID, prior, rebuilt.Status, by))
	}
	return nil
}

// keepLocalWorkstateItem reports whether the local row is newer than the
// incoming hosted item, so upserting would overwrite what was changed here. It
// returns the local row it compared, for a caller that records the difference.
// Status is not weighed: a story's status is the fold of its chain, which the
// pull rebuilds the row from (foldPulledStories).
func keepLocalWorkstateItem(ctx context.Context, a *app.App, incoming workitem.Item) (bool, workitem.Item, error) {
	local, err := a.Store.Stories.Get(ctx, incoming.ID)
	if err != nil {
		if errors.Is(err, workitem.ErrNotFound) {
			return false, workitem.Item{}, nil
		}
		return false, workitem.Item{}, err
	}
	return local.UpdatedAt.After(incoming.UpdatedAt), local, nil
}

func workstateAreaForKind(kind string) string {
	switch kind {
	case string(workitem.KindStory):
		return "stories"
	case string(workitem.KindExecution):
		return "executions"
	default:
		return ""
	}
}

// workstateItemWire is the CLI record shape pushed as payload and restored from
// hosted.record on pull.
type workstateItemWire struct {
	ID                 string    `json:"id"`
	Kind               string    `json:"kind"`
	Status             string    `json:"status"`
	Title              string    `json:"title"`
	Body               string    `json:"body,omitempty"`
	Priority           string    `json:"priority,omitempty"`
	Category           string    `json:"category,omitempty"`
	ParentID           string    `json:"parent_id,omitempty"`
	AcceptanceCriteria string    `json:"acceptance_criteria,omitempty"`
	Tags               []string  `json:"tags,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
	CreatedAt          time.Time `json:"created_at"`
	Archived           bool      `json:"archived,omitempty"`
	ParkOrigin         string    `json:"park_origin,omitempty"`
	// InputsDecl (a task's declared inputs) and Inputs (an execution's values) are
	// the cross-repo contract with the hosted workstate.
	InputsDecl []workitem.InputDecl `json:"inputs_decl,omitempty"`
	Inputs     map[string]any       `json:"inputs,omitempty"`
}

func parseWorkstateItem(hi hosted.WorkstateItem) (workitem.Item, error) {
	raw := hi.Record
	if len(raw) == 0 {
		// Fall back to promoted fields when record is empty.
		raw = mustJSON(map[string]any{
			"id": hi.ID, "kind": hi.Kind, "status": hi.Status, "title": hi.Title,
		})
	}
	var w workstateItemWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return workitem.Item{}, fmt.Errorf("decode record: %w", err)
	}
	if w.ID == "" {
		w.ID = hi.ID
	}
	if w.Kind == "" {
		w.Kind = hi.Kind
	}
	if w.Title == "" {
		w.Title = hi.Title
	}
	if w.Status == "" {
		w.Status = hi.Status
	}
	k := workitem.Kind(w.Kind)
	switch k {
	case workitem.KindStory, workitem.KindTask, workitem.KindExecution:
	default:
		return workitem.Item{}, fmt.Errorf("invalid kind %q", w.Kind)
	}
	if strings.TrimSpace(w.Title) == "" {
		return workitem.Item{}, fmt.Errorf("title required")
	}
	return workitem.Item{
		ID: w.ID, Kind: k, Title: w.Title, Body: w.Body, Status: w.Status,
		Priority: w.Priority, Category: w.Category, ParentID: w.ParentID,
		AcceptanceCriteria: w.AcceptanceCriteria, Tags: w.Tags,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, Archived: w.Archived,
		ParkOrigin: w.ParkOrigin, Inputs: w.InputsDecl,
		// A JSON round trip decodes numbers as float64; re-type them as the store does.
		InputValues: workitem.DecodeInputValues(workitem.EncodeInputValues(w.Inputs)),
	}, nil
}

func parseWorkstateLedger(hr hosted.WorkstateLedgerRow) (ledger.Entry, error) {
	raw := hr.Record
	if len(raw) == 0 {
		raw = mustJSON(map[string]any{
			"id": hr.ID, "story_id": hr.StoryID, "kind": hr.Kind,
		})
	}
	var w struct {
		ID           string          `json:"id"`
		StoryID      string          `json:"story_id,omitempty"`
		ProjectID    string          `json:"project_id,omitempty"`
		Kind         string          `json:"kind"`
		Actor        string          `json:"actor,omitempty"`
		Body         string          `json:"body,omitempty"`
		Payload      json.RawMessage `json:"payload,omitempty"`
		Refs         json.RawMessage `json:"refs,omitempty"`
		CreatedAt    time.Time       `json:"created_at"`
		BaseID       string          `json:"base_id,omitempty"`
		WorkflowHash string          `json:"workflow_hash,omitempty"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return ledger.Entry{}, fmt.Errorf("decode record: %w", err)
	}
	if w.ID == "" {
		w.ID = hr.ID
	}
	if w.Kind == "" {
		w.Kind = hr.Kind
	}
	if w.StoryID == "" {
		w.StoryID = hr.StoryID
	}
	if strings.TrimSpace(w.ID) == "" || strings.TrimSpace(w.Kind) == "" {
		return ledger.Entry{}, fmt.Errorf("ledger id and kind required")
	}
	return ledger.Entry{
		ID: w.ID, StoryID: w.StoryID, ProjectID: w.ProjectID, Kind: w.Kind,
		Actor: w.Actor, Body: w.Body, Payload: w.Payload, Refs: w.Refs,
		CreatedAt: w.CreatedAt, BaseID: w.BaseID, WorkflowHash: w.WorkflowHash,
	}, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// collectWorkstate builds the full ingest batch (no cursor). Kept for pull
// conflict checks and tests that want an unfiltered snapshot.
func collectWorkstate(ctx context.Context, a *app.App, optIn map[string]bool) (hosted.WorkstateIngest, error) {
	batch, _, _, err := collectWorkstateSince(ctx, a, optIn, hosted.WorkstateCursor{})
	return batch, err
}

// ledgerMark is where a collection left the ledger: the insertion position and
// id of the last row sent, the database it was read from, and the newest
// created_at seen.
type ledgerMark struct {
	createdAt time.Time
	seq       int64
	anchorID  string
	storeID   string
}

// validateLedgerCursor drops a ledger position that no longer belongs to the
// local store, so the next collection sends the whole ledger once (idempotent:
// the server ingests by id). A position belongs when the store's instance id
// matches the one saved AND the row at that position is still the anchor row;
// the id alone survives a VACUUM that renumbers rowids, the anchor alone could
// coincide in a rehydrated database. A cursor with no position (never pushed,
// or saved by a created_at-only binary) already starts from the top. Reports
// whether a saved position was dropped.
func validateLedgerCursor(ctx context.Context, a *app.App, cursor hosted.WorkstateCursor) (hosted.WorkstateCursor, bool, error) {
	if cursor.LedgerSeq <= 0 {
		return cursor, false, nil
	}
	storeID, err := a.Store.Ledger.InstanceID(ctx)
	if err != nil {
		return cursor, false, err
	}
	valid := cursor.LedgerStoreID == storeID
	if valid {
		id, ok, ierr := a.Store.Ledger.IDAtSeq(ctx, cursor.LedgerSeq)
		if ierr != nil {
			return cursor, false, ierr
		}
		valid = ok && id == cursor.LedgerAnchorID
	}
	if valid {
		return cursor, false, nil
	}
	cursor.LedgerCreatedAt = time.Time{}
	cursor.LedgerSeq = 0
	cursor.LedgerAnchorID = ""
	cursor.LedgerStoreID = ""
	return cursor, true, nil
}

// collectWorkstateSince builds the ingest batch for records past the cursor,
// paging to exhaustion (sty_88e83180). Work items page on updated_at; the ledger
// pages on insertion order, because a row can commit with a created_at behind a
// row already pushed (sty_4a31e1ed). Returns the batch plus the marks observed
// (seeded from the cursor so empty areas never rewind it).
func collectWorkstateSince(ctx context.Context, a *app.App, optIn map[string]bool, cursor hosted.WorkstateCursor) (hosted.WorkstateIngest, time.Time, ledgerMark, error) {
	batch, maxItems, mark, _, err := collectWorkstateRows(ctx, a, optIn, cursor, nil)
	return batch, maxItems, mark, err
}

// collectWorkstateRows is collectWorkstateSince plus, for each collected ledger
// row in batch order, who owns it and where it sits — what the unpushed-code
// hold needs to cut the ledger cursor ahead of a held row (sty_7361569a).
//
// A non-nil scope keeps only the items whose own id it names and the ledger rows
// of the stories it names (sty_991b6900); nil collects everything past the cursor.
func collectWorkstateRows(ctx context.Context, a *app.App, optIn map[string]bool, cursor hosted.WorkstateCursor, scope scopeSet) (hosted.WorkstateIngest, time.Time, ledgerMark, []ledgerRowMeta, error) {
	var rows []ledgerRowMeta
	var batch hosted.WorkstateIngest
	maxItems := cursor.ItemsUpdatedAt
	maxLedger := ledgerMark{
		createdAt: cursor.LedgerCreatedAt,
		seq:       cursor.LedgerSeq,
		anchorID:  cursor.LedgerAnchorID,
		storeID:   cursor.LedgerStoreID,
	}

	pageItems := func(kind workitem.Kind) error {
		offset := 0
		for {
			items, err := a.Store.Stories.ListChangedSince(ctx, kind, cursor.ItemsUpdatedAt, workstateItemChunk, offset)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				break
			}
			for _, it := range items {
				if !scope.HasItem(it.ID) {
					continue
				}
				raw, merr := marshalWorkstateItem(it)
				if merr != nil {
					return merr
				}
				batch.Items = append(batch.Items, raw)
				if it.UpdatedAt.After(maxItems) {
					maxItems = it.UpdatedAt
				}
			}
			if len(items) < workstateItemChunk {
				break
			}
			offset += len(items)
		}
		return nil
	}

	if optIn["stories"] {
		if err := pageItems(workitem.KindStory); err != nil {
			return batch, maxItems, maxLedger, rows, fmt.Errorf("list stories: %w", err)
		}
	}
	if optIn["executions"] {
		if err := pageItems(workitem.KindExecution); err != nil {
			return batch, maxItems, maxLedger, rows, fmt.Errorf("list executions: %w", err)
		}
	}
	if optIn["ledger"] {
		storeID, err := a.Store.Ledger.InstanceID(ctx)
		if err != nil {
			return batch, maxItems, maxLedger, rows, fmt.Errorf("list ledger: %w", err)
		}
		maxLedger.storeID = storeID
		for {
			entries, err := a.Store.Ledger.ListInsertedAfter(ctx, maxLedger.seq, workstateLedgerChunk)
			if err != nil {
				return batch, maxItems, maxLedger, rows, fmt.Errorf("list ledger: %w", err)
			}
			if len(entries) == 0 {
				break
			}
			for _, e := range entries {
				maxLedger.seq = e.Seq // the page cursor: advances past rows the scope skips
				if !scope.HasLedger(e.StoryID) {
					continue
				}
				raw, merr := marshalWorkstateLedger(e.Entry)
				if merr != nil {
					return batch, maxItems, maxLedger, rows, merr
				}
				batch.Ledger = append(batch.Ledger, raw)
				rows = append(rows, ledgerRowMeta{storyID: e.StoryID, id: e.ID, seq: e.Seq, createdAt: e.CreatedAt})
				if e.CreatedAt.After(maxLedger.createdAt) {
					maxLedger.createdAt = e.CreatedAt
				}
				maxLedger.anchorID = e.ID
			}
			if len(entries) < workstateLedgerChunk {
				break
			}
		}
	}
	if batch.Items == nil {
		batch.Items = []json.RawMessage{}
	}
	if batch.Ledger == nil {
		batch.Ledger = []json.RawMessage{}
	}
	return batch, maxItems, maxLedger, rows, nil
}

// dryRunHolds lists the stories a real push would hold, from the same live git
// check. The cursor is read when the repo is bound so the list is what a push
// would actually consider; otherwise every story is checked.
func dryRunHolds(cmd *cobra.Command, a *app.App, optIn map[string]bool, out io.Writer, server string) error {
	on, err := config.HoldUnpushed(a.Config)
	if err != nil {
		return err
	}
	if !on {
		fmt.Fprintln(out, "Hold is off ([sync] hold_unpushed = false) — no story would be held.")
		return nil
	}
	var cursor hosted.WorkstateCursor
	if project, perr := resolveBoundProject(a.Config, a.RepoRoot); perr == nil {
		if c, lerr := hosted.LoadWorkstateCursor(server, project, a.RepoRoot); lerr == nil {
			cursor = c
		}
	}
	if optIn["ledger"] {
		if cursor, _, err = validateLedgerCursor(cmd.Context(), a, cursor); err != nil {
			return err
		}
	}
	batch, _, _, rows, err := collectWorkstateRows(cmd.Context(), a, optIn, cursor, nil)
	if err != nil {
		return err
	}
	batch.Items = filterItemsAfter(batch.Items, cursor.ItemsUpdatedAt)
	res, err := classifyHolds(cmd.Context(), a, batch.Items, rows)
	if err != nil {
		return err
	}
	if len(res.held) == 0 && len(res.unavailable) == 0 {
		fmt.Fprintln(out, "No story would be held.")
		return nil
	}
	for _, l := range res.lines(true) {
		fmt.Fprintln(out, l)
	}
	return nil
}

// marshalWorkstateItem encodes a work item with the promoted fields the server
// extracts (id, kind, status, title, updated_at) plus the full record.
func marshalWorkstateItem(it workitem.Item) (json.RawMessage, error) {
	w := workstateItemWire{
		ID: it.ID, Kind: string(it.Kind), Status: it.Status, Title: it.Title,
		Body: it.Body, Priority: it.Priority, Category: it.Category, ParentID: it.ParentID,
		AcceptanceCriteria: it.AcceptanceCriteria, Tags: it.Tags,
		UpdatedAt: it.UpdatedAt, CreatedAt: it.CreatedAt, Archived: it.Archived,
		ParkOrigin: it.ParkOrigin, InputsDecl: it.Inputs, Inputs: it.InputValues,
	}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("encode workstate item %s: %w", it.ID, err)
	}
	return b, nil
}

func rawItemID(raw json.RawMessage) string {
	var w struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &w)
	return w.ID
}

func maxItemUpdatedAt(items []json.RawMessage) time.Time {
	var max time.Time
	for _, raw := range items {
		var w struct {
			UpdatedAt time.Time `json:"updated_at"`
		}
		_ = json.Unmarshal(raw, &w)
		if w.UpdatedAt.After(max) {
			max = w.UpdatedAt
		}
	}
	return max
}

// marshalWorkstateLedger encodes a ledger entry for ingest.
func marshalWorkstateLedger(e ledger.Entry) (json.RawMessage, error) {
	type wire struct {
		ID        string          `json:"id"`
		StoryID   string          `json:"story_id,omitempty"`
		ProjectID string          `json:"project_id,omitempty"`
		Kind      string          `json:"kind"`
		Actor     string          `json:"actor,omitempty"`
		Body      string          `json:"body,omitempty"`
		Payload   json.RawMessage `json:"payload,omitempty"`
		Refs      json.RawMessage `json:"refs,omitempty"`
		CreatedAt time.Time       `json:"created_at"`
		// BaseID and WorkflowHash ride along so the chain a machine wrote is the
		// chain another machine pulls (sty_41f61669).
		BaseID       string `json:"base_id,omitempty"`
		WorkflowHash string `json:"workflow_hash,omitempty"`
	}
	w := wire{
		ID: e.ID, StoryID: e.StoryID, ProjectID: e.ProjectID, Kind: e.Kind, Actor: e.Actor,
		Body: e.Body, Payload: e.Payload, Refs: e.Refs, CreatedAt: e.CreatedAt,
		BaseID: e.BaseID, WorkflowHash: e.WorkflowHash,
	}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("encode workstate ledger %s: %w", e.ID, err)
	}
	return b, nil
}
