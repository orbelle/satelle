package drive_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/drive"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A drive never ends short of its route's exit without a record (sty_aaafe4e2):
// after its first presentation every other end parks the story with a reason
// naming the step, the performer and what was missing, or — where it cannot park —
// writes a drive-stopped row.

const question = "Should I use X or Y?"

// routeNoPark is routeDone without a park state.
const routeNoPark = `["*"]
obligations = ["raised", "coded", "closed"]
cancel = { state = "cancelled" }
`

// routeExecutionParks is routeNoPark with an execution list that has a park
// state: the wildcard list does not park an execution, so an execution is only
// parked where its own list says so.
const routeExecutionParks = routeNoPark + `
[execution]
obligations = ["raised", "coded", "closed"]
park = { state = "blocked" }
cancel = { state = "cancelled" }
`

// namedRoute performs in_progress with the named agent coder.
const namedRoute = `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "coder"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`

// reworkRoute is namedRoute with a declared rework relay on in_progress.
const reworkRoute = `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "coder"
rework = { consult = "reviewer-consult", rounds = 2 }
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`

// lateNamedRoute performs the exit step, done, with the named agent coder.
const lateNamedRoute = `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
requires = ["raised"]

[closed]
status = "done"
agent = "coder"
terminal = true
requires = ["coded"]
`

// budgetedRoute is workRoute whose exit declares a reject budget.
const budgetedRoute = `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
reject_budget = 2
requires = ["coded"]
`

// stubDispatcher is the engine's named-performer dispatch, scripted per step.
type stubDispatcher struct {
	mu      sync.Mutex
	out     map[string]string
	errs    map[string]error
	rejects map[string]*verb.PerformerReject
}

func newStubDispatcher() *stubDispatcher {
	return &stubDispatcher{out: map[string]string{}, errs: map[string]error{}, rejects: map[string]*verb.PerformerReject{}}
}

func (d *stubDispatcher) DispatchExecutor(_ context.Context, _ workitem.Item, to string) (verb.DispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out, ok := d.out[to]
	res := verb.DispatchResult{Dispatched: true, Agent: "coder", Command: "fake-coder", Skill: "coder", Output: out, LastMessage: verb.LastMessage(out)}
	switch {
	case d.rejects[to] != nil:
		return res, d.rejects[to]
	case d.errs[to] != nil:
		return res, d.errs[to]
	case !ok:
		return verb.DispatchResult{}, nil
	}
	return res, nil
}

// append writes a ledger row the way a mechanism outside the drive does.
func (e *env) append(id, kind, actor, body string, payload map[string]any) {
	e.t.Helper()
	_, err := verb.Dispatch(context.Background(), "ledger-append", mustJSON(map[string]any{
		"story_id": id, "kind": kind, "actor": actor, "body": body, "payload": payload,
	}))
	if err != nil {
		e.t.Fatalf("ledger-append %s: %v", kind, err)
	}
}

// otherInvocations appends a reviewer's and a summariser's agent_invocation row,
// which carry no last_message, so a reader of the newest row alone is wrong.
func (e *env) otherInvocations(id string) {
	e.t.Helper()
	e.append(id, ledger.KindAgentInvocation, "reviewer", "reviewed", map[string]any{"from": "a", "to": "b", "skill": "stub-gate", "tokens_in": 5})
	e.append(id, ledger.KindAgentInvocation, "reviewer", "summarised", map[string]any{"from": "a", "to": "b", "summary": true, "tokens_in": 3})
}

func payloadOf(t *testing.T, row ledger.Entry) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(row.Payload, &p); err != nil {
		t.Fatalf("row %s payload: %v (%s)", row.Kind, err, row.Payload)
	}
	return p
}

