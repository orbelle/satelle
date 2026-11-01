package verb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/epicset"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// `satelle story ready` (sty_a32bd6a9) readies backlog stories and epics without
// engaging them: it runs the readiness step the story's route declares — the
// performer, then the step's reviewers — records every verdict on the story's
// ledger, and marks the story only when the round was accepted. It changes no
// status and takes no engagement seat, so a scheduled or watched pass can ready
// many stories at once while another story holds the seat.
//
// It is the repo's own readiness, not a copy of it: the step, its performer, its
// reviewers, its reject budget and the tag that marks an accepted round are all
// read off the route (readinessEdgeFor); the round itself is runReadinessRound,
// the same helpers a presented edge uses.

func init() {
	Register(&Verb{
		Name:               "story-ready",
		Description:        "Ready backlog stories or epics without engaging them: run each one's readiness step, record the verdicts, mark it ready only when its readiness gates accept",
		Invoke:             storyReady,
		DispatchesReviewer: func(json.RawMessage) bool { return transitionGater != nil || executorDispatcher != nil },
	})
}

// Outcomes of readying one story.
const (
	ReadyMarked      = "marked"
	ReadyNotAccepted = "not-accepted"
	ReadyRefused     = "refused"
	// ReadyNoStep is a child whose route declares no readiness step: there is
	// nothing to ready, and its epic's readiness covers it, as it does for the wave
	// guard (refuseUnreadiedEpic).
	ReadyNoStep = "no-readiness-step"
)

// ReadyResult is the outcome for one story of a `story ready` call. Children is
// set on an epic: the outcome of each child readied on the way.
type ReadyResult struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
	// Attempt names the readiness attempt that was accepted, or rejected.
	Attempt string `json:"attempt,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Reused is true when the readiness already held and no round was run.
	Reused bool `json:"reused,omitempty"`
	// NoMark is true when the round holds but the readiness step declares no mark,
	// so no tag was applied: the ledger's accepted round is the whole record.
	NoMark   bool          `json:"no_mark,omitempty"`
	Children []ReadyResult `json:"children,omitempty"`
	// Advise names the advisor skills the readiness step declares, set only on the
	// result whose epic-level round was the repo's first accepted epic readiness,
	// whether or not its children then let it be marked. Nothing runs them.
	Advise []string `json:"advise,omitempty"`
}

// Line is the one-line report of the story:
// `<id> marked|not-accepted|refused|no-readiness-step[: reason]`. no-readiness-step
// is only ever a child of an epic: its route declares no readiness step, so it does
// not hold the epic back.
func (r ReadyResult) Line() string {
	line := r.ID + " " + r.Outcome
	if r.Outcome == ReadyMarked && r.Reused {
		line += " (readiness already holds, attempt " + r.Attempt + ")"
	}
	if r.Outcome == ReadyMarked && r.NoMark {
		line += " (readiness holds; no mark configured)"
	}
	if r.Reason != "" {
		line += ": " + r.Reason
	}
	return line
}

// Marked reports whether every story in the result — an epic's children included
// — was marked. A child with no readiness step has nothing to mark and counts as
// ready.
func (r ReadyResult) Marked() bool {
	if r.Outcome != ReadyMarked {
		return false
	}
	for _, c := range r.Children {
		if c.Outcome != ReadyNoStep && !c.Marked() {
			return false
		}
	}
	return true
}

type readyReq struct {
	IDs []string `json:"ids"`
}

func storyReady(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req readyReq
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	if len(req.IDs) == 0 {
		return nil, fmt.Errorf("verb: story-ready needs at least one story id")
	}
	out := make([]ReadyResult, 0, len(req.IDs))
	for _, id := range req.IDs {
		res := readyOne(ctx, strings.TrimSpace(id))
		for _, line := range flattenReady(res) {
			RecordVerdict(line)
		}
		out = append(out, res)
	}
	return json.Marshal(out)
}

// flattenReady lists the report lines of a result and its children.
func flattenReady(r ReadyResult) []string {
	lines := []string{r.Line()}
	for _, c := range r.Children {
		lines = append(lines, flattenReady(c)...)
	}
	return lines
}

// classifyChildEdgeErr decides what a failed readiness lookup for an epic's child
// means: a route with no readiness step leaves the child covered by the epic's
// readiness (ReadyNoStep, which does not hold the epic back); any other failure is
// refused and holds the epic back.
func classifyChildEdgeErr(err error) (outcome string, holdsEpic bool) {
	if errors.Is(err, errNoReadinessStep) {
		return ReadyNoStep, false
	}
	return ReadyRefused, true
}

func refused(id, format string, args ...any) ReadyResult {
	return ReadyResult{ID: id, Outcome: ReadyRefused, Reason: fmt.Sprintf(format, args...)}
}

// readyOne readies a single story or epic.
func readyOne(ctx context.Context, id string) ReadyResult {
	store, err := requireWorkItem()
	if err != nil {
		return refused(id, "%v", err)
	}
	item, err := storyState(ctx, id)
	if err != nil {
		return refused(id, "%v", err)
	}
	edge, ok, res := checkReadyable(ctx, item)
	if !ok {
		return res
	}
	if epicset.IsEpicParent(item) {
		return readyEpic(ctx, store, item, edge)
	}
	return readyStory(ctx, item, edge)
}

// checkReadyable applies the refusals common to a story and an epic, in the order
// that spares the most work: the route must declare a readiness step, the story
// must still be at the route's start status, its round budget must not be spent,
// and its performer must be one the engine dispatches. A step with no mark is
// readyable: the round is recorded on the ledger and only the display tag is
// absent. Nothing has been dispatched when it refuses.
func checkReadyable(ctx context.Context, item workitem.Item) (readinessEdge, bool, ReadyResult) {
	edge, err := readinessEdgeFor(ctx, item)
	if err != nil {
		return edge, false, refused(item.ID, "%v", err)
	}
	if item.Status != edge.From && !legacyReady(ctx, item, edge) {
		return edge, false, refused(item.ID, "status is %q, not the route's start status %q — only a story that has not started can be readied without engagement", item.Status, edge.From)
	}
	// The round judges edge.From→edge.To whatever the stored status, so the
	// budget and the performer are resolved on that edge.
	item = edge.present(item)
	if err := refuseSpentRejectBudget(ctx, item, edge.To); err != nil {
		return edge, false, refused(item.ID, "%v", err)
	}
	// The engine's single rule for who performs a step: the same one a presented
	// edge's dispatch applies. A step that dispatches nothing is not one `story
	// ready` can run — the driving session would have to perform it.
	resolver, isResolver := executorDispatcher.(PerformerResolver)
	if executorDispatcher == nil || !isResolver {
		return edge, false, refused(item.ID, "readiness step %q cannot be readied without engagement: no dispatcher is wired to perform it", edge.name())
	}
	perf, perr := resolver.StepPerformer(ctx, item, edge.To)
	if perr != nil {
		return edge, false, refused(item.ID, "readiness step %q: %v", edge.name(), perr)
	}
	if !perf.Dispatched {
		return edge, false, refused(item.ID, "readiness step %q is not performed by a dispatched agent (%s), so it cannot run without engagement", edge.name(), perf.Reason)
	}
	return edge, true, ReadyResult{}
}

