package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func newSyncWorkstateRechainCmd() *cobra.Command {
	var server string
	var dryRun, conflicted bool
	cmd := &cobra.Command{
		Use:         "rechain [story-id...]",
		Short:       "Put a story forked from its hosted chain onto that chain",
		Annotations: hostedOrigin(needsStore()),
		Args:        cobra.ArbitraryArgs,
		Long: `rechain puts a story forked from its hosted chain back on it, moves its push
head to the hosted latest and clears its conflict. It never pushes.

Seeded events and a status_reconcile to the hosted status are dropped. Local work
on a root of its own is replayed after the hosted latest. Local work that forks
from the hosted chain is merged after it in event time, none before its base, with
a chain_merge record; a status move invalid at its merged place is excluded and
named. A story with no valid merge is refused by name, untouched.

With no ids it takes every story in conflict or differing from its hosted chain;
--conflicted takes only the stories recorded in conflict.
--dry-run applies the same checks and changes nothing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if conflicted && len(args) > 0 {
				return fmt.Errorf("--conflicted takes no story ids")
			}
			return runSyncWorkstateRechain(cmd, server, dryRun, conflicted, args)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report each story as it would be repaired or refused without changing anything.")
	cmd.Flags().BoolVar(&conflicted, "conflicted", false, "Take only the stories recorded as in conflict, not every story that differs from its hosted chain.")
	return cmd
}

func runSyncWorkstateRechain(cmd *cobra.Command, serverArg string, dryRun, conflictedOnly bool, ids []string) error {
	a, err := appFrom(cmd)
	if err != nil {
		return err
	}
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	srv := resolveServer(serverArg)
	if srv == "" {
		return fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}
	project, err := resolveBoundProject(a.Config, a.RepoRoot)
	if err != nil {
		return err
	}
	chain := newChainPull(newHostedClient(ctx, srv, a.RepoRoot), srv, project, a.RepoRoot, cmd.ErrOrStderr())
	defer chain.close()
	rd, err := chain.reader(ctx)
	if err != nil {
		return err
	}
	cases, err := rechainCases(ctx, a, chain, rd, ids, conflictedOnly)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		fmt.Fprintln(out, "No story is recorded as in conflict or differs from its hosted chain; name story ids to rechain.")
		return nil
	}
	word := func(real, dry string) string {
		if dryRun {
			return dry
		}
		return real
	}
	failed := 0
	now := time.Now().UTC()
	for _, c := range cases {
		switch c.plan.outcome {
		case rechainRefused:
			fmt.Fprintf(out, "%s %s: %s\n", word("refused", "would refuse"), c.id, c.plan.reason)
			failed++
			continue
		case rechainNothing:
			fmt.Fprintf(out, "nothing to rechain %s: the local chain is the hosted chain\n", c.id)
			continue
		}
		if !dryRun {
			if err := c.apply(ctx, a, chain, now); err != nil {
				fmt.Fprintf(out, "refused %s: %v\n", c.id, err)
				failed++
				continue
			}
		}
		replayed := ""
		if n := len(c.plan.replay); n > 0 {
			replayed = fmt.Sprintf(", replay %d local event(s)", n)
		}
		if c.plan.merging {
			replayed += fmt.Sprintf(", merge %d local event(s), exclude %d", len(c.plan.merge), len(c.plan.excluded))
		}
		fmt.Fprintf(out, "%s %s: remove %d event(s) (%d seeded, %d reconcile), take %d hosted event(s)%s\n",
			word("rechained", "would rechain"), c.id, len(c.plan.remove)+len(c.plan.retake), c.plan.seeded, len(c.plan.reconciled), len(c.plan.take), replayed)
		for _, x := range c.plan.excluded {
			fmt.Fprintf(out, "  %s %s (%s → %s): invalid after %s\n", word("excluded", "would exclude"), x.ID, x.fromLabel(), x.To, x.after)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d stor(y/ies) not rechained — see above", failed)
	}
	return nil
}

// rechainCase is one story rechain reports: its two chains and what putting the
// local one on the hosted one does.
type rechainCase struct {
	id      string
	local   []ledger.Entry
	events  []hosted.ChainEvent
	latest  string
	readErr bool // a chain could not be read, so the story is refused rather than skipped
	plan    rechainPlan
}

// rechainCases reads and judges every story to report, in id order. Named ids are
// all reported; with none, the stories recorded as in conflict are, and so is every
// local story whose chain differs from its hosted one. conflictedOnly takes the
// recorded conflicts alone: satelled's run after a push, never a sweep.
func rechainCases(ctx context.Context, a *app.App, chain *chainPull, rd chainReader, ids []string, conflictedOnly bool) ([]rechainCase, error) {
	discover := len(ids) == 0
	conflicted := map[string]bool{}
	if discover {
		st, _, err := syncstate.Load(config.GlobalDir(), a.RepoRoot)
		if err != nil {
			return nil, err
		}
		for id := range st.StoryConflicts {
			conflicted[id] = true
		}
		if conflictedOnly {
			ids = slices.Collect(maps.Keys(conflicted))
		} else if ids, err = a.Store.Ledger.StoryIDs(ctx); err != nil {
			return nil, fmt.Errorf("list story chains: %w", err)
		}
		for id := range conflicted {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	var cases []rechainCase
	for i, id := range slices.Sorted(slices.Values(ids)) {
		if i > 0 && !pause(ctx, appendPace) {
			return nil, ctx.Err()
		}
		c := readRechainCase(ctx, a, chain, rd, id)
		if discover && !conflicted[id] && !c.readErr && !divergent(c.local, c.events) {
			continue
		}
		cases = append(cases, c)
	}
	return cases, nil
}

// readRechainCase reads a story's local and hosted chains and judges them. A story
// that cannot be read is a refused case, never a skipped one.
func readRechainCase(ctx context.Context, a *app.App, chain *chainPull, rd chainReader, id string) rechainCase {
	c := rechainCase{id: id}
	refuse := func(readErr bool, format string, args ...any) rechainCase {
		c.readErr = readErr
		c.plan = rechainPlan{outcome: rechainRefused, reason: fmt.Sprintf(format, args...)}
		return c
	}
	local, err := a.Store.Ledger.StoryChain(ctx, id)
	if err != nil {
		return refuse(true, "read the local chain: %v", err)
	}
	events, latest, err := rd.ReadStory(ctx, chain.project, id, "")
	if err != nil {
		return refuse(true, "read the hosted chain: %v", err)
	}
	c.local, c.events, c.latest = local, events, latest
	if len(local) == 0 {
		return refuse(false, "no local events")
	}
	if latest == "" || len(events) == 0 {
		return refuse(false, "the hosted project holds no events of it")
	}
	c.plan = planRechain(local, events, func() (string, error) { return hostedStatus(ctx, a, id, events) }, func() string { return recordedStatus(ctx, a, id) })
	return c
}

// recordedStatus is the story's recorded status; empty when it has no row.
func recordedStatus(ctx context.Context, a *app.App, id string) string {
	row, err := a.Store.Stories.Get(ctx, id)
	if err != nil {
		return ""
	}
	return row.Status
}

const (
	rechainDo      = "rechain"
	rechainNothing = "nothing"
	rechainRefused = "refused"
)

// rechainPlan is what rechain does to one story, decided before anything changes.
type rechainPlan struct {
	outcome string
	reason  string              // why it is refused
	remove  []string            // local events to remove, in chain order
	seeded  int                 // of remove, the ones that are a seeded genesis
	take    []hosted.ChainEvent // hosted events to take, in chain order
	replay  []string            // local work to put after the hosted latest, in chain order
	retake  []string            // local copies of hosted events, off the hosted chain, the replay takes again

	merging  bool           // a fork planMerge merges
	merge    []string       // local-only events to put after the hosted latest, in merged order
	excluded []excludedMove // local status moves the merge leaves out of the chain
	dropped  []string       // of remove, the seeded snapshots the merge drops
	forkBase string         // the earliest hosted event local work builds on

	reconciled map[string]bool // of remove, the status_reconcile events whose status the hosted chain folds to
}

// hostedStatus is the status the hosted chain folds to, by the fold the local one
// uses: the story's row picks the route, and a hosted event that cannot be decoded
// is an error, never a guess.
func hostedStatus(ctx context.Context, a *app.App, id string, events []hosted.ChainEvent) (string, error) {
	entries := make([]ledger.Entry, 0, len(events))
	for _, ev := range events {
		e, err := ledgerEntryOf(id, ev)
		if err != nil {
			return "", fmt.Errorf("decode hosted event %s: %w", ev.ID, err)
		}
		entries = append(entries, e)
	}
	row, err := a.Store.Stories.Get(ctx, id)
	if err != nil {
		row = workitem.Item{ID: id}
	}
	return verb.FoldChain(ctx, row, entries).Status, nil
}

// reconciledStatus is the status a status_reconcile event set: the chain value
// "story reconcile --repair" rebuilt the row to. ok is false for any other event,
// and for a reconcile that repaired no status.
func reconciledStatus(e ledger.Entry) (status string, ok bool) {
	if e.Kind != ledger.KindStatusReconcile {
		return "", false
	}
	var p struct {
		Fields []struct {
			Field string `json:"field"`
			Chain string `json:"chain"`
		} `json:"fields"`
	}
	if json.Unmarshal(e.Payload, &p) != nil {
		return "", false
	}
	for _, f := range p.Fields {
		if f.Field == storyfold.FieldStatus {
			return f.Chain, true
		}
	}
	return "", false
}

// planRechain decides what putting a local chain on its hosted chain does, and
// whether it can be done: the dry run and the real run both report it.
//
// The local chain keeps its longest start the hosted chain has event for event
// (same id, same base, same place) and gives up everything after it, so the events
// removed are closed under the events built on them. Each must be a seeded event, a
// status_reconcile that set the status the hosted chain folds to (hostedStatus folds
// it, and is called only when one is there), or one the hosted chain holds. A
// status_reconcile that set another status is refused with both statuses; any other
// event is local work and the story is refused.
//
// A local chain that forks in history the hosted chain holds, from legacy
// concurrent writes, is taken in the hosted chain's order (heldOrder,
// sty_6209b64c). It is repaired only when the hosted chain folds to the story's
// recorded status (rowStatus), so the repair moves no status, and the local copy
// of the hosted latest is taken again when it is not the last event held here,
// so the next local append builds on it.
func planRechain(local []ledger.Entry, events []hosted.ChainEvent, hostedStatus func() (string, error), rowStatus func() string) rechainPlan {
	refuse := func(format string, args ...any) rechainPlan {
		return rechainPlan{outcome: rechainRefused, reason: fmt.Sprintf(format, args...)}
	}
	if plan, ok := planReplay(local, events, hostedStatus); ok {
		return plan
	}
	if plan, ok := planMerge(local, events, hostedStatus); ok {
		return plan
	}
	_, linear := chainOrder(local)
	forked := linear != nil
	order, err := heldOrder(local, events)
	if err != nil {
		return refuse("%v", err)
	}
	onHosted := make(map[string]bool, len(events))
	for _, e := range events {
		onHosted[e.ID] = true
	}
	localOnly, reconciled := map[string]bool{}, map[string]bool{} // the seeded-or-work events the hosted chain lacks; the reconciles it lacks that set its status
	var lacking, work []string                                    // the events the hosted chain lacks; of those, the ones that are not seeded
	var folded string
	if forked {
		if folded, err = hostedStatus(); err != nil {
			return refuse("the hosted chain cannot be folded: %v", err)
		}
		if row := rowStatus(); folded != row {
			return refuse("%v, and the hosted chain folds to %s but the story's status is %s", linear, folded, row)
		}
	}
	for _, e := range order {
		if onHosted[e.ID] {
			continue
		}
		named := fmt.Sprintf("%s (%s)", e.ID, e.Kind)
		lacking = append(lacking, named)
		if status, ok := reconciledStatus(e); ok {
			if folded == "" {
				if folded, err = hostedStatus(); err != nil {
					return refuse("the hosted chain cannot be folded: %v", err)
				}
			}
			if status != folded {
				return refuse("local status_reconcile %s sets status %s but the hosted chain folds to %s", e.ID, status, folded)
			}
			reconciled[e.ID] = true
			continue
		}
		localOnly[e.ID] = true
		if !storyfold.IsSeededEvent(e) {
			work = append(work, named)
		}
	}
	if len(localOnly) > 0 {
		if _, ok := storyfold.SeededEvents(order, localOnly); !ok && !droppedVersions(order, events, localOnly, reconciled) {
			if len(work) == 0 {
				work = lacking
			}
			return refuse("local events the hosted chain lacks are not only a seeded genesis: %s", strings.Join(work, ", "))
		}
	}
	n := matchingPrefix(order, events)
	take, err := takePlan(order[:n], events)
	if err != nil {
		return refuse("the hosted chain cannot be taken: %v", err)
	}
	plan := rechainPlan{outcome: rechainDo, take: take, reconciled: reconciled}
	for _, e := range order[n:] {
		plan.remove = append(plan.remove, e.ID)
		if localOnly[e.ID] {
			plan.seeded++
		}
	}
	if latest := events[len(events)-1]; forked && len(plan.take) == 0 && lastKept(local, plan.remove) != latest.ID {
		plan.remove = append(plan.remove, latest.ID)
		plan.take = append(plan.take, latest)
	}
	if len(plan.remove) == 0 && len(plan.take) == 0 {
		plan.outcome = rechainNothing
	}
	return plan
}

// droppedVersions reports whether the local-only events are only workflow_version
// events whose hash no event that stays carries, a hosted one included: the
// version the ledger put ahead of a status_reconcile the repair drops, on a story
// whose seeded snapshot the hosted chain already holds (sty_6209b64c). Dropping
// one loses no version an event that stays names.
func droppedVersions(order []ledger.Entry, events []hosted.ChainEvent, localOnly, reconciled map[string]bool) bool {
	dropped := map[string]bool{}
	for _, e := range order {
		if !localOnly[e.ID] {
			continue
		}
		if !ledger.IsVersionEvent(e) || e.WorkflowHash == "" {
			return false
		}
		dropped[e.WorkflowHash] = true
	}
	for _, e := range order {
		if !localOnly[e.ID] && !reconciled[e.ID] && dropped[e.WorkflowHash] {
			return false
		}
	}
	return len(dropped) > 0 && !slices.ContainsFunc(events, func(ev hosted.ChainEvent) bool { return dropped[ev.WorkflowVersion] })
}

// lastKept is the last event of local, in insertion order, that remove leaves:
// the one the ledger builds the next local append on.
func lastKept(local []ledger.Entry, remove []string) string {
	for i := len(local) - 1; i >= 0; i-- {
		if !slices.Contains(remove, local[i].ID) {
			return local[i].ID
		}
	}
	return ""
}

// planReplay decides a story whose local-only events are real work on a root of
// their own: a write made after this checkout left the story's history on the
// server started a new chain instead of building on the hosted head
// (sty_00212637). ok is false for any other shape, which planRechain judges as
// before.
//
// The local copies of hosted events are kept while they are a start of the hosted
// chain, id and base; the rest are retaken from the server — a fetch after the
// fork linked the hosted root onto the local work. The local-only events must be
// one chain from their own root, and are replayed after the hosted latest in that
// order, a workflow_version included: only a seeded definition snapshot, SeedItem's
// copy of a definition the hosted chain carries, is dropped. A replayed status move
// must start from the status the hosted chain folds to, when it folds to one;
// otherwise the story moved elsewhere and is refused with both statuses.
func planReplay(local []ledger.Entry, events []hosted.ChainEvent, hostedStatus func() (string, error)) (rechainPlan, bool) {
	refuse := func(format string, args ...any) (rechainPlan, bool) {
		return rechainPlan{outcome: rechainRefused, reason: fmt.Sprintf(format, args...)}, true
	}
	onHosted := make(map[string]bool, len(events))
	for _, e := range events {
		onHosted[e.ID] = true
	}
	held := map[string]ledger.Entry{}
	var own []ledger.Entry
	for _, e := range local {
		if onHosted[e.ID] {
			held[e.ID] = e
		} else {
			own = append(own, e)
		}
	}
	if onlyStubEvents(own) {
		return rechainPlan{}, false
	}
	ownOrder, err := chainOrder(own)
	if err != nil {
		return rechainPlan{}, false // not a root of its own: built on a hosted event, or forked
	}
	kept, retake := hostedStart(held, events)
	take, err := takePlan(kept, events)
	if err != nil {
		return refuse("the hosted chain cannot be taken: %v", err)
	}
	plan := rechainPlan{outcome: rechainDo, take: take, retake: retake}
	guarded := false
	for _, e := range ownOrder {
		if isSeededSnapshot(e) {
			plan.remove = append(plan.remove, e.ID)
			plan.seeded++
			continue
		}
		if e.Kind == ledger.KindStatusTransition && !guarded {
			guarded = true
			folded, err := hostedStatus()
			if err != nil {
				return refuse("the hosted chain cannot be folded: %v", err)
			}
			// A hosted chain that folds to no status holds no status event: nothing
			// moved the item from its start, so the local move's own from is that
			// start (sty_937fd6cc — an execution or a task carries no status in its
			// creation). It is not checked against a workflow's start state: the
			// empty fold proves only that nobody moved it.
			if from := storyfold.TransitionFrom(e); folded != "" && from != folded {
				return refuse("local status_transition %s moves from %s but the hosted chain folds to %s", e.ID, from, folded)
			}
		}
		plan.replay = append(plan.replay, e.ID)
	}
	return plan, true
}

// onlyStubEvents reports whether a story's local-only events are only the shapes
// planRechain repairs by dropping them, seeded events and reconciles; anything
// else among them is work, which a replay or a merge keeps.
func onlyStubEvents(own []ledger.Entry) bool {
	return !slices.ContainsFunc(own, func(e ledger.Entry) bool { return !isStub(e) })
}

func isStub(e ledger.Entry) bool {
	return storyfold.IsSeededEvent(e) || e.Kind == ledger.KindStatusReconcile
}

// isSeededSnapshot reports SeedItem's copy of a definition the hosted chain
// carries: the one local-only event a replay or a merge drops. A workflow_version
// is work, never dropped by either.
func isSeededSnapshot(e ledger.Entry) bool {
	return e.Kind == ledger.KindDefinitionSnapshot && storyfold.IsSeededEvent(e)
}

// hostedStart splits the local copies of hosted events into the ones kept, a start
// of the hosted chain id and base, and the ones after it, which are retaken from
// the server with the bases the hosted chain gives them.
func hostedStart(held map[string]ledger.Entry, events []hosted.ChainEvent) (kept []ledger.Entry, retake []string) {
	for _, ev := range events {
		e, ok := held[ev.ID]
		if !ok || e.BaseID != ev.BaseID {
			break
		}
		kept = append(kept, e)
	}
	for _, ev := range events[len(kept):] {
		if _, ok := held[ev.ID]; ok {
			retake = append(retake, ev.ID)
		}
	}
	return kept, retake
}

// excludedMove is a local status move a merge leaves out: invalid after the
// status the merged chain folds to at its place.
type excludedMove struct {
	ID    string `json:"id"`
	From  string `json:"from,omitempty"` // empty for a status_reconcile, which names no from
	To    string `json:"to"`
	after string
}

func (x excludedMove) fromLabel() string {
	if x.From == "" {
		return "reconcile"
	}
	return x.From
}

// chainMergeRecord is the payload of the chain_merge event a merge ends with.
type chainMergeRecord struct {
	ForkBase     string         `json:"fork_base"`
	HostedLatest string         `json:"hosted_latest"`
	Merged       []string       `json:"merged"`
	Excluded     []excludedMove `json:"excluded"`
	Dropped      []string       `json:"dropped"`
}

// planMerge decides a story whose local chain forks from its hosted chain: local
// work built on a hosted event, beside hosted events built on it differently
// (sty_f321c29e). ok is false for any other shape: a local side of only seeded
// events and reconciles, which planRechain drops, and a local chain that is the
// hosted chain and then local events, which a push publishes.
//
// The server is append-only, so the merged chain is the hosted chain as it stands
// and then every local-only event, from any number of local branches, in event
// time, none before the event it builds on, ties in local order. Only a seeded
// definition snapshot is dropped. A status move whose from is not the status
// folded so far is excluded, and so is a status_reconcile to another status; one
// to the folded status stays, a no-op. Every check is made here, before anything
// is written: a base neither side holds, a cycle, an event both sides hold with
// different content, or a hosted event that cannot be decoded refuses the story.
func planMerge(local []ledger.Entry, events []hosted.ChainEvent, hostedStatus func() (string, error)) (rechainPlan, bool) {
	refuse := func(format string, args ...any) (rechainPlan, bool) {
		return rechainPlan{outcome: rechainRefused, reason: fmt.Sprintf(format, args...)}, true
	}
	onHosted := make(map[string]int, len(events))
	for i, e := range events {
		onHosted[e.ID] = i
	}
	held, own := map[string]ledger.Entry{}, map[string]ledger.Entry{}
	var ownOrder []ledger.Entry // local insert order, which breaks a tie in event time
	for _, e := range local {
		if _, ok := onHosted[e.ID]; ok {
			held[e.ID] = e
		} else {
			own[e.ID] = e
			ownOrder = append(ownOrder, e)
		}
	}
	if onlyStubEvents(ownOrder) {
		return rechainPlan{}, false
	}
	if order, err := chainOrder(local); err == nil && matchingPrefix(order, events) == len(events) {
		return rechainPlan{}, false
	}
	if !slices.ContainsFunc(ownOrder, func(e ledger.Entry) bool { return !isStub(e) && buildsOnHosted(e, own, onHosted) }) {
		return rechainPlan{}, false
	}

	storyID := ownOrder[0].StoryID
	for _, ev := range events {
		h, err := ledgerEntryOf(storyID, ev)
		if err != nil {
			return refuse("decode hosted event %s: %v", ev.ID, err)
		}
		if l, ok := held[ev.ID]; ok && !sameContent(l, h) {
			return refuse("%s is on both sides with different content", ev.ID)
		}
	}
	forkAt := -1
	for _, e := range ownOrder {
		if i, ok := onHosted[e.BaseID]; ok {
			if forkAt < 0 || i < forkAt {
				forkAt = i
			}
			continue
		}
		if _, ok := own[e.BaseID]; !ok && e.BaseID != "" {
			return refuse("%s builds on %s, which neither side holds", e.ID, e.BaseID)
		}
	}

	merged := mergeOrder(ownOrder, own)
	if len(merged) != len(ownOrder) {
		placed := map[string]bool{}
		for _, e := range merged {
			placed[e.ID] = true
		}
		var left []string
		for _, e := range ownOrder {
			if !placed[e.ID] {
				left = append(left, e.ID)
			}
		}
		return refuse("local events %s form a cycle", strings.Join(left, ", "))
	}

	kept, retake := hostedStart(held, events)
	take, err := takePlan(kept, events)
	if err != nil {
		return refuse("the hosted chain cannot be taken: %v", err)
	}
	plan := rechainPlan{outcome: rechainDo, take: take, retake: retake, merging: true, forkBase: events[forkAt].ID}
	status, folded := "", false
	for _, e := range merged {
		if isSeededSnapshot(e) {
			plan.remove = append(plan.remove, e.ID)
			plan.dropped = append(plan.dropped, e.ID)
			plan.seeded++
			continue
		}
		to, reconciled := reconciledStatus(e)
		if e.Kind == ledger.KindStatusTransition || reconciled {
			if !folded {
				if status, err = hostedStatus(); err != nil {
					return refuse("the hosted chain cannot be folded: %v", err)
				}
				folded = true
			}
			from := ""
			if !reconciled {
				from, to = storyfold.TransitionFrom(e), storyfold.TransitionTo(e)
			}
			// An empty fold is the item's start: nothing moved it, so any move is
			// valid from it, as a replay judges (sty_937fd6cc).
			if status != "" && ((!reconciled && from != status) || (reconciled && to != status)) {
				plan.excluded = append(plan.excluded, excludedMove{ID: e.ID, From: from, To: to, after: status})
				continue
			}
			status = to
		}
		plan.merge = append(plan.merge, e.ID)
	}
	return plan, true
}

// buildsOnHosted reports whether e, through local-only events, builds on a hosted
// event.
func buildsOnHosted(e ledger.Entry, own map[string]ledger.Entry, onHosted map[string]int) bool {
	seen := map[string]bool{}
	for !seen[e.ID] {
		seen[e.ID] = true
		if _, ok := onHosted[e.BaseID]; ok {
			return true
		}
		next, ok := own[e.BaseID]
		if !ok {
			return false
		}
		e = next
	}
	return false
}

// mergeOrder puts the local-only events in event time, each after the event it
// builds on: an event is ready once its base is placed (or is not local-only), and
// the ready event with the least time goes next, a tie to the earlier in local
// order. An event of a cycle never becomes ready and is left out.
func mergeOrder(ownOrder []ledger.Entry, own map[string]ledger.Entry) []ledger.Entry {
	pos := make(map[string]int, len(ownOrder))
	children := map[string][]ledger.Entry{}
	var ready []ledger.Entry
	for i, e := range ownOrder {
		pos[e.ID] = i
		if _, ok := own[e.BaseID]; ok {
			children[e.BaseID] = append(children[e.BaseID], e)
		} else {
			ready = append(ready, e)
		}
	}
	var merged []ledger.Entry
	for len(ready) > 0 {
		next := 0
		for i, e := range ready {
			if best := ready[next]; e.CreatedAt.Before(best.CreatedAt) || (e.CreatedAt.Equal(best.CreatedAt) && pos[e.ID] < pos[best.ID]) {
				next = i
			}
		}
		e := ready[next]
		ready = slices.Delete(ready, next, next+1)
		merged = append(merged, e)
		ready = append(ready, children[e.ID]...)
	}
	return merged
}

// sameContent reports whether a local copy of a hosted event is the same event:
// kind, body and payload. Its base is the chain's, judged apart.
func sameContent(l, h ledger.Entry) bool {
	return l.Kind == h.Kind && l.Body == h.Body && compactJSON(l.Payload) == compactJSON(h.Payload)
}

func compactJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "{}"
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// matchingPrefix counts the events a local chain in chain order and a hosted chain
// share from their start: same id, same base.
func matchingPrefix(order []ledger.Entry, events []hosted.ChainEvent) int {
	n := 0
	for n < len(order) && n < len(events) && order[n].ID == events[n].ID && order[n].BaseID == events[n].BaseID {
		n++
	}
	return n
}

// divergent reports whether a story's local and hosted chains disagree at a place
// both have: the fork a pull left. A story the hosted project holds nothing of, one
// that is the hosted chain and then local events, and one that is only a start of
// the hosted chain are not: a push or a pull is what moves them. A fork in held
// history the hosted chain holds the same way is the hosted chain's own
// (heldOrder); local events past it are still one line from the hosted latest.
func divergent(local []ledger.Entry, events []hosted.ChainEvent) bool {
	if len(events) == 0 || len(local) == 0 {
		return false
	}
	order, err := heldOrder(local, events)
	if err != nil {
		return true
	}
	n := matchingPrefix(order, events)
	if n < min(len(order), len(events)) {
		return true
	}
	for i := max(n, 1); i < len(order); i++ {
		if order[i].BaseID != order[i-1].ID {
			return true
		}
	}
	return false
}

// apply puts the story on its hosted chain, then moves its head to the hosted
// latest and clears its conflict. planRechain made every check; a re-run after a
// failure part way finds a start of the hosted chain and takes the rest.
func (c rechainCase) apply(ctx context.Context, a *app.App, chain *chainPull, now time.Time) error {
	if c.plan.merging {
		return c.applyMerge(ctx, a, chain, now)
	}
	if len(c.plan.replay) > 0 {
		return c.applyReplay(ctx, a, chain, now)
	}
	onHosted := make(map[string]bool, len(c.events))
	for _, e := range c.events {
		onHosted[e.ID] = true
	}
	if len(c.plan.remove) > 0 {
		guard := func(e ledger.Entry) bool {
			return storyfold.IsSeededEvent(e) || c.plan.reconciled[e.ID] || onHosted[e.ID]
		}
		if err := a.Store.Ledger.RemoveEvents(ctx, c.id, c.plan.remove, guard); err != nil {
			return err
		}
	}
	if err := takeEvents(ctx, a, c.id, c.plan.take, now); err != nil {
		return err
	}
	local, err := a.Store.Ledger.StoryChain(ctx, c.id)
	if err != nil {
		return fmt.Errorf("read the local chain: %w", err)
	}
	order, err := heldOrder(local, c.events)
	if err != nil {
		return err
	}
	if len(order) != len(c.events) || matchingPrefix(order, c.events) != len(c.events) {
		return fmt.Errorf("the local chain is not the hosted chain after taking it")
	}
	return c.settle(ctx, a, chain, now)
}

// applyReplay drops the local copies of hosted events that are off the hosted
// chain, writes the hosted events the store lacks with the bases they carry and
// the local work after the hosted latest in its order, in one transaction, then
// drops any seeded snapshot planReplay named. The head stays at the hosted
// latest, so the next push sends the replayed events.
func (c rechainCase) applyReplay(ctx context.Context, a *app.App, chain *chainPull, now time.Time) error {
	onHosted := make(map[string]bool, len(c.events))
	for _, e := range c.events {
		onHosted[e.ID] = true
	}
	if len(c.plan.retake) > 0 {
		if err := a.Store.Ledger.RemoveEvents(ctx, c.id, c.plan.retake, func(e ledger.Entry) bool { return onHosted[e.ID] }); err != nil {
			return err
		}
	}
	taken := make([]ledger.Entry, 0, len(c.plan.take))
	for _, ev := range c.plan.take {
		e, err := ledgerEntryOf(c.id, ev)
		if err != nil {
			return fmt.Errorf("decode the hosted event %s of %s: %w", ev.ID, c.id, err)
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		taken = append(taken, e)
	}
	if err := a.Store.Ledger.RebaseEvents(ctx, c.id, taken, c.plan.replay, c.latest); err != nil {
		return err
	}
	if len(c.plan.remove) > 0 {
		if err := a.Store.Ledger.RemoveEvents(ctx, c.id, c.plan.remove, storyfold.IsSeededEvent); err != nil {
			return err
		}
	}
	local, err := a.Store.Ledger.StoryChain(ctx, c.id)
	if err != nil {
		return fmt.Errorf("read the local chain: %w", err)
	}
	order, err := chainOrder(local)
	if err != nil {
		return err
	}
	if len(order) != len(c.events)+len(c.plan.replay) || matchingPrefix(order, c.events) != len(c.events) ||
		!slices.Equal(idsOfEntries(order[len(c.events):]), c.plan.replay) {
		return fmt.Errorf("the local chain is not the hosted chain and the replayed work after replaying it")
	}
	// The store holds the story's whole chain again, as a fetch leaves it.
	if err := a.Store.Ledger.ClearMarks(ctx, ledger.MarkHistoryRemote, []string{c.id}); err != nil {
		return err
	}
	return c.settle(ctx, a, chain, now)
}

// applyMerge writes the merge planMerge decided. The merged events are first moved
// onto the last kept hosted event, so nothing that stays builds on the copies of
// hosted events off the hosted chain, the excluded moves or the dropped snapshots,
// and those are removed; then the hosted events the store lacks are written with
// their bases and the merged events moved after the hosted latest, in one
// transaction, and the chain_merge event is appended on the new tip. A failure part
// way leaves a fork of the hosted chain, which a re-run merges again.
func (c rechainCase) applyMerge(ctx context.Context, a *app.App, chain *chainPull, now time.Time) error {
	onHosted := make(map[string]bool, len(c.events))
	for _, e := range c.events {
		onHosted[e.ID] = true
	}
	keptEnd := ""
	if n := len(c.events) - len(c.plan.take); n > 0 {
		keptEnd = c.events[n-1].ID
	}
	if len(c.plan.merge) > 0 {
		if err := a.Store.Ledger.RebaseEvents(ctx, c.id, nil, c.plan.merge, keptEnd); err != nil {
			return err
		}
	}
	left := map[string]bool{}
	for _, x := range c.plan.excluded {
		left[x.ID] = true
	}
	for _, id := range c.plan.dropped {
		left[id] = true
	}
	if gone := append(slices.Clone(c.plan.retake), slices.Collect(maps.Keys(left))...); len(gone) > 0 {
		guard := func(e ledger.Entry) bool { return onHosted[e.ID] || left[e.ID] }
		if err := a.Store.Ledger.RemoveEvents(ctx, c.id, gone, guard); err != nil {
			return err
		}
	}
	taken := make([]ledger.Entry, 0, len(c.plan.take))
	for _, ev := range c.plan.take {
		e, err := ledgerEntryOf(c.id, ev)
		if err != nil {
			return fmt.Errorf("decode the hosted event %s of %s: %w", ev.ID, c.id, err)
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		taken = append(taken, e)
	}
	if len(c.plan.merge) > 0 {
		if err := a.Store.Ledger.RebaseEvents(ctx, c.id, taken, c.plan.merge, c.latest); err != nil {
			return err
		}
	} else if err := takeEvents(ctx, a, c.id, c.plan.take, now); err != nil {
		return err
	}
	record := chainMergeRecord{ForkBase: c.plan.forkBase, HostedLatest: c.latest,
		Merged: c.plan.merge, Excluded: c.plan.excluded, Dropped: c.plan.dropped}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	// The tip's workflow hash is named so the append records no version event of
	// its own between the merged events and the record.
	tip := c.events[len(c.events)-1].WorkflowVersion
	if local, err := a.Store.Ledger.StoryChain(ctx, c.id); err == nil && len(local) > 0 && local[len(local)-1].WorkflowHash != "" {
		tip = local[len(local)-1].WorkflowHash
	}
	if _, err := a.Store.Ledger.Append(ctx, ledger.AppendInput{
		StoryID: c.id, Kind: ledger.KindChainMerge, Actor: "satelle",
		Body:    fmt.Sprintf("merged %d local event(s) after the hosted latest %s, forked at %s", len(c.plan.merge), c.latest, c.plan.forkBase),
		Payload: payload, WorkflowHash: tip,
	}, now); err != nil {
		return err
	}
	local, err := a.Store.Ledger.StoryChain(ctx, c.id)
	if err != nil {
		return fmt.Errorf("read the local chain: %w", err)
	}
	order, err := chainOrder(local)
	if err != nil {
		return err
	}
	n := len(c.events)
	if len(order) < n+len(c.plan.merge)+1 || matchingPrefix(order, c.events) != n ||
		!slices.Equal(idsOfEntries(order[n:n+len(c.plan.merge)]), c.plan.merge) || order[len(order)-1].Kind != ledger.KindChainMerge {
		return fmt.Errorf("the local chain is not the hosted chain, the merged work and its chain_merge record after merging it")
	}
	if err := a.Store.Ledger.ClearMarks(ctx, ledger.MarkHistoryRemote, []string{c.id}); err != nil {
		return err
	}
	return c.settle(ctx, a, chain, now)
}

// settle rebuilds the story's row from its repaired chain, moves its head to the
// hosted latest and clears its conflict.
func (c rechainCase) settle(ctx context.Context, a *app.App, chain *chainPull, now time.Time) error {
	row, err := a.Store.Stories.Get(ctx, c.id)
	if err != nil {
		return fmt.Errorf("read the story row: %w", err)
	}
	if _, err := verb.RebuildRow(ctx, a.Store.Stories, a.Store.Ledger, row, now); err != nil {
		return fmt.Errorf("rebuild the story row from its chain: %w", err)
	}
	chain.pending[c.id] = c.latest
	chain.flush()
	_ = syncstate.RecordStoryConflicts(config.GlobalDir(), a.RepoRoot, nil, []string{c.id}, now)
	return nil
}

func idsOfEntries(events []ledger.Entry) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}
