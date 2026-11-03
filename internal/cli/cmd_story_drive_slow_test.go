//go:build clislow

package cli

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// AC1: the drive walks every edge of the spine through story-set and ends done,
// leaving the same transition rows a hand-driven `story set` walk leaves.
func TestSlowStoryDriveWalksTheSpineToDone(t *testing.T) {
	f := newDriveFixture(t, driveOpts{})
	driven := f.create()
	out, err := runRoot(t, "story", "drive", driven)
	if err != nil {
		t.Fatalf("drive: %v\n%s", err, out)
	}
	if got := storyStatus(t, driven); got != "done" {
		t.Fatalf("status = %q, want done\n%s", got, out)
	}
	if !strings.Contains(out, driven+" done (terminal)") {
		t.Errorf("the drive must end on its summary line, got:\n%s", out)
	}

	manual := f.create()
	for _, to := range []string{"plan", "in_progress", "integration", "done"} {
		if o, err := runRoot(t, "story", "set", manual, "--status", to); err != nil {
			t.Fatalf("manual set %s: %v\n%s", to, err, o)
		}
	}
	got, want := f.edges(driven), f.edges(manual)
	if len(got) != 4 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("a drive records one transition row per edge, as a hand walk does:\n drive:  %v\n manual: %v", got, want)
	}
	// Every performer ran: the planner and the coder on entering their steps and
	// the executor once the story was in integration.
	for _, name := range []string{"fake-planner", "fake-coder", "fake-executor"} {
		if n := len(f.log(name)); n < 1 {
			t.Errorf("%s never ran", name)
		}
	}
}

// AC2, case 3: on entering an executor-allocated step the headless executor binding
// runs once, with the step's executor skill, before the next edge is presented.
func TestSlowStoryDriveRunsTheExecutorBindingOnEnteringItsStep(t *testing.T) {
	f := newDriveFixture(t, driveOpts{})
	id := f.create()
	if out, err := runRoot(t, "story", "drive", id); err != nil {
		t.Fatalf("drive: %v\n%s", err, out)
	}
	runs := f.log("fake-executor")
	if len(runs) != 1 {
		t.Fatalf("the executor binding must run exactly once, ran %d times", len(runs))
	}
	if !strings.Contains(runs[0], "INTEGRATE-RUBRIC-MARKER") {
		t.Errorf("the step's executor skill must reach the binding as its rubric:\n%s", runs[0])
	}
	// Order on the ledger: entered integration, then performed it, then the exit edge.
	var order []string
	for _, e := range f.rows(id) {
		switch {
		case e.Kind == ledger.KindStatusTransition && strings.Contains(string(e.Payload), `"to":"integration"`):
			order = append(order, "entered")
		case e.Kind == ledger.KindAgentInvocation && strings.Contains(e.Body, `performed step "integration"`):
			order = append(order, "performed")
		case e.Kind == ledger.KindStatusTransition && strings.Contains(string(e.Payload), `"to":"done"`):
			order = append(order, "closed")
		}
	}
	if strings.Join(order, ",") != "entered,performed,closed" {
		t.Errorf("ledger order = %v, want entered, performed, closed", order)
	}
}

// AC3: when the gate rejects, the findings reach the performer the engine names
// for the step as a message BEFORE the edge is presented again, and the re-dispatch
// carries them in its payload.
func TestSlowStoryDriveRelaysARejectionToThePerformerBeforeRepresenting(t *testing.T) {
	f := newDriveFixture(t, driveOpts{gateRejects: 1})
	id := f.create()
	if out, err := runRoot(t, "story", "drive", id); err != nil {
		t.Fatalf("drive: %v\n%s", err, out)
	}
	runs := f.log("fake-planner")
	if len(runs) != 2 {
		t.Fatalf("the planner is dispatched on each presentation of the plan edge, ran %d times", len(runs))
	}
	if strings.Contains(runs[0], "NOTE-FIX-THE-PLAN") {
		t.Errorf("the first dispatch precedes any rejection, yet saw the notes:\n%s", runs[0])
	}
	if !strings.Contains(runs[1], "NOTE-FIX-THE-PLAN") || !strings.Contains(runs[1], `"messages"`) {
		t.Errorf("the re-dispatch payload must carry the relayed notes as messages:\n%s", runs[1])
	}
	var order []string
	for _, e := range f.rows(id) {
		switch {
		case e.Kind == ledger.KindReviewReject:
			order = append(order, "reject")
		case e.Kind == "agent_message" && strings.Contains(e.Body, "NOTE-FIX-THE-PLAN") && strings.Contains(string(e.Payload), `"to":"planner"`):
			order = append(order, "message")
		case e.Kind == ledger.KindAgentInvocation && strings.Contains(e.Body, `dispatched step "plan"`):
			order = append(order, "dispatch")
		}
	}
	// The rows of one presentation share the transition's timestamp, so only the
	// order ACROSS presentations is meaningful: the first presentation's dispatch
	// and reject, then the message, then the re-dispatch.
	if len(order) != 4 || order[2] != "message" || order[3] != "dispatch" ||
		order[0]+order[1] != "dispatchreject" && order[0]+order[1] != "rejectdispatch" {
		t.Errorf("order = %v, want {dispatch, reject}, message, dispatch (the message precedes the re-dispatch)", order)
	}
}

