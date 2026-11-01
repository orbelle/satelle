package verb_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentartifact"
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

// stepRuns is the story's agent_invocation rows on the edge step→step.
func stepRuns(t *testing.T, id, step string) []ledger.Entry {
	t.Helper()
	var out []ledger.Entry
	for _, e := range ledgerRows(t, id, ledger.KindAgentInvocation) {
		var p struct{ From, To string }
		if json.Unmarshal(e.Payload, &p) == nil && p.From == step && p.To == step {
			out = append(out, e)
		}
	}
	return out
}

// AC1: a rejected exit owes the named performer exactly one run at the same
// status, recorded as a step→step row; the status does not move. AC2: the row
// records the findings it was offered and the exit they came from. AC4 (b): a
// second set, with no newer reject, dispatches nothing.
func TestSameStatusSetRedispatchesTheNamedPerformerAfterAnExitReject(t *testing.T) {
	d := &redispatchStub{perf: coderAtInProgress, res: verb.DispatchResult{
		Skill: "code", FindingsOffered: []string{"code-review#1"}, FindingsEdgeTo: "done",
		Answers: []agentartifact.Answer{{Finding: "code-review#1", Answer: agentartifact.AnswerAdopted, Evidence: "guarded"}},
	}}
	db, it := engagedAtInProgress(t, d)
	rejectExit(t, db, it.ID, "in_progress", "done")

	if got := sameStatusSet(t, it.ID, "in_progress"); got.Status != "in_progress" {
		t.Fatalf("status = %q, a re-dispatch never moves status", got.Status)
	}
	if n := d.sameStatusCalls("in_progress"); n != 1 {
		t.Fatalf("same-status dispatches = %d (%v), want exactly 1", n, d.calls)
	}
	runs := stepRuns(t, it.ID, "in_progress")
	if len(runs) != 1 {
		t.Fatalf("step→step agent_invocation rows = %d, want 1", len(runs))
	}
	var row struct {
		Agent   string   `json:"agent"`
		Offered []string `json:"findings_offered"`
		EdgeTo  string   `json:"findings_edge_to"`
	}
	if err := json.Unmarshal(runs[0].Payload, &row); err != nil {
		t.Fatal(err)
	}
	if row.Agent != "coder" || len(row.Offered) != 1 || row.Offered[0] != "code-review#1" || row.EdgeTo != "done" {
		t.Errorf("run row = %+v, want the coder's run with code-review#1 offered from the exit to done", row)
	}

	if got := sameStatusSet(t, it.ID, "in_progress"); got.Status != "in_progress" {
		t.Fatalf("status = %q after a repeated set", got.Status)
	}
	if n := d.sameStatusCalls("in_progress"); n != 1 {
		t.Errorf("a repeated set with no newer reject dispatched again: %d calls (%v)", n, d.calls)
	}

	// A fresh reject owes one more run.
	rejectExit(t, db, it.ID, "in_progress", "done")
	sameStatusSet(t, it.ID, "in_progress")
	if n := d.sameStatusCalls("in_progress"); n != 2 {
		t.Errorf("a newer reject must owe one more run: %d calls (%v)", n, d.calls)
	}
}

// AC4: where nothing is owed the same-status set only re-seats. (a) no exit was
// rejected; (c) the step's performer is in-loop; (d) the step is the headless
// [executor] binding's, which the drive re-runs, not this set. Status never moves.
func TestSameStatusSetDispatchesNothingWhenNothingIsOwed(t *testing.T) {
	cases := map[string]struct {
		perf   map[string]verb.PerformerResolution
		reject bool
	}{
		"no reject": {perf: coderAtInProgress},
		"in-loop performer": {perf: map[string]verb.PerformerResolution{
			"in_progress": {Agent: "coder", Kind: verb.PerformerInLoop},
		}, reject: true},
		"executor binding": {perf: map[string]verb.PerformerResolution{
			"in_progress": {Kind: verb.PerformerExecutor},
		}, reject: true},
		"no performer": {perf: map[string]verb.PerformerResolution{}, reject: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d := &redispatchStub{perf: c.perf}
			db, it := engagedAtInProgress(t, d)
			if c.reject {
				rejectExit(t, db, it.ID, "in_progress", "done")
			}
			before := len(ledgerRows(t, it.ID, ledger.KindAgentInvocation))
			if got := sameStatusSet(t, it.ID, "in_progress"); got.Status != "in_progress" {
				t.Fatalf("status = %q", got.Status)
			}
			if n := d.sameStatusCalls("in_progress"); n != 0 {
				t.Errorf("dispatches = %d (%v), want none", n, d.calls)
			}
			if after := len(ledgerRows(t, it.ID, ledger.KindAgentInvocation)); after != before {
				t.Errorf("agent_invocation rows %d became %d: nothing ran", before, after)
			}
		})
	}
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

// directCoder posts a message from the orchestrator addressed to the coder by name.
func directCoder(t *testing.T, id, to, cc, body string) {
	t.Helper()
	req := map[string]any{"id": id, "from": "orchestrator", "to": to, "body": body}
	if cc != "" {
		req["cc"] = cc
	}
	post(t, req)
}