// driveRows are the park comments (payload source "drive") and the drive-stopped
// rows a drive wrote.
func (e *env) driveRows(id string) (parks, stops []ledger.Entry) {
	e.t.Helper()
	for _, row := range e.rows(id, ledger.KindComment) {
		var p struct {
			Source string `json:"source"`
		}
		if json.Unmarshal(row.Payload, &p) == nil && p.Source == "drive" {
			parks = append(parks, row)
		}
	}
	return parks, e.rows(id, ledger.KindDriveStopped)
}

func (e *env) wantNoDriveRecords(id string) {
	e.t.Helper()
	if parks, stops := e.driveRows(id); len(parks) != 0 || len(stops) != 0 {
		e.t.Errorf("a drive that never began, or ended terminal, records no stop: %d parks, %d drive-stopped", len(parks), len(stops))
	}
}

// ledgerIDs is every row in the ledger, across all stories.
func (e *env) ledgerIDs() map[string]bool {
	e.t.Helper()
	rows, err := e.db.Ledger.ListAll(context.Background(), 0)
	if err != nil {
		e.t.Fatal(err)
	}
	ids := make(map[string]bool, len(rows))
	for _, r := range rows {
		ids[r.ID] = true
	}
	return ids
}

// wantLedgerDelta fails if the ledger holds any row that before did not: a drive
// that refuses before its first presentation writes nothing at all.
func (e *env) wantLedgerDelta(before map[string]bool) {
	e.t.Helper()
	rows, err := e.db.Ledger.ListAll(context.Background(), 0)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range rows {
		if !before[r.ID] {
			e.t.Errorf("a drive that refused before its first presentation wrote a %s row (actor %s): %s", r.Kind, r.Actor, r.Body)
		}
	}
	if len(rows) != len(before) {
		e.t.Errorf("ledger holds %d rows, want the %d it held before the drive", len(rows), len(before))
	}
}

// wantStopPayload checks the keys every park comment and drive-stopped row carries.
func wantStopPayload(t *testing.T, row ledger.Entry, lastMessage string) map[string]any {
	t.Helper()
	p := payloadOf(t, row)
	for _, key := range []string{"source", "step", "performer", "reason", "last_message", "location_id"} {
		if _, ok := p[key]; !ok {
			t.Errorf("%s row payload lacks %q: %v", row.Kind, key, p)
		}
	}
	if p["source"] != "drive" || p["location_id"] != "loc-test" || p["location_label"] != "label-test" {
		t.Errorf("%s row payload = %v, want source drive and the node's location", row.Kind, p)
	}
	reason, _ := p["reason"].(string)
	if !strings.HasPrefix(reason, fmt.Sprintf("step %v, performer %v:", p["step"], p["performer"])) {
		t.Errorf("%s row reason %q does not begin with its step and performer", row.Kind, reason)
	}
	if lastMessage != "*" && p["last_message"] != lastMessage {
		t.Errorf("%s row last_message = %q, want %q", row.Kind, p["last_message"], lastMessage)
	}
	return p
}

func opts(perf drive.Performers) drive.Options {
	return drive.Options{Performers: perf, Location: "loc-test", LocationLabel: "label-test"}
}

// AC1: a presentation refused by a plain error after the first one parks the
// story, naming the step, the performer and the refusal.
func TestDriveParksOnRefusalAfterFirstMove(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	e.gater.errs["done"] = errors.New("gate backend unreachable")
	id := e.story()
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	res, err := drive.Story(context.Background(), id, opts(perf))
	if err != nil {
		t.Fatalf("a refusal after the first move parks: %v", err)
	}
	if !res.Parked || res.Status != "blocked" || e.status(id) != "blocked" {
		t.Fatalf("want parked at blocked, got %+v (stored %q)", res, e.status(id))
	}
	if !strings.HasPrefix(res.Reason, "step done, performer none:") || !strings.Contains(res.Reason, "gate backend unreachable") {
		t.Errorf("reason = %q, want the step, the performer and the refusal", res.Reason)
	}
	parks, stops := e.driveRows(id)
	if len(parks) != 1 || len(stops) != 0 {
		t.Fatalf("want one park comment and no drive-stopped row, got %d and %d", len(parks), len(stops))
	}
	wantStopPayload(t, parks[0], "*")
}