// AC3 + AC4: a reviewer that always rejects ends the story parked, with the last
// objection recorded on its timeline, and the process exits 3 after printing the
// status and the reason.
func TestSlowStoryDriveParksOnASpentRejectBudgetAndExitsThree(t *testing.T) {
	f := newDriveFixture(t, driveOpts{gateRejects: 999, rejectBudget: 2})
	id := f.create()
	out, err := runRoot(t, "story", "drive", id)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || ExitCodeOf(err) != 3 {
		t.Fatalf("a parked drive must return an ExitError{3}, got %v\n%s", err, out)
	}
	if got := storyStatus(t, id); got != "blocked" {
		t.Fatalf("status = %q, want blocked", got)
	}
	for _, want := range []string{id + " parked at blocked", "NOTE-FIX-THE-PLAN"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must carry %q:\n%s", want, out)
		}
	}
	var reason string
	for _, e := range f.rows(id) {
		if e.Kind == ledger.KindComment && strings.Contains(e.Body, "NOTE-FIX-THE-PLAN") {
			reason = e.Body
		}
	}
	if reason == "" {
		t.Errorf("the objection must be on the timeline as the park reason; rows: %v", f.edges(id))
	}
}

// A drive that reaches a wait step ends there waiting: it exits 0, not with the
// parked code, and its summary says waiting, never parked.
func TestSlowStoryDriveEndsWaitingAtAWaitStep(t *testing.T) {
	f := newDriveFixture(t, driveOpts{wait: true})
	id := f.create()
	out, err := runRoot(t, "story", "drive", id)
	if err != nil || ExitCodeOf(err) == exitParked {
		t.Fatalf("a drive that stops at a wait step succeeds, got %v (exit %d)\n%s", err, ExitCodeOf(err), out)
	}
	if got := storyStatus(t, id); got != "awaiting_release" {
		t.Fatalf("status = %q, want awaiting_release\n%s", got, out)
	}
	if !strings.Contains(out, id+" waiting at awaiting_release: ") || strings.Contains(out, "parked") {
		t.Errorf("the summary must say waiting at the wait step and never parked:\n%s", out)
	}
}

// `story queue` lists the story the drive left at the wait step, and `story
// queue withdraw` closes the entry only when given a reason.
func TestSlowStoryQueueCommandsListAndWithdraw(t *testing.T) {
	f := newDriveFixture(t, driveOpts{wait: true})
	id := f.create()
	if out, err := runRoot(t, "story", "drive", id); err != nil {
		t.Fatalf("drive: %v\n%s", err, out)
	}
	out, err := runRoot(t, "story", "queue")
	if err != nil || !strings.HasPrefix(out, id+"\tawaiting_release\t") {
		t.Fatalf("story queue must list the waiting story first: %v\n%s", err, out)
	}
	if out, err := runRoot(t, "story", "queue", "withdraw", id); err == nil {
		t.Fatalf("a withdrawal with no --reason is refused:\n%s", out)
	}
	if out, err := runRoot(t, "story", "queue", "withdraw", id, "--reason", "release postponed"); err != nil {
		t.Fatalf("withdraw: %v\n%s", err, out)
	}
	if out, err := runRoot(t, "story", "queue"); err != nil || !strings.Contains(out, "# queue is empty") {
		t.Fatalf("the withdrawn entry leaves the queue: %v\n%s", err, out)
	}
	var reason string
	for _, e := range f.rows(id) {
		if e.Kind == ledger.KindQueueWithdrawn {
			reason = e.Body
		}
	}
	if !strings.Contains(reason, "release postponed") {
		t.Errorf("the reason must be recorded on the story, got %q", reason)
	}
}

// AC5: a workflow that offers no park state to hold the question cannot park the
// story, so the drive records the stop as a drive-stopped row and exits 1, printing
// where and why it stopped.
func TestSlowStoryDriveWithNoParkStateStops(t *testing.T) {
	f := newDriveFixture(t, driveOpts{gateRejects: 999, rejectBudget: 1, noPark: true})
	id := f.create()
	out, err := runRoot(t, "story", "drive", id)
	if err == nil {
		t.Fatalf("a drive that must park but cannot has to stop:\n%s", out)
	}
	line := id + " stopped at backlog: step plan, performer planner:"
	if ExitCodeOf(err) != 1 || !strings.Contains(out, line) || !strings.Contains(err.Error(), line) {
		t.Errorf("want exit 1 and %q on the output, got %d: %v\n%s", line, ExitCodeOf(err), err, out)
	}
	if got := storyStatus(t, id); got == "blocked" {
		t.Errorf("no park state exists, yet the story is %q", got)
	}
	var stops int
	for _, e := range f.rows(id) {
		if e.Kind == ledger.KindDriveStopped {
			stops++
			if !strings.Contains(e.Body, "NOTE-FIX-THE-PLAN") || !strings.Contains(string(e.Payload), "no park state") {
				t.Errorf("the stop row must carry the objection and why it could not park: %q %s", e.Body, e.Payload)
			}
		}
	}
	if stops != 1 {
		t.Errorf("want exactly one drive-stopped row, got %d", stops)
	}
}

// AC5: an agent caller's drive is handed off as a whole — the whole walk runs in
// one detached copy, and the caller is answered with its outcome.
func TestSlowStoryDriveHandsOffForAnAgentCaller(t *testing.T) {
	f := newDriveFixture(t, driveOpts{})
	id := f.create()
	args := []string{"story", "drive", id}
	useGateHandOff(t, "120s", args)
	out, err := runRoot(t, args...)
	if err != nil && !errors.Is(err, errGateHandedOff) {
		t.Fatalf("drive: %v\n%s", err, out)
	}
	if got := storyStatus(t, id); got != "done" {
		t.Fatalf("the detached drive must walk the story to done, status %q\n%s", got, out)
	}
	store := gateStoreForTest(t)
	entries, _ := os.ReadDir(store.Dir())
	if len(entries) != 1 {
		t.Fatalf("the whole drive is one handed-off run, got %d handles", len(entries))
	}
	if m, _ := store.Meta(entries[0].Name()); m.Verb != "story-drive" || m.Story != id {
		t.Fatalf("handle = %+v", m)
	}
}