// legacyReady reports whether item is an epic-parent that already sits at its
// readiness step's state with no accepted round that holds: an epic that reached
// ready before the readiness mark existed. Such an epic may run the round it never
// had, so its children can start; one whose readiness holds has nothing to re-run.
func legacyReady(ctx context.Context, item workitem.Item, edge readinessEdge) bool {
	if !epicset.IsEpicParent(item) || item.Status != edge.To {
		return false
	}
	_, holds := readinessHolds(ctx, item, edge)
	return !holds
}

// readyStory readies one non-container story: it reuses a readiness that still
// holds, and otherwise runs a round.
func readyStory(ctx context.Context, item workitem.Item, edge readinessEdge) ReadyResult {
	if attempt, holds := readinessHolds(ctx, item, edge); holds {
		recordReadinessReuse(ctx, item, edge.From, edge.To, attempt)
		if err := markReady(ctx, item.ID, edge.Step.Mark); err != nil {
			return refused(item.ID, "readiness holds (attempt %s) but marking it failed: %v", attempt, err)
		}
		return ReadyResult{ID: item.ID, Outcome: ReadyMarked, Attempt: attempt, Reused: true, NoMark: edge.Step.Mark == ""}
	}
	round := runReadinessRound(ctx, item, edge)
	if !round.Accepted {
		return ReadyResult{ID: item.ID, Outcome: ReadyNotAccepted, Attempt: round.Attempt, Reason: round.Reason}
	}
	if err := markReady(ctx, item.ID, edge.Step.Mark); err != nil {
		return ReadyResult{ID: item.ID, Outcome: ReadyNotAccepted, Attempt: round.Attempt, Reason: fmt.Sprintf("the round was accepted but marking the story failed: %v", err)}
	}
	return ReadyResult{ID: item.ID, Outcome: ReadyMarked, Attempt: round.Attempt, NoMark: edge.Step.Mark == ""}
}

