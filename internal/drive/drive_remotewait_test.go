package drive_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/drive"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/runstatus"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A step that waits on a team remote's run: the drive reads the run, reports
// where it stands, and on a conclusion takes the edge the step maps the outcome
// to. The statuses, the remote and the outcomes are fixture data.

const remoteWaitDone = `["*"]
obligations = ["raised", "coded", "opened", "closed"]
park = { state = "blocked" }
cancel = { state = "cancelled" }
recover = { step = "in_progress", from = ["review"] }
`

func remoteWaitRoute(codedAgent string) string {
	return `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "` + codedAgent + `"
requires = ["raised"]

[opened]
status = "review"
remote_wait = { remote = "pr-team", on = { accept = "done", reject = "in_progress", stopped = "blocked" } }
requires = ["coded"]

[closed]
status = "done"
terminal = true
requires = ["opened"]
`
}

// fakeRuns is the runstatus.Source a test scripts.
type fakeRuns struct {
	mu    sync.Mutex
	run   runstatus.Run
	ok    bool
	err   error
	calls int
}

func (f *fakeRuns) set(run runstatus.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run, f.ok, f.err = run, true, nil
}

func (f *fakeRuns) StoryRun(_ context.Context, remote string, _ string) (runstatus.Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if remote != "pr-team" {
		return runstatus.Run{}, false, errors.New("asked for the wrong remote: " + remote)
	}
	return f.run, f.ok, f.err
}

// messageDispatcher records the messages the story holds each time the named
// agent is dispatched, which is what its payload would carry.
type messageDispatcher struct {
	e    *env
	seen [][]string
}

func (d *messageDispatcher) DispatchExecutor(_ context.Context, it workitem.Item, _ string) (verb.DispatchResult, error) {
	var bodies []string
	for _, m := range d.e.rows(it.ID, ledger.KindAgentMessage) {
		bodies = append(bodies, m.Body)
	}
	d.seen = append(d.seen, bodies)
	return verb.DispatchResult{Dispatched: true, Agent: "coder", Command: "fake-coder", Skill: "coder"}, nil
}

// waitingAt walks a fresh story to the waiting step with a queued run and returns
// its id, so a test then scripts the conclusion.
func waitingAt(t *testing.T, e *env, perf drive.Performers, runs *fakeRuns) string {
	t.Helper()
	id := e.story()
	runs.set(runstatus.Run{State: runstatus.Queued, Position: 2})
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs})
	if err != nil || !res.Waiting || res.Status != "review" {
		t.Fatalf("walk to the wait: %v %+v", err, res)
	}
	return id
}

func TestDriveRemoteWaitQueuedWaitsAndRecordsNothing(t *testing.T) {
	e := newEnv(t, remoteWaitDone, remoteWaitRoute("executor"))
	runs := &fakeRuns{}
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	id := waitingAt(t, e, perf, runs)

	var out bytes.Buffer
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs, Out: &out})
	if err != nil || !res.Waiting || res.Parked || res.Terminal || len(res.Moves) != 0 {
		t.Fatalf("a queued run waits: %v %+v", err, res)
	}
	if res.Reason != "queued with pr-team (position 2)" {
		t.Errorf("reason = %q, want where the run stands", res.Reason)
	}
	if got := e.status(id); got != "review" {
		t.Errorf("status = %q, want review", got)
	}
	if !strings.Contains(out.String(), "queued with pr-team (position 2)") {
		t.Errorf("the drive should print the run line:\n%s", out.String())
	}
	if parks, stops := e.driveRows(id); len(parks) != 0 || len(stops) != 0 {
		t.Errorf("waiting records no stop: %d parks, %d stops", len(parks), len(stops))
	}
}

// An accept takes the mapped edge through the normal gates: the gate rejects the
// first presentation and accepts the second, and the move is recorded.
func TestDriveRemoteWaitAccept(t *testing.T) {
	e := newEnv(t, remoteWaitDone, remoteWaitRoute("executor"))
	runs := &fakeRuns{}
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	id := waitingAt(t, e, perf, runs)

	e.gater.rejects["done"] = 1
	runs.set(runstatus.Run{State: runstatus.Concluded, Outcome: "accept", Findings: "reviewed cleanly"})
	var moves []drive.Move
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs, OnMove: func(m drive.Move) { moves = append(moves, m) }})
	if err != nil || !res.Terminal || res.Status != "done" {
		t.Fatalf("an accept reaches the mapped status: %v %+v", err, res)
	}
	if len(res.Moves) != 1 || res.Moves[0] != (drive.Move{From: "review", To: "done"}) || len(moves) != 1 {
		t.Errorf("moves = %+v / %+v, want review → done", res.Moves, moves)
	}
	if e.gater.seen["done"] != 2 {
		t.Errorf("the edge must be presented through the gates (reject, then accept), saw %d presentations", e.gater.seen["done"])
	}
	if msgs := e.rows(id, ledger.KindAgentMessage); len(msgs) != 0 {
		t.Errorf("an agent-less target has nobody to relay to: %d messages", len(msgs))
	}
}

