package verb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// executionRunWF governs stories by the wildcard route and task executions by
// their own: backlog → in_progress (performing, executor) → done.
var executionRunWF = routeHalves(
	`["*"]
obligations = ["raised", "planned", "coded", "closed"]
park = { state = "blocked" }

[execution]
obligations = ["raised", "run", "run-closed"]
`,
	`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "executor"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]

[run]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[run-closed]
status = "done"
terminal = true
requires = ["run"]
`)

// newRunningExecution creates a task and one execution of it, moved into its
// performing in_progress state.
func newRunningExecution(t *testing.T) workitem.Item {
	t.Helper()
	var task, run workitem.Item
	json.Unmarshal(call(t, "task-create", map[string]any{"title": "Release"}), &task)
	json.Unmarshal(call(t, "execution-create", map[string]any{"title": "release run", "parent_id": task.ID}), &run)
	if run.Kind != workitem.KindExecution {
		t.Fatalf("execution-create kind = %q", run.Kind)
	}
	json.Unmarshal(call(t, "execution-set", map[string]any{"id": run.ID, "status": "in_progress"}), &run)
	if run.Status != "in_progress" {
		t.Fatalf("execution status = %q, want in_progress", run.Status)
	}
	return run
}

// TestExecutionMoveIntoPerformingStateTakesLease (sty_931026b3 AC1/AC4): moving
// an execution into its performing state, by either verb, takes a live lease so
// the gates see the run as engaged; the lease never occupies the story seat, and
// the move into done frees it.
func TestExecutionMoveIntoPerformingStateTakesLease(t *testing.T) {
	ctx := context.Background()
	for _, v := range []string{"execution-set", "story-set"} {
		t.Run(v, func(t *testing.T) {
			db := wireWithWorkflowsStore(t, executionRunWF)
			var task, run workitem.Item
			json.Unmarshal(call(t, "task-create", map[string]any{"title": "Release"}), &task)
			json.Unmarshal(call(t, "execution-create", map[string]any{"title": "release run", "parent_id": task.ID}), &run)
			if _, err := db.Leases.Get(ctx, run.ID); err == nil {
				t.Fatalf("a backlog execution must hold no lease")
			}

			json.Unmarshal(call(t, v, map[string]any{"id": run.ID, "status": "in_progress"}), &run)
			if run.Status != "in_progress" {
				t.Fatalf("status = %q, want in_progress", run.Status)
			}
			l, err := db.Leases.Get(ctx, run.ID)
			if err != nil || !lease.Alive(l, time.Now().UTC()) {
				t.Fatalf("no live lease for %s after the move into in_progress: %v", run.ID, err)
			}
			if l.StorySeat {
				t.Fatalf("an execution's lease must not occupy the story seat")
			}

			json.Unmarshal(call(t, v, map[string]any{"id": run.ID, "status": "done"}), &run)
			if run.Status != "done" {
				t.Fatalf("status = %q, want done", run.Status)
			}
			if _, err := db.Leases.Get(ctx, run.ID); err == nil {
				t.Fatalf("the move into done must release the lease for %s", run.ID)
			}
			seats, err := verb.Dispatch(ctx, "story-seat-list", nil)
			if err != nil {
				t.Fatalf("story-seat-list: %v", err)
			}
			if strings.Contains(string(seats), run.ID) {
				t.Fatalf("seat list still names %s after close: %s", run.ID, seats)
			}
		})
	}
}

// TestSameStatusSetSeatsAnExecutionWhoseSeatWasDropped (sty_7f93ae45 AC1/AC3):
// the command the edit gate's dropped-seat advice names — `story set <exe>
// --status <its performing status>` — grants the execution a lease carrying the
// calling session and tree, and the seat listing shows it.
func TestSameStatusSetSeatsAnExecutionWhoseSeatWasDropped(t *testing.T) {
	t.Setenv("SATELLE_SESSION", "release-run")
	db := wireWithWorkflowsStore(t, executionRunWF)
	run := newRunningExecution(t)

	if _, err := verb.Dispatch(context.Background(), "story-seat-release", mustJSONLease(map[string]any{"id": run.ID})); err != nil {
		t.Fatalf("story-seat-release: %v", err)
	}
	if _, err := db.Leases.Get(context.Background(), run.ID); err == nil {
		t.Fatalf("lease for %s should be gone after release", run.ID)
	}

	for _, v := range []string{"story-set", "execution-set"} {
		json.Unmarshal(call(t, v, map[string]any{"id": run.ID, "status": "in_progress"}), &run)
		if run.Status != "in_progress" {
			t.Fatalf("%s: status = %q", v, run.Status)
		}
		l, err := db.Leases.Get(context.Background(), run.ID)
		if err != nil {
			t.Fatalf("%s: no lease for %s after the re-engage: %v", v, run.ID, err)
		}
		if l.SessionID != "release-run" {
			t.Fatalf("%s: lease session = %q, want release-run", v, l.SessionID)
		}
		if l.StorySeat {
			t.Fatalf("%s: an execution takes a lease, never the story seat", v)
		}
		seats, err := verb.Dispatch(context.Background(), "story-seat-list", nil)
		if err != nil {
			t.Fatalf("story-seat-list: %v", err)
		}
		if !strings.Contains(string(seats), run.ID) {
			t.Fatalf("%s: seat list does not name %s: %s", v, run.ID, seats)
		}
		if _, err := verb.Dispatch(context.Background(), "story-seat-release", mustJSONLease(map[string]any{"id": run.ID})); err != nil {
			t.Fatalf("story-seat-release: %v", err)
		}
	}
}

// TestSameStatusSetOnAnUnseatableExecutionFails (sty_7f93ae45 AC3): another
// owner's live lease on the execution refuses the re-engage non-zero, and a
// same-status set at a non-performing state says the execution cannot be seated
// instead of reporting plain success.
func TestSameStatusSetOnAnUnseatableExecutionFails(t *testing.T) {
	db := wireWithWorkflowsStore(t, executionRunWF)
	run := newRunningExecution(t)

	// Another live owner holds the execution's lease.
	if err := db.Leases.ForceRelease(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.Leases.AcquireWith(context.Background(), lease.AcquireOpts{
		ItemID: run.ID, Kind: string(workitem.KindExecution), Owner: "someone-else", State: "in_progress",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := dispatchRaw(t, "story-set", map[string]any{"id": run.ID, "status": "in_progress"})
	if err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("re-engage while another owner holds the lease: want a refusal, got %v (%s)", err, out)
	}

	// A same-status set at a state that is not performing cannot seat it.
	var idle workitem.Item
	var task workitem.Item
	json.Unmarshal(call(t, "task-create", map[string]any{"title": "Idle"}), &task)
	json.Unmarshal(call(t, "execution-create", map[string]any{"title": "idle run", "parent_id": task.ID}), &idle)
	_, err = dispatchRaw(t, "execution-set", map[string]any{"id": idle.ID, "status": idle.Status})
	if err == nil || !strings.Contains(err.Error(), "could not seat "+idle.ID) {
		t.Fatalf("same-status set at %q: want a could-not-seat refusal, got %v", idle.Status, err)
	}
}
