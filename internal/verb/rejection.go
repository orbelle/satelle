package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// RejectionTarget is who acts on a rejected edge into to: the performer of the
// destination step when entering it dispatches one (it is re-dispatched with the
// findings on re-presentation), otherwise the step being left, whose work the
// reviewers judged. step is the step that performer belongs to; on an error it is
// the step whose performer could not be resolved. The headless drive and the
// same-status re-dispatch both ask it.
func RejectionTarget(ctx context.Context, r PerformerResolver, item workitem.Item, to string) (step string, target, fromPerf PerformerResolution, err error) {
	toPerf, err := r.StepPerformer(ctx, item, to)
	if err != nil {
		return to, target, fromPerf, err
	}
	fromPerf, err = r.StepPerformer(ctx, item, item.Status)
	if err != nil {
		return item.Status, target, fromPerf, err
	}
	if toPerf.Kind == PerformerDispatched {
		return to, toPerf, fromPerf, nil
	}
	return item.Status, fromPerf, fromPerf, nil
}

// LatestExitRejectFrom is the exit of step from whose gate rejected most
// recently: the destination of the newest review_reject row on an edge from→to
// with to ≠ from, and when it was recorded. ok is false when no exit of from was
// ever rejected.
func LatestExitRejectFrom(entries []ledger.Entry, from string) (to string, at time.Time, ok bool) {
	for _, e := range entries {
		if !ledger.IsExitReject(e) {
			continue
		}
		var row struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if json.Unmarshal(e.Payload, &row) != nil || row.From != from {
			continue
		}
		if !ok || !e.CreatedAt.Before(at) {
			to, at, ok = row.To, e.CreatedAt, true
		}
	}
	return to, at, ok
}

// LatestExitReject is LatestExitRejectFrom over the story's ledger: the exit of
// from rejected most recently, "" when none was.
func LatestExitReject(ctx context.Context, itemID, from string) string {
	to, _, _ := LatestExitRejectFrom(StoryLedgerKind(ctx, itemID, ledger.KindReviewReject), from)
	return to
}

// lastStepRun is when the performer of step last ran: the newest successful
// agent_invocation row whose edge enters step — its entry dispatch, or a run at
// the step itself (step→step). A failed run records no agent and is not a run.
func lastStepRun(entries []ledger.Entry, step string) (at time.Time, ok bool) {
	for _, e := range entries {
		if e.Kind != ledger.KindAgentInvocation {
			continue
		}
		var row struct {
			To    string `json:"to"`
			Agent string `json:"agent"`
		}
		if json.Unmarshal(e.Payload, &row) != nil || row.To != step || row.Agent == "" {
			continue
		}
		if !ok || e.CreatedAt.After(at) {
			at, ok = e.CreatedAt, true
		}
	}
	return at, ok
}

// reDispatchOwed reports whether a same-status set at current.Status owes the
// step's named performer one more run: an exit of the step was rejected after
// the performer last ran, and RejectionTarget holds the step's own dispatched
// performer to that rejection. The executor binding, an in-loop performer and a
// step nobody performs are owed nothing here.
func reDispatchOwed(ctx context.Context, current workitem.Item) bool {
	resolver, ok := executorDispatcher.(PerformerResolver)
	if !ok {
		return false
	}
	entries := storyLedgerEntries(ctx, current.ID)
	to, rejectedAt, ok := LatestExitRejectFrom(entries, current.Status)
	if !ok {
		return false
	}
	if ranAt, ran := lastStepRun(entries, current.Status); ran && !rejectedAt.After(ranAt) {
		return false
	}
	step, target, _, err := RejectionTarget(ctx, resolver, current, to)
	return err == nil && step == current.Status && target.Kind == PerformerDispatched
}

// redispatchStep runs the step's named performer once more at the status the
// story is already in, so it answers the findings of the exit it was rejected on
// (reDispatchOwed). It takes no edge and runs no gate. A performer that rejects,
// or a run the spend budget parks, parks the story as on entry (park); a failed
// run is recorded and refused.
func redispatchStep(ctx context.Context, current workitem.Item, now time.Time,
	park func(state, notes string, res DispatchResult) (json.RawMessage, error, bool)) (json.RawMessage, error) {
	defer keepEngagementAlive(ctx, current.ID)()
	step := current.Status
	run := dispatchPerformer(ctx, current, step, "", false, now)
	current = run.rebind(current)
	state := ResumeParkName(ctx, current)
	if run.Err != nil {
		if run.Reject != nil && state != "" && state != step {
			if out, perr, handled := park(state, run.Reject.Notes, run.Res); handled {
				return out, perr
			}
		}
		recordDispatchFailure(ctx, current, step, "", run.Res, run.Err, now)
		return nil, fmt.Errorf("re-dispatch of step %s refused: %v", step, run.Err)
	}
	if run.Refuse != nil {
		notifyChange(panelTopic(current.Kind))
		return nil, fmt.Errorf("re-dispatch of step %s refused: %v", step, run.Refuse)
	}
	return nil, nil
}