func TestDriveRemoteWaitRejectRelaysToTheExecutorAsNotes(t *testing.T) {
	e := newEnv(t, remoteWaitDone, remoteWaitRoute("executor"))
	var notes []string
	perf := &fakePerformers{
		kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(),
		onPerform: func(_ workitem.Item, _, n string) error { notes = append(notes, n); return nil },
	}
	runs := &fakeRuns{}
	id := waitingAt(t, e, perf, runs)

	runs.set(runstatus.Run{State: runstatus.Concluded, Outcome: "reject", Findings: "missing test"})
	if _, err := drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs}); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if len(notes) != 2 || notes[0] != "" || !strings.Contains(notes[1], "missing test") {
		t.Errorf("the re-run carries the findings, the first run none: %q", notes)
	}
}

// A mapped outcome whose target is the park state goes through the drive's one
// park: the story is blocked, a drive park row exists and the recorded reason
// names the remote, the outcome and the findings. The park edge is not presented
// as an advance.
func TestDriveRemoteWaitPark(t *testing.T) {
	e := newEnv(t, remoteWaitDone, remoteWaitRoute("executor"))
	runs := &fakeRuns{}
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	id := waitingAt(t, e, perf, runs)

	runs.set(runstatus.Run{State: runstatus.Concluded, Outcome: "stopped", Findings: "denied by a reviewer"})
	var out bytes.Buffer
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs, Out: &out})
	if err != nil || !res.Parked || res.Status != "blocked" {
		t.Fatalf("a stopped run parks the story: %v %+v", err, res)
	}
	if got := e.status(id); got != "blocked" {
		t.Errorf("status = %q, want blocked", got)
	}
	parks, _ := e.driveRows(id)
	if len(parks) != 1 {
		t.Fatalf("want one drive park row, got %d", len(parks))
	}
	for _, want := range []string{"pr-team", "stopped", "denied by a reviewer"} {
		if !strings.Contains(parks[0].Body, want) || !strings.Contains(res.Reason, want) {
			t.Errorf("the recorded reason must name %q: row %q, result %q", want, parks[0].Body, res.Reason)
		}
	}
	if strings.Contains(out.String(), "presenting review → blocked") {
		t.Errorf("the park edge must not be presented as an advance:\n%s", out.String())
	}
}

func TestDriveRemoteWaitUnmappedOutcomeParks(t *testing.T) {
	e := newEnv(t, remoteWaitDone, remoteWaitRoute("executor"))
	runs := &fakeRuns{}
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	id := waitingAt(t, e, perf, runs)

	runs.set(runstatus.Run{State: runstatus.Concluded, Outcome: "weird"})
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs})
	if err != nil || !res.Parked {
		t.Fatalf("an unmapped outcome parks: %v %+v", err, res)
	}
	if !strings.Contains(res.Reason, `"weird"`) || !strings.Contains(res.Reason, "maps to no edge") {
		t.Errorf("reason = %q, want the unmapped outcome named", res.Reason)
	}
}

func TestDriveRemoteWaitSourceErrorRefusesWithoutMoving(t *testing.T) {
	e := newEnv(t, remoteWaitDone, remoteWaitRoute("executor"))
	runs := &fakeRuns{}
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	id := waitingAt(t, e, perf, runs)

	runs.mu.Lock()
	runs.err = errors.New("satelle.dev is unreachable")
	runs.mu.Unlock()
	if _, err := drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs}); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("a run that cannot be read refuses the drive, got %v", err)
	}
	if got := e.status(id); got != "review" {
		t.Errorf("status = %q, want review untouched", got)
	}

	if _, err := drive.Story(context.Background(), id, drive.Options{Performers: perf}); err == nil || !strings.Contains(err.Error(), "no run source") {
		t.Errorf("a wait with no run source wired must refuse, got %v", err)
	}
}