// AC8: a refusal on the very first presentation is a refusal to start: nothing
// is parked or recorded.
func TestDriveRefusalOnFirstPresentationRecordsNothing(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	e.gater.errs["in_progress"] = errors.New("gate backend unreachable")
	id := e.story()
	_, err := drive.Story(context.Background(), id, opts(&fakePerformers{}))
	if err == nil || !strings.Contains(err.Error(), "gate backend unreachable") {
		t.Fatalf("want the refusal as an error, got %v", err)
	}
	var stopped *drive.Stopped
	if errors.As(err, &stopped) || e.status(id) != "backlog" {
		t.Errorf("a refusal before the first move is plain: %v (status %q)", err, e.status(id))
	}
	e.wantNoDriveRecords(id)
}

// AC8: a drive over an id that does not exist records nothing.
func TestDriveOfAnUnknownStoryRecordsNothing(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	e.story()
	before := e.ledgerIDs()
	if _, err := drive.Story(context.Background(), "sty_nothere", opts(&fakePerformers{})); err == nil {
		t.Fatal("an unknown story must be refused")
	}
	e.wantLedgerDelta(before)
}

// AC8: a drive over an id that is neither a story nor an execution (a task header
// has no lifecycle) is refused and leaves the ledger as it was.
func TestDriveOfANonStoryRecordsNothing(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	raw, err := verb.Dispatch(context.Background(), "task-create", mustJSON(map[string]any{"title": "not a story"}))
	if err != nil {
		t.Fatalf("task-create: %v", err)
	}
	var task workitem.Item
	if err := json.Unmarshal(raw, &task); err != nil || task.ID == "" {
		t.Fatalf("task-create: %v %q", err, raw)
	}
	before := e.ledgerIDs()
	_, err = drive.Story(context.Background(), task.ID, opts(&fakePerformers{}))
	if err == nil || !strings.Contains(err.Error(), "is not a story or execution") {
		t.Fatalf("want a refusal naming that %s is not a story or execution, got %v", task.ID, err)
	}
	e.wantLedgerDelta(before)
}

// AC8: a story whose route cannot be resolved (the repo's done half does not
// parse, so no route governs) is refused before any presentation and leaves the
// ledger as it was.
func TestDriveWithNoRouteRecordsNothing(t *testing.T) {
	e := newEnv(t, "[\"*\"]\nobligations = [", workRoute)
	id := e.story()
	before := e.ledgerIDs()
	_, err := drive.Story(context.Background(), id, opts(&fakePerformers{}))
	if err == nil || !strings.Contains(err.Error(), "no route to walk") {
		t.Fatalf("want a refusal naming the missing route, got %v", err)
	}
	var stopped *drive.Stopped
	if errors.As(err, &stopped) || e.status(id) != "backlog" {
		t.Errorf("a refusal to start is plain: %v (status %q)", err, e.status(id))
	}
	e.wantLedgerDelta(before)
}

// AC3: a performer that exits cleanly having asked a question leaves the question
// on the park record, however many reviewer and summariser rows came after it.
func TestDriveLastMessageExecutorCleanQuestion(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	e.gater.always["done"] = true
	id := e.story()
	e.gater.onGate = func(to string) {
		if to == "done" {
			e.otherInvocations(id)
		}
	}
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(), output: question}
	res, err := drive.Story(context.Background(), id, opts(perf))
	if err != nil || !res.Parked {
		t.Fatalf("want parked, got %v %+v", err, res)
	}
	parks, _ := e.driveRows(id)
	if len(parks) != 1 {
		t.Fatalf("want one park comment, got %d", len(parks))
	}
	wantStopPayload(t, parks[0], question)
}