// sty_47525ee9 AC1/AC3: with no gate reject at all, an unanswered message
// directed to the one-shot coder owes it exactly one run at the same status. The
// story is not parked and its status does not move. A repeated set with no newer
// message dispatches nothing; a newer message owes one more run; a message FROM
// the coder answers what came before it.
func TestSameStatusSetRedispatchesOnADirectedMessageAlone(t *testing.T) {
	d := &redispatchStub{perf: coderAtInProgress}
	_, it := engagedAtInProgress(t, d)
	directCoder(t, it.ID, "coder", "", "the guard allows a downgrade — fix it")

	if got := sameStatusSet(t, it.ID, "in_progress"); got.Status != "in_progress" {
		t.Fatalf("status = %q, a re-dispatch never moves status or parks", got.Status)
	}
	if n := d.sameStatusCalls("in_progress"); n != 1 {
		t.Fatalf("same-status dispatches = %d (%v), want exactly 1 for a directed message", n, d.calls)
	}
	if runs := stepRuns(t, it.ID, "in_progress"); len(runs) != 1 {
		t.Fatalf("step→step agent_invocation rows = %d, want 1", len(runs))
	}
	for _, e := range ledgerRows(t, it.ID, ledger.KindStatusTransition) {
		var p struct{ To string }
		if json.Unmarshal(e.Payload, &p) == nil && p.To == "blocked" {
			t.Fatalf("the re-dispatch parked the story: %s", e.Payload)
		}
	}

	sameStatusSet(t, it.ID, "in_progress")
	if n := d.sameStatusCalls("in_progress"); n != 1 {
		t.Errorf("a repeated set with no newer message dispatched again: %d calls (%v)", n, d.calls)
	}

	directCoder(t, it.ID, "coder", "", "and the test is missing")
	sameStatusSet(t, it.ID, "in_progress")
	if n := d.sameStatusCalls("in_progress"); n != 2 {
		t.Errorf("a newer directed message must owe one more run: %d calls (%v)", n, d.calls)
	}

	// The coder answers; a message from it clears what came before it.
	directCoder(t, it.ID, "coder", "", "one more")
	post(t, map[string]any{"id": it.ID, "from": "coder", "to": "orchestrator", "body": "done"})
	sameStatusSet(t, it.ID, "in_progress")
	if n := d.sameStatusCalls("in_progress"); n != 2 {
		t.Errorf("a message the coder answered must owe nothing: %d calls (%v)", n, d.calls)
	}
}

// sty_47525ee9 AC1: a rejected in_progress→integration exit and a directed
// message together owe one run (not two), whose row records the findings it was
// offered and the exit they came from.
func TestSameStatusSetRedispatchesOnceForARejectAndAMessage(t *testing.T) {
	d := &redispatchStub{perf: coderAtInProgress, res: verb.DispatchResult{
		Skill: "code", FindingsOffered: []string{"code-review#1"}, FindingsEdgeTo: "integration",
	}}
	db, it := engagedAtInProgress(t, d)
	rejectExit(t, db, it.ID, "in_progress", "integration")
	directCoder(t, it.ID, "coder", "", "answer code-review#1")

	if got := sameStatusSet(t, it.ID, "in_progress"); got.Status != "in_progress" {
		t.Fatalf("status = %q", got.Status)
	}
	if n := d.sameStatusCalls("in_progress"); n != 1 {
		t.Fatalf("same-status dispatches = %d (%v), want exactly 1", n, d.calls)
	}
	runs := stepRuns(t, it.ID, "in_progress")
	if len(runs) != 1 {
		t.Fatalf("step→step agent_invocation rows = %d, want 1", len(runs))
	}
	var row struct {
		Agent   string   `json:"agent"`
		Offered []string `json:"findings_offered"`
		EdgeTo  string   `json:"findings_edge_to"`
	}
	if err := json.Unmarshal(runs[0].Payload, &row); err != nil {
		t.Fatal(err)
	}
	if row.Agent != "coder" || len(row.Offered) != 1 || row.EdgeTo != "integration" {
		t.Errorf("run row = %+v, want the coder's run offered code-review#1 from the exit to integration", row)
	}
}

// sty_47525ee9 AC3: a message that is not directed to the step's performer owes
// it nothing — addressed to another role, a broadcast, or a relay transcript row.
func TestSameStatusSetIgnoresMessagesNotDirectedToThePerformer(t *testing.T) {
	cases := map[string]struct{ to, cc string }{
		"to another role":      {to: "planner"},
		"broadcast":            {to: "*"},
		"relay transcript row": {to: "coder", cc: "*"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d := &redispatchStub{perf: coderAtInProgress}
			_, it := engagedAtInProgress(t, d)
			directCoder(t, it.ID, c.to, c.cc, "context")
			if got := sameStatusSet(t, it.ID, "in_progress"); got.Status != "in_progress" {
				t.Fatalf("status = %q", got.Status)
			}
			if n := d.sameStatusCalls("in_progress"); n != 0 {
				t.Errorf("dispatches = %d (%v), want none", n, d.calls)
			}
		})
	}
}