// A conclusion is acted on once across drive invocations: a later `story drive`
// that reads the same concluded run waits instead of bouncing the story back to
// recovery again, and a new run (another identity) is acted on afresh.
func TestDriveRemoteWaitActsOnAConclusionOnce(t *testing.T) {
	e := newEnv(t, remoteWaitDone, remoteWaitRoute("executor"))
	var notes []string
	perf := &fakePerformers{
		kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(),
		onPerform: func(_ workitem.Item, _, n string) error { notes = append(notes, n); return nil },
	}
	runs := &fakeRuns{}
	id := waitingAt(t, e, perf, runs)
	drv := func() drive.Result {
		t.Helper()
		res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs})
		if err != nil {
			t.Fatalf("drive: %v", err)
		}
		return res
	}

	runs.set(runstatus.Run{ID: "run_1", ConcludedAt: "2026-10-24T01:00:00Z", State: runstatus.Concluded, Outcome: "reject", Findings: "missing test"})
	if res := drv(); len(res.Moves) == 0 || res.Moves[0] != (drive.Move{From: "review", To: "in_progress"}) {
		t.Fatalf("first drive should take the reject edge: %+v", res)
	}
	performed := len(notes)

	// The story is back at the wait with the same stale conclusion.
	res := drv()
	if !res.Waiting || len(res.Moves) != 0 || res.Status != "review" || len(notes) != performed {
		t.Fatalf("a second drive must not act on the same conclusion: %+v (performed %d→%d)", res, performed, len(notes))
	}
	if !strings.Contains(res.Reason, "already acted on") {
		t.Errorf("reason = %q, want it to say the conclusion was already acted on", res.Reason)
	}
	if got := len(e.rows(id, ledger.KindRemoteWaitActed)); got != 1 {
		t.Errorf("remote_wait_acted rows = %d, want 1", got)
	}

	// A new run's conclusion is a new event.
	runs.set(runstatus.Run{ID: "run_2", ConcludedAt: "2026-10-24T02:00:00Z", State: runstatus.Concluded, Outcome: "reject", Findings: "still missing"})
	if res := drv(); len(res.Moves) == 0 || res.Moves[0] != (drive.Move{From: "review", To: "in_progress"}) {
		t.Fatalf("a new run's conclusion should be acted on: %+v", res)
	}
}

// An edge that was taken and then failed (the performer of the step it led to
// errored) still records the conclusion as acted on, so the next drive does not
// take the same edge again from it; an edge the gate refused outright moved
// nothing and records nothing, so it is retried.
func TestDriveRemoteWaitRecordsActedWhenTheStoryLeftTheStepDespiteAnError(t *testing.T) {
	e := newEnv(t, remoteWaitDone, remoteWaitRoute("executor"))
	calls := 0
	perf := &fakePerformers{
		kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless(),
		onPerform: func(workitem.Item, string, string) error {
			calls++
			if calls > 1 {
				return errors.New("rate limited")
			}
			return nil
		},
	}
	runs := &fakeRuns{}
	id := waitingAt(t, e, perf, runs)

	runs.set(runstatus.Run{ID: "run_1", ConcludedAt: "t1", State: runstatus.Concluded, Outcome: "reject", Findings: "missing test"})
	_, _ = drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs})
	if got := e.status(id); got == "review" {
		t.Fatalf("the reject edge should have moved the story off review, status = %q", got)
	}
	if got := len(e.rows(id, ledger.KindRemoteWaitActed)); got != 1 {
		t.Errorf("remote_wait_acted rows = %d, want 1 after an edge taken and then failed", got)
	}
}

func TestDriveRemoteWaitRecordsNothingWhenTheEdgeWasRefused(t *testing.T) {
	e := newEnv(t, remoteWaitDone, remoteWaitRoute("executor"))
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	runs := &fakeRuns{}
	id := waitingAt(t, e, perf, runs)

	e.gater.errs["in_progress"] = errors.New("gate unavailable")
	runs.set(runstatus.Run{ID: "run_1", ConcludedAt: "t1", State: runstatus.Concluded, Outcome: "reject", Findings: "missing test"})
	if _, err := drive.Story(context.Background(), id, drive.Options{Performers: perf, Runs: runs}); err == nil {
		t.Fatal("a refused edge should end the drive with an error")
	}
	if got := e.status(id); got != "review" {
		t.Fatalf("status = %q, want review", got)
	}
	if got := len(e.rows(id, ledger.KindRemoteWaitActed)); got != 0 {
		t.Errorf("remote_wait_acted rows = %d, want 0 for an edge that did not move the story", got)
	}
}