// AC3: the same for a named performer dispatched inside story-set.
func TestDriveLastMessageNamedPerformerCleanQuestion(t *testing.T) {
	e := newEnv(t, routeDone, namedRoute)
	d := newStubDispatcher()
	d.out["in_progress"] = question
	verb.SetExecutorDispatcher(d)
	e.gater.always["done"] = true
	id := e.story()
	e.gater.onGate = func(to string) {
		if to == "done" {
			e.otherInvocations(id)
		}
	}
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerDispatched}, agents: map[string]string{"in_progress": "coder"}}
	res, err := drive.Story(context.Background(), id, opts(perf))
	if err != nil || !res.Parked {
		t.Fatalf("want parked, got %v %+v", err, res)
	}
	parks, _ := e.driveRows(id)
	if len(parks) != 1 {
		t.Fatalf("want one park comment, got %d", len(parks))
	}
	p := wantStopPayload(t, parks[0], question)
	if p["performer"] != "coder" || p["step"] != "in_progress" {
		t.Errorf("payload = %v, want the coder on in_progress", p)
	}
}

// AC4: a context that ends mid-step still leaves a record, even though the park
// is written after it ended.
func TestDriveRecordsStopOnCancel(t *testing.T) {
	for _, tc := range []struct {
		name, done string
		wantPark   bool
	}{{"parks", routeDone, true}, {"no park state", routeNoPark, false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.done, workRoute)
			id := e.story()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			perf := &fakePerformers{
				kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(), output: "working on it",
				onPerform: func(workitem.Item, string, string) error { cancel(); return nil },
			}
			res, err := drive.Story(ctx, id, opts(perf))
			parks, stops := e.driveRows(id)
			if tc.wantPark {
				if err != nil || !res.Parked || len(parks) != 1 || len(stops) != 0 {
					t.Fatalf("want a park comment, got %v %+v (%d parks, %d stops)", err, res, len(parks), len(stops))
				}
				p := wantStopPayload(t, parks[0], "working on it")
				if !strings.Contains(p["reason"].(string), "interrupted") {
					t.Errorf("reason = %v, want the interruption named", p["reason"])
				}
				return
			}
			var stopped *drive.Stopped
			if !errors.As(err, &stopped) || len(parks) != 0 || len(stops) != 1 {
				t.Fatalf("want a drive-stopped row, got %v (%d parks, %d stops)", err, len(parks), len(stops))
			}
			wantStopPayload(t, stops[0], "working on it")
		})
	}
}

// AC5: where the workflow has no park state, the stop is a drive-stopped row, once.
func TestDriveStoppedNoParkState(t *testing.T) {
	e := newEnv(t, routeNoPark, workRoute)
	e.gater.always["done"] = true
	id := e.story()
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(), output: question}
	_, err := drive.Story(context.Background(), id, opts(perf))
	var stopped *drive.Stopped
	if !errors.As(err, &stopped) || stopped.ID != id || stopped.Status != "in_progress" {
		t.Fatalf("want a *Stopped at in_progress, got %v", err)
	}
	if want := id + " stopped at in_progress: " + stopped.Reason; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
	parks, stops := e.driveRows(id)
	if len(parks) != 0 || len(stops) != 1 || e.status(id) != "in_progress" {
		t.Fatalf("want one drive-stopped row and nothing parked, got %d parks, %d stops, status %q", len(parks), len(stops), e.status(id))
	}
	p := wantStopPayload(t, stops[0], question)
	if !strings.Contains(p["park_error"].(string), "no park state") || stops[0].Body != stopped.Reason {
		t.Errorf("row = %v body %q, want the park error and the reason", p, stops[0].Body)
	}
}