// readyEpic readies an epic-parent: each backlog child whose readiness does not
// hold, then the epic's own readiness step (its judgement of order, parallelism
// and whether it can be achieved), then the children once more — the judgement may
// have filed one — and marks the epic only when every non-terminal child is ready
// and the epic-level round was accepted. A child whose route declares no readiness
// step has nothing to ready: it is reported ReadyNoStep and does not hold the epic
// back. A readiness step that declares ready_children = false skips both child
// passes: the epic is marked on its own round alone. The epic's status is never
// changed.
func readyEpic(ctx context.Context, store *workitem.Store, epic workitem.Item, edge readinessEdge) ReadyResult {
	res := ReadyResult{ID: epic.ID}
	idx, err := requireDocIndex()
	if err != nil {
		return refused(epic.ID, "%v", err)
	}
	wfs, err := idx.List(ctx, "workflows")
	if err != nil {
		return refused(epic.ID, "list workflows: %v", err)
	}
	// readyChildren readies every non-terminal child that has not started and whose
	// readiness does not hold, and reports which are still not ready. The first pass
	// runs each child once; the re-check pass (recheck) runs once more a child whose
	// round was not accepted — the epic-level judgement may have fixed it — but not
	// one that was refused, since nothing ran for it. At most one re-check happens.
	outcomes := map[string]string{}
	readyChildren := func(recheck bool) (notReady []string, listErr error) {
		kids, err := epicset.Members(ctx, store, epic)
		if err != nil {
			return nil, err
		}
		for _, kid := range kids {
			state := waveStateOf(wfs, kid)
			if !state.Known || state.Done || state.Cancelled {
				continue
			}
			kidEdge, err := readinessEdgeFor(ctx, kid)
			if err != nil {
				outcome, holdsEpic := classifyChildEdgeErr(err)
				if _, seen := outcomes[kid.ID]; !seen {
					outcomes[kid.ID] = outcome
					res.Children = append(res.Children, ReadyResult{ID: kid.ID, Outcome: outcome, Reason: err.Error()})
				}
				if holdsEpic {
					notReady = append(notReady, kid.ID)
				}
				continue
			}
			if kid.Status != kidEdge.From {
				continue // already past readiness
			}
			if _, holds := readinessHolds(ctx, kid, kidEdge); holds {
				continue
			}
			if prev, seen := outcomes[kid.ID]; seen && (!recheck || prev != ReadyNotAccepted) {
				notReady = append(notReady, kid.ID)
				continue
			}
			kr := readyOne(ctx, kid.ID)
			outcomes[kid.ID] = kr.Outcome
			// A re-checked child's second result replaces its first: one line per
			// child, and the epic is judged on the latest outcome.
			replaced := false
			for i := range res.Children {
				if res.Children[i].ID == kid.ID {
					res.Children[i], replaced = kr, true
					break
				}
			}
			if !replaced {
				res.Children = append(res.Children, kr)
			}
			if !kr.Marked() {
				notReady = append(notReady, kid.ID)
			}
		}
		return notReady, nil
	}
	// A step declaring ready_children = false plans no child here: the epic's own
	// round is the whole readiness, and each child is planned at its own
	// engagement, when the wave lets it start.
	perChild := edge.Step.ReadyChildren
	if perChild {
		if _, err := readyChildren(false); err != nil {
			return refused(epic.ID, "%v", err)
		}
	}
	first := firstEpicReadiness(ctx, epic)
	round := runReadinessRound(ctx, epic, edge)
	res.Attempt = round.Attempt
	if round.Accepted && first {
		res.Advise = epicAdvice(edge.To, edge.Advisors)
	}
	// A child created during the epic-level judgement is readied before the epic
	// can be marked.
	var notReady []string
	if perChild {
		notReady, err = readyChildren(true)
		if err != nil {
			r := refused(epic.ID, "%v", err)
			r.Advise = res.Advise
			return r
		}
	}
	switch {
	case !round.Accepted:
		res.Outcome, res.Reason = ReadyNotAccepted, round.Reason
	case len(notReady) > 0:
		res.Outcome, res.Reason = ReadyNotAccepted, "children not ready: "+strings.Join(notReady, ", ")
	default:
		if err := markReady(ctx, epic.ID, edge.Step.Mark); err != nil {
			res.Outcome, res.Reason = ReadyNotAccepted, fmt.Sprintf("every child is ready and the round was accepted but marking the epic failed: %v", err)
			return res
		}
		res.Outcome, res.NoMark = ReadyMarked, edge.Step.Mark == ""
	}
	return res
}
