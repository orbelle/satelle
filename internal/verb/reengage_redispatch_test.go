package verb_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_4470bb38: after a step's exit gate rejects, `story set <id> --status
// <current>` re-dispatches the step's named one-shot performer once, instead of
// only re-seating the lease.

// redispatchStub resolves each step's performer from perf (PerformerNone when
// absent) and records every dispatch as "<status>><to>".
type redispatchStub struct {
	mu    sync.Mutex
	calls []string
	perf  map[string]verb.PerformerResolution
	res   verb.DispatchResult
}

func (d *redispatchStub) StepPerformer(_ context.Context, _ workitem.Item, to string) (verb.PerformerResolution, error) {
	if p, ok := d.perf[to]; ok {
		return p, nil
	}
	return verb.PerformerResolution{Kind: verb.PerformerNone}, nil
}

func (d *redispatchStub) DispatchExecutor(_ context.Context, it workitem.Item, to string) (verb.DispatchResult, error) {
	d.mu.Lock()
	d.calls = append(d.calls, it.Status+">"+to)
	d.mu.Unlock()
	p := d.perf[to]
	if p.Kind != verb.PerformerDispatched {
		return verb.DispatchResult{}, nil
	}
	res := d.res
	res.Dispatched, res.Agent = true, p.Agent
	return res, nil
}

// sameStatusCalls is how many dispatches ran at the status the story was in.
func (d *redispatchStub) sameStatusCalls(status string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if c == status+">"+status {
			n++
		}
	}
	return n
}

var coderAtInProgress = map[string]verb.PerformerResolution{
	"in_progress": {Agent: "coder", Dispatched: true, Kind: verb.PerformerDispatched},
}

// engagedAtInProgress wires singleStoryWF and the stub, and engages a story to
// in_progress (its entry dispatches the stub once).
func engagedAtInProgress(t *testing.T, d *redispatchStub) (*store.DB, workitem.Item) {
	t.Helper()
	db := wireWithWorkflowsStore(t, singleStoryWF)
	verb.SetExecutorDispatcher(d)
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "re-dispatch", "category": "feature"}), &it)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": it.ID, "status": "plan"}), &it)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"}), &it)
	if it.Status != "in_progress" {
		t.Fatalf("engage: status %q", it.Status)
	}
	return db, it
}

// rejectExit records a gate's reject of the edge from→to, as the gate round does.
func rejectExit(t *testing.T, db *store.DB, id, from, to string) {
	t.Helper()
	p, _ := json.Marshal(map[string]any{"from": from, "to": to, "skill": "code-review", "notes": "the guard allows a downgrade"})
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: id, Kind: ledger.KindReviewReject, Actor: "reviewer", Body: "rejected", Payload: p,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func sameStatusSet(t *testing.T, id, status string) workitem.Item {
	t.Helper()
	var it workitem.Item
	json.Unmarshal(call(t, "story-set", map[string]any{"id": id, "status": status}), &it)
	return it
}

// RejectionTarget is the drive's decision, now shared: the destination's
// dispatched performer when it has one, else the step being left.
func TestRejectionTargetNamesWhoActsOnARejectedEdge(t *testing.T) {
	item := workitem.Item{ID: "sty_x", Status: "in_progress"}
	d := &redispatchStub{perf: map[string]verb.PerformerResolution{
		"in_progress": {Agent: "coder", Dispatched: true, Kind: verb.PerformerDispatched},
		"plan":        {Agent: "planner", Dispatched: true, Kind: verb.PerformerDispatched},
	}}
	step, target, _, err := verb.RejectionTarget(context.Background(), d, item, "done")
	if err != nil || step != "in_progress" || target.Agent != "coder" {
		t.Errorf("exit to an unperformed step: step %q target %+v err %v, want the coder at in_progress", step, target, err)
	}
	step, target, _, err = verb.RejectionTarget(context.Background(), d, item, "plan")
	if err != nil || step != "plan" || target.Agent != "planner" {
		t.Errorf("edge into a dispatched step: step %q target %+v err %v, want the planner at plan", step, target, err)
	}
}

func TestLatestExitRejectFromPicksTheNewestExit(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 21, 0, 0, 0, time.UTC)
	row := func(kind, from, to string, at time.Time) ledger.Entry {
		p, _ := json.Marshal(map[string]string{"from": from, "to": to})
		return ledger.Entry{Kind: kind, Payload: p, CreatedAt: at}
	}
	entries := []ledger.Entry{
		row(ledger.KindReviewReject, "in_progress", "blocked", t0.Add(3*time.Hour)),
		row(ledger.KindReviewReject, "in_progress", "integration", t0.Add(time.Hour)),
		row(ledger.KindReviewReject, "plan", "in_progress", t0.Add(4*time.Hour)),        // an entry, not an exit
		row(ledger.KindReviewAccept, "in_progress", "integration", t0.Add(5*time.Hour)), // not a reject
	}
	to, at, ok := verb.LatestExitRejectFrom(entries, "in_progress")
	if !ok || to != "blocked" || !at.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("got %q at %v (%v), want blocked at +3h", to, at, ok)
	}
	if _, _, ok := verb.LatestExitRejectFrom(entries, "done"); ok {
		t.Error("a step with no rejected exit reports none")
	}
}