// AC5: where the park itself is refused, the stop is a drive-stopped row, once.
func TestDriveStoppedParkRefused(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	e.gater.always["done"] = true
	e.gater.always["blocked"] = true
	id := e.story()
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(), output: question}
	_, err := drive.Story(context.Background(), id, opts(perf))
	var stopped *drive.Stopped
	if !errors.As(err, &stopped) || stopped.ParkErr == nil {
		t.Fatalf("want a *Stopped carrying why the park failed, got %v", err)
	}
	_, stops := e.driveRows(id)
	if len(stops) != 1 || e.status(id) != "in_progress" {
		t.Fatalf("want one drive-stopped row and the story where it was, got %d stops, status %q", len(stops), e.status(id))
	}
	p := wantStopPayload(t, stops[0], question)
	if !strings.Contains(p["park_error"].(string), "park at blocked") {
		t.Errorf("park_error = %v, want the refused park named", p["park_error"])
	}
}

// AC9: every way a drive stops names the step, the performer and what was missing.
func TestDriveStopReasonsNameStepAndPerformer(t *testing.T) {
	executorPerf := func(onPerform func(workitem.Item, string, string) error) *fakePerformers {
		return &fakePerformers{
			kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(),
			output: question, onPerform: onPerform,
		}
	}
	coderPerf := func(step string) *fakePerformers {
		return &fakePerformers{kinds: map[string]verb.PerformerKind{step: verb.PerformerDispatched}, agents: map[string]string{step: "coder"}}
	}
	cases := []struct {
		name, done, route, want string
		setup                   func(e *env, d *stubDispatcher)
		perf                    func() *fakePerformers
		rework                  drive.Reworker
	}{
		{name: "reject budget spent", done: routeDone, route: budgetedRoute, want: "step in_progress, performer executor:",
			setup: func(e *env, _ *stubDispatcher) { e.gater.always["done"] = true }, perf: func() *fakePerformers { return executorPerf(nil) }},
		{name: "unbudgeted rejections", done: routeDone, route: workRoute, want: "step backlog, performer gate:stub-gate:",
			setup: func(e *env, _ *stubDispatcher) { e.gater.always["in_progress"] = true }, perf: func() *fakePerformers { return &fakePerformers{} }},
		{name: "executor rejects", done: routeDone, route: workRoute, want: "step in_progress, performer executor:",
			perf: func() *fakePerformers {
				return executorPerf(func(workitem.Item, string, string) error { return &verb.PerformerReject{Notes: "merge conflict"} })
			}},
		{name: "executor fails", done: routeDone, route: workRoute, want: "step in_progress, performer executor:",
			perf: func() *fakePerformers {
				return executorPerf(func(workitem.Item, string, string) error { return errors.New("rate limited") })
			}},
		{name: "rework relay", done: routeDone, route: reworkRoute, want: "step in_progress, performer coder:",
			setup: func(e *env, _ *stubDispatcher) { e.gater.always["done"] = true }, perf: func() *fakePerformers { return coderPerf("in_progress") },
			rework: func(context.Context, string) (drive.Rework, error) {
				return drive.Rework{Outcome: "budget", Rounds: 2, LastObjection: "STILL"}, nil
			}},
		{name: "rework relay fails", done: routeDone, route: reworkRoute, want: "step in_progress, performer coder:",
			setup: func(e *env, _ *stubDispatcher) { e.gater.always["done"] = true }, perf: func() *fakePerformers { return coderPerf("in_progress") },
			rework: func(context.Context, string) (drive.Rework, error) { return drive.Rework{}, errors.New("no session") }},
		{name: "set parks it", done: routeDone, route: namedRoute, want: "step in_progress, performer coder:",
			setup: func(_ *env, d *stubDispatcher) {
				d.rejects["in_progress"] = &verb.PerformerReject{Notes: "premise is wrong"}
			},
			perf: func() *fakePerformers { return coderPerf("in_progress") }},
		{name: "refused after the first move", done: routeDone, route: workRoute, want: "step done, performer none:",
			setup: func(e *env, _ *stubDispatcher) { e.gater.errs["done"] = errors.New("gate down") },
			perf:  func() *fakePerformers { return executorPerf(nil) }},
		{name: "named performer fails", done: routeDone, route: lateNamedRoute, want: "step done, performer coder:",
			setup: func(_ *env, d *stubDispatcher) { d.errs["done"] = errors.New("coder crashed") },
			perf:  func() *fakePerformers { return coderPerf("done") }},
		{name: "no park state", done: routeNoPark, route: workRoute, want: "step in_progress, performer executor:",
			setup: func(e *env, _ *stubDispatcher) { e.gater.always["done"] = true }, perf: func() *fakePerformers { return executorPerf(nil) }},
		{name: "park refused", done: routeDone, route: workRoute, want: "step in_progress, performer executor:",
			setup: func(e *env, _ *stubDispatcher) { e.gater.always["done"] = true; e.gater.always["blocked"] = true },
			perf:  func() *fakePerformers { return executorPerf(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.done, tc.route)
			d := newStubDispatcher()
			if strings.Contains(tc.route, `agent = "coder"`) {
				d.out["in_progress"], d.out["done"] = "coder output", "coder output"
				verb.SetExecutorDispatcher(d)
			}
			if tc.setup != nil {
				tc.setup(e, d)
			}
			id := e.story()
			o := opts(tc.perf())
			o.Rework = tc.rework
			res, err := drive.Story(context.Background(), id, o)
			var reason string
			var stopped *drive.Stopped
			switch {
			case errors.As(err, &stopped):
				reason = stopped.Reason
			case err != nil:
				t.Fatalf("drive: %v", err)
			case res.Parked:
				reason = res.Reason
			default:
				t.Fatalf("the drive neither parked nor stopped: %+v", res)
			}
			t.Logf("reason: %s", reason)
			if !strings.HasPrefix(reason, tc.want) {
				t.Errorf("reason = %q, want it to begin %q", reason, tc.want)
			}
			parks, stops := e.driveRows(id)
			if len(parks)+len(stops) == 0 {
				t.Fatal("no park comment or drive-stopped row was written")
			}
			for _, row := range append(parks, stops...) {
				wantStopPayload(t, row, "*")
			}
		})
	}
}

