package cli

import (
	"context"
	"encoding/json"
	"fmt"
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
	var dryRun bool
	cmd := &cobra.Command{
		Use:         "rechain [story-id...]",
		Short:       "Put a story forked from its hosted chain onto that chain",
		Annotations: hostedOrigin(needsStore()),
		Args:        cobra.ArbitraryArgs,
		Long: `rechain puts a story forked from its hosted chain back on it, moves its push
head to the hosted latest and clears its conflict. It never pushes.

A local event the hosted chain lacks is dropped when seeded, or a status_reconcile
to the status the hosted chain folds to. Local work on a root of its own (written
while the history was on the server) is replayed after the hosted latest, in
order; a replayed status move must start from the hosted status. Any other local
work is refused by name, untouched.

With no ids it takes every story in conflict or differing from its hosted chain.
--dry-run applies the same checks and changes nothing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncWorkstateRechain(cmd, server, dryRun, args)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report each story as it would be repaired or refused without changing anything.")
	return cmd
}

func runSyncWorkstateRechain(cmd *cobra.Command, serverArg string, dryRun bool, ids []string) error {
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
	cases, err := rechainCases(ctx, a, chain, rd, ids)
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
		fmt.Fprintf(out, "%s %s: remove %d event(s) (%d seeded, %d reconcile), take %d hosted event(s)%s\n",
			word("rechained", "would rechain"), c.id, len(c.plan.remove)+len(c.plan.retake), c.plan.seeded, len(c.plan.reconciled), len(c.plan.take), replayed)
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
// local story whose chain differs from its hosted one.
func rechainCases(ctx context.Context, a *app.App, chain *chainPull, rd chainReader, ids []string) ([]rechainCase, error) {
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
		if ids, err = a.Store.Ledger.StoryIDs(ctx); err != nil {
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
	c.plan = planRechain(local, events, func() (string, error) { return hostedStatus(ctx, a, id, events) })
	return c
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
func planRechain(local []ledger.Entry, events []hosted.ChainEvent, hostedStatus func() (string, error)) rechainPlan {
	refuse := func(format string, args ...any) rechainPlan {
		return rechainPlan{outcome: rechainRefused, reason: fmt.Sprintf(format, args...)}
	}
	if plan, ok := planReplay(local, events, hostedStatus); ok {
		return plan
	}
	order, err := chainOrder(local)
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
		if _, ok := storyfold.SeededEvents(order, localOnly); !ok {
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
	if len(plan.remove) == 0 && len(plan.take) == 0 {
		plan.outcome = rechainNothing
	}
	return plan
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
// must start from the status the hosted chain folds to; otherwise the story moved
// elsewhere and is refused with both statuses.
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
	// Only seeded events and reconciles are the shapes planRechain repairs by
	// dropping them; anything else among the local-only events is work.
	if !slices.ContainsFunc(own, func(e ledger.Entry) bool {
		return !storyfold.IsSeededEvent(e) && e.Kind != ledger.KindStatusReconcile
	}) {
		return rechainPlan{}, false
	}
	ownOrder, err := chainOrder(own)
	if err != nil {
		return rechainPlan{}, false // not a root of its own: built on a hosted event, or forked
	}
	var kept []ledger.Entry
	for _, ev := range events {
		e, ok := held[ev.ID]
		if !ok || e.BaseID != ev.BaseID {
			break
		}
		kept = append(kept, e)
	}
	take, err := takePlan(kept, events)
	if err != nil {
		return refuse("the hosted chain cannot be taken: %v", err)
	}
	plan := rechainPlan{outcome: rechainDo, take: take}
	for _, ev := range events[len(kept):] {
		if _, ok := held[ev.ID]; ok {
			plan.retake = append(plan.retake, ev.ID)
		}
	}
	guarded := false
	for _, e := range ownOrder {
		if e.Kind == ledger.KindDefinitionSnapshot && storyfold.IsSeededEvent(e) {
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
			if from := storyfold.TransitionFrom(e); from != folded {
				return refuse("local status_transition %s moves from %s but the hosted chain folds to %s", e.ID, from, folded)
			}
		}
		plan.replay = append(plan.replay, e.ID)
	}
	return plan, true
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
// the hosted chain are not: a push or a pull is what moves them.
func divergent(local []ledger.Entry, events []hosted.ChainEvent) bool {
	if len(events) == 0 || len(local) == 0 {
		return false
	}
	order, err := chainOrder(local)
	if err != nil {
		return true
	}
	return matchingPrefix(order, events) < min(len(order), len(events))
}

// apply puts the story on its hosted chain, then moves its head to the hosted
// latest and clears its conflict. planRechain made every check; a re-run after a
// failure part way finds a start of the hosted chain and takes the rest.
func (c rechainCase) apply(ctx context.Context, a *app.App, chain *chainPull, now time.Time) error {
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
	order, err := chainOrder(local)
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