// An execution has no session watching it, so its drive's every end short of the
// route's exit is recorded as stopped (sty_132e7781): a drive-stopped row on the
// execution carrying outcome "stopped", and, when its story_id input names a
// story, an execution_stopped alert on that story.

// execution makes a task that declares story_id and one execution of it about
// subject; "" leaves the input unset. It returns the task and the execution.
func (e *env) execution(subject string) (task, exec workitem.Item) {
	e.t.Helper()
	ctx := context.Background()
	task, err := e.db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindTask, Title: "sweep",
		Inputs: []workitem.InputDecl{{Name: workitem.InputStoryID, Type: workitem.InputString}},
	}, time.Now())
	if err != nil {
		e.t.Fatalf("create task: %v", err)
	}
	req := map[string]any{"title": "one sweep", "parent_id": task.ID}
	if subject != "" {
		req["inputs"] = map[string]string{workitem.InputStoryID: subject}
	}
	raw, err := verb.Dispatch(ctx, "execution-create", mustJSON(req))
	if err != nil {
		e.t.Fatalf("execution-create: %v", err)
	}
	if err := json.Unmarshal(raw, &exec); err != nil || exec.ID == "" {
		e.t.Fatalf("execution-create: %v %q", err, raw)
	}
	return task, exec
}

// wantStoppedExecution checks the one drive-stopped row an execution's stop wrote
// and, when subject names a story, the one execution_stopped alert on it. It
// returns the row's payload.
func (e *env) wantStoppedExecution(task, exec workitem.Item, subject, lastMessage string) map[string]any {
	e.t.Helper()
	_, stops := e.driveRows(exec.ID)
	if len(stops) != 1 {
		e.t.Fatalf("want exactly one drive-stopped row on the execution, got %d", len(stops))
	}
	p := wantStopPayload(e.t, stops[0], lastMessage)
	if p["outcome"] != drive.OutcomeStopped {
		e.t.Errorf("drive-stopped payload outcome = %v, want %q", p["outcome"], drive.OutcomeStopped)
	}
	if subject == "" {
		return p
	}
	alerts := e.rows(subject, ledger.KindExecutionStopped)
	if len(alerts) != 1 {
		e.t.Fatalf("want exactly one execution_stopped row on the subject, got %d", len(alerts))
	}
	a := payloadOf(e.t, alerts[0])
	if a["execution"] != exec.ID || a["task"] != task.ID || a["step"] != p["step"] || a["performer"] != p["performer"] || a["last_message"] != p["last_message"] {
		e.t.Errorf("alert payload = %v, want the execution %s, task %s and the stop's step, performer and last_message %v", a, exec.ID, task.ID, p)
	}
	return p
}

// cancelOnResolve cancels the drive's context the first time the engine's
// performer rule is asked, which is after the drive loaded the item and before it
// presented anything.
type cancelOnResolve struct {
	*fakePerformers
	cancel context.CancelFunc
}

func (c cancelOnResolve) StepPerformer(ctx context.Context, item workitem.Item, step string) (verb.PerformerResolution, error) {
	c.cancel()
	return c.fakePerformers.StepPerformer(ctx, item, step)
}

// A refusal before the first presentation, which a story's drive leaves
// unrecorded, is a stop for an execution, and nothing is parked even where the
// route could.
func TestExecutionStoppedOnRefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		perf *fakePerformers
		gate func(*env)
		want string
	}{
		{"unattended step",
			&fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerInLoop}, agents: map[string]string{"in_progress": "coder"}},
			func(*env) {}, "bind a headless CLI"},
		{"gate error on the first edge", &fakePerformers{},
			func(e *env) { e.gater.errs["in_progress"] = errors.New("gate backend unreachable") }, "gate backend unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, routeExecutionParks, workRoute)
			tc.gate(e)
			subject := e.story()
			task, exec := e.execution(subject)
			_, err := drive.Story(context.Background(), exec.ID, opts(tc.perf))
			var stopped *drive.Stopped
			if !errors.As(err, &stopped) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a *Stopped naming %q, got %v", tc.want, err)
			}
			if e.status(exec.ID) != "backlog" {
				t.Errorf("status = %q, a refusal before the first presentation parks nothing", e.status(exec.ID))
			}
			e.wantStoppedExecution(task, exec, subject, "")
			if parks, _ := e.driveRows(exec.ID); len(parks) != 0 {
				t.Errorf("want no park comment, got %d", len(parks))
			}
		})
	}
}

// A performer that fails, and a context that ends before or after the first
// presentation, each leave one stop.
func TestExecutionStoppedOnFailure(t *testing.T) {
	t.Run("performer fails", func(t *testing.T) {
		e := newEnv(t, routeNoPark, workRoute)
		subject := e.story()
		task, exec := e.execution(subject)
		perf := &fakePerformers{
			kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(), output: "rate limit hit",
			onPerform: func(workitem.Item, string, string) error { return errors.New("rate limited") },
		}
		_, err := drive.Story(context.Background(), exec.ID, opts(perf))
		var stopped *drive.Stopped
		if !errors.As(err, &stopped) {
			t.Fatalf("want a *Stopped, got %v", err)
		}
		p := e.wantStoppedExecution(task, exec, subject, "rate limit hit")
		if p["step"] != "in_progress" || p["performer"] != "executor" {
			t.Errorf("payload = %v, want the executor on in_progress", p)
		}
	})
	t.Run("context ended before the first presentation", func(t *testing.T) {
		e := newEnv(t, routeExecutionParks, workRoute)
		subject := e.story()
		task, exec := e.execution(subject)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		perf := cancelOnResolve{&fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}, cancel}
		_, err := drive.Story(ctx, exec.ID, opts(perf))
		var stopped *drive.Stopped
		if !errors.As(err, &stopped) || !errors.Is(err, context.Canceled) {
			t.Fatalf("want a *Stopped carrying the cancellation, got %v", err)
		}
		p := e.wantStoppedExecution(task, exec, subject, "")
		if !strings.Contains(p["reason"].(string), "interrupted") {
			t.Errorf("reason = %v, want the interruption named", p["reason"])
		}
	})
	t.Run("context ended mid-step", func(t *testing.T) {
		e := newEnv(t, routeNoPark, workRoute)
		subject := e.story()
		task, exec := e.execution(subject)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		perf := &fakePerformers{
			kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(), output: "working on it",
			onPerform: func(workitem.Item, string, string) error { cancel(); return nil },
		}
		_, err := drive.Story(ctx, exec.ID, opts(perf))
		var stopped *drive.Stopped
		if !errors.As(err, &stopped) {
			t.Fatalf("want a *Stopped, got %v", err)
		}
		e.wantStoppedExecution(task, exec, subject, "working on it")
	})
}

// A performer that ends on a question leaves the question on the stop and on the
// alert, where the route has no park state and where it parks the execution too.
func TestExecutionStoppedOnQuestion(t *testing.T) {
	for _, tc := range []struct {
		name, done string
		parked     bool
	}{{"no park state", routeNoPark, false}, {"parked as well", routeExecutionParks, true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.done, workRoute)
			e.gater.always["done"] = true
			subject := e.story()
			task, exec := e.execution(subject)
			perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(), output: question}
			res, err := drive.Story(context.Background(), exec.ID, opts(perf))
			var stopped *drive.Stopped
			if tc.parked {
				if err != nil || !res.Parked || e.status(exec.ID) != "blocked" {
					var why error
					if errors.As(err, &stopped) {
						why = stopped.ParkErr
					}
					t.Fatalf("want the execution parked at blocked, got %v %+v (stored %q, park: %v)", err, res, e.status(exec.ID), why)
				}
				if parks, _ := e.driveRows(exec.ID); len(parks) != 1 {
					t.Errorf("want the park comment the route's park writes, got %d", len(parks))
				}
			} else if !errors.As(err, &stopped) || e.status(exec.ID) != "in_progress" {
				t.Fatalf("want a *Stopped at in_progress, got %v (stored %q)", err, e.status(exec.ID))
			}
			p := e.wantStoppedExecution(task, exec, subject, question)
			if _, has := p["park_error"]; has == tc.parked {
				t.Errorf("park_error present = %v, want it only where the park did not happen: %v", has, p)
			}
		})
	}
}

// An execution whose story_id input is unset has no subject to alert.
func TestExecutionWithoutASubjectWritesNoAlert(t *testing.T) {
	e := newEnv(t, routeNoPark, workRoute)
	e.gater.always["done"] = true
	task, exec := e.execution("")
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(), output: question}
	if _, err := drive.Story(context.Background(), exec.ID, opts(perf)); err == nil {
		t.Fatal("want the drive stopped")
	}
	e.wantStoppedExecution(task, exec, "", question)
	all, err := e.db.Ledger.ListAll(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r.Kind == ledger.KindExecutionStopped {
			t.Errorf("an execution with no subject wrote an execution_stopped row on %s", r.StoryID)
		}
	}
}

// An execution that reaches its route's terminal step records no stop.
func TestExecutionConcludedRecordsNoStop(t *testing.T) {
	e := newEnv(t, routeDone, workRoute)
	subject := e.story()
	_, exec := e.execution(subject)
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	res, err := drive.Story(context.Background(), exec.ID, opts(perf))
	if err != nil || !res.Terminal {
		t.Fatalf("want the execution done, got %v %+v", err, res)
	}
	e.wantNoDriveRecords(exec.ID)
	if n := len(e.rows(subject, ledger.KindExecutionStopped)); n != 0 {
		t.Errorf("a concluded execution wrote %d execution_stopped rows on its subject", n)
	}
}
