package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
)

// sty_216d5205: the PROGRESS chip counts the gates on a step's edge — declared,
// passed, failed and, while the exit gates run, still running — and spells the
// counts and the round history in its tooltip.

// gatedSpec is a route whose edges declare gates: three into plan, two into
// in_progress plus a tag-scoped gate (rev-c, surface:ui) and a conditional one
// (rev-d, when=trig), and four into done.
func gatedSpec() wfdot.Spec {
	return wfdot.Spec{
		States: []wfdot.State{
			{Name: "backlog", Shape: "Mdiamond"},
			{Name: "plan", Agent: "planner"},
			{Name: "in_progress", Agent: "coder"},
			{Name: "done", Shape: "Msquare"},
			{Name: "blocked", Agent: "reviewer", From: []string{"*"}},
			{Name: "gate-c", Skill: "rev-c", Agent: "reviewer", On: []string{"in_progress"}, AppliesTo: []string{"surface:ui"}},
			{Name: "gate-d", Skill: "rev-d", Agent: "reviewer", On: []string{"in_progress"}, When: "trig"},
		},
		Transitions: []wfdot.Transition{
			{From: "backlog", To: "plan", Skills: []string{"arch", "plan-review", "intent"}},
			{From: "plan", To: "in_progress", Skills: []string{"rev-a", "rev-b"}},
			{From: "in_progress", To: "done", Skills: []string{"ac", "scope", "build", "fmt"}},
			{From: "plan", To: "blocked"},
			{From: "in_progress", To: "blocked"},
		},
	}
}

// evS is a verdict row of one gate (skill) in one round (attempt).
func evS(kind, from, to, attempt, skill string) ledger.Entry {
	p, _ := json.Marshal(map[string]string{"from": from, "to": to, "attempt": attempt, "skill": skill})
	return ledger.Entry{Kind: kind, Payload: p}
}

// whenSkip is the engine's scoped-gate-skipped telemetry row for a `when` gate
// that did not trigger.
func whenSkip(skill, to string) ledger.Entry {
	p, _ := json.Marshal(map[string]any{"kind": "scoped-gate-skipped",
		"data": map[string]any{"reason": "when", "skill": skill, "to": to, "when": "trig"}})
	return ledger.Entry{Kind: ledger.KindTelemetryEvent, Payload: p}
}

// enteredPlan is a story that entered plan after one rejected and one accepted
// readiness round of its three gates.
func enteredPlan() []ledger.Entry {
	return []ledger.Entry{
		evS(ledger.KindReviewReject, "backlog", "plan", "r1", "arch"),
		evS(ledger.KindReviewAccept, "backlog", "plan", "r1", "plan-review"),
		evS(ledger.KindReviewAccept, "backlog", "plan", "r1", "intent"),
		evS(ledger.KindReviewAccept, "backlog", "plan", "r2", "arch"),
		evS(ledger.KindReviewAccept, "backlog", "plan", "r2", "plan-review"),
		evS(ledger.KindReviewAccept, "backlog", "plan", "r2", "intent"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
	}
}

func counts(g *gateCountsVM) [4]int {
	if g == nil {
		return [4]int{-1, -1, -1, -1}
	}
	return [4]int{g.Gates, g.Passed, g.Failed, g.Running}
}

// AC1: gates running on the outgoing edge — the seat's gate activity reports
// some returned and others not — count on the current chip, the running count live.
func TestInFlightGateCountsFromSeatActivity(t *testing.T) {
	seat := &seatGateVM{Total: 4, Passed: 1, Failed: 1}
	stages := buildStages(enteredPlan(), "plan", true, noStep, gatedSpec(), nil, seat)
	cur := stageNamed(stages, "plan")
	if cur.State != "current" || counts(cur.Gates) != [4]int{4, 1, 1, 2} || !cur.Gates.Live {
		t.Fatalf("plan chip = %+v gates %+v, want 4 gates, 1 passed, 1 failed, 2 running, live", cur, cur.Gates)
	}
	if cur.Gates.Edge != "plan → in_progress" {
		t.Errorf("edge = %q, want the route's next step", cur.Gates.Edge)
	}
}

// AC1: a new round with nothing returned yet counts every gate as running, not
// the previous rejected round's verdicts.
func TestInFlightNewRoundNothingReported(t *testing.T) {
	es := append(enteredPlan(),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "o1", "rev-a"),
		evS(ledger.KindReviewReject, "plan", "in_progress", "o1", "rev-b"))
	cur := stageNamed(buildStages(es, "plan", true, noStep, gatedSpec(), nil, &seatGateVM{Total: 4}), "plan")
	if counts(cur.Gates) != [4]int{4, 0, 0, 4} || !cur.Gates.Live {
		t.Fatalf("plan gates = %+v, want 4 gates, 0 passed, 0 failed, 4 running", cur.Gates)
	}
}

// AC1: a seat in flight on the step performer's dispatch is not gates running —
// the chip shows the entry edge's counts and no running count.
func TestPerformerDispatchIsNotGatesRunning(t *testing.T) {
	seat := seatPayload{ID: "sty_1", InFlight: true, Activity: "perform:plan", ActivityTotal: 1}
	if g := gateActivity(seat); g != nil {
		t.Fatalf("a performer's stamp read as gates running: %+v", g)
	}
	stages := buildStages(enteredPlan(), "plan", true, noStep, gatedSpec(), nil, gateActivity(seat))
	cur := stageNamed(stages, "plan")
	if counts(cur.Gates) != [4]int{3, 3, 0, 0} || cur.Gates.Live {
		t.Fatalf("plan gates = %+v, want the entry edge's 3 passed of 3", cur.Gates)
	}
	_, progress := renderRowCells(t, "plan", stages)
	if strings.Contains(progress, `class="run`) {
		t.Errorf("a performer dispatch rendered a running count: %q", progress)
	}
	// Stale or settled seats are not running either.
	for _, s := range []seatPayload{
		{InFlight: true, Stale: true, ActivityGates: true, ActivityTotal: 4},
		{InFlight: false, ActivityGates: true, ActivityTotal: 4},
	} {
		if g := gateActivity(s); g != nil {
			t.Errorf("seat %+v read as gates running", s)
		}
	}
}

// AC1: the rendered PROGRESS cell of a story with exit gates running holds ONE
// gate count — the outgoing edge's — with the running count carrying the live
// class, and no pending-gate mark.
func TestCurrentChipShowsOnlyOutgoingGates(t *testing.T) {
	stages := buildStages(enteredPlan(), "plan", true, noStep, gatedSpec(), nil, &seatGateVM{Total: 4, Passed: 1, Failed: 1})
	_, progress := renderRowCells(t, "plan", stages)
	if n := strings.Count(progress, `class="gates"`); n != 1 {
		t.Fatalf("want exactly one gate count, got %d: %q", n, progress)
	}
	want := `plan <b class="gates">4</b> <b class="ok">1</b> <b class="rej">1</b> <b class="run live">2</b></span>`
	if !strings.Contains(progress, want) {
		t.Errorf("progress cell = %q, want %q", progress, want)
	}
	if strings.Contains(progress, "pending-gate") {
		t.Errorf("progress cell still carries a pending-gate: %q", progress)
	}
}

// AC1: the live class pulses — it reuses the chip's pulse animation.
func TestRunningCountPulses(t *testing.T) {
	raw, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	i := strings.Index(css, ".stage-chip .run.live")
	if i < 0 {
		t.Fatal("app.css has no .stage-chip .run.live rule")
	}
	rule := css[i : i+strings.Index(css[i:], "}")]
	if !strings.Contains(rule, "animation: review-pulse") {
		t.Errorf(".run.live rule = %q, want the review-pulse animation", rule)
	}
	if strings.Contains(css, ".pending-gate") {
		t.Error("app.css still styles the removed .pending-gate")
	}
}

// AC2: a completed step shows its gate count with every gate passed and no
// running count, even when an earlier round on the edge was rejected.
func TestCompletedStageShowsPassedEqualsGates(t *testing.T) {
	es := append(enteredPlan(),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i1", "rev-a"),
		ev(ledger.KindStatusTransition, "plan", "in_progress"))
	stages := buildStages(es, "in_progress", false, noStep, gatedSpec(), nil, nil)
	plan := stageNamed(stages, "plan")
	if plan.State != "done" || counts(plan.Gates) != [4]int{3, 3, 0, 0} || plan.Gates.Live {
		t.Fatalf("plan chip = %+v gates %+v, want done with 3 gates, 3 passed, 0 failed", plan, plan.Gates)
	}
	_, progress := renderRowCells(t, "in_progress", stages)
	if !strings.Contains(progress, `plan <b class="gates">3</b> <b class="ok">3</b> <b class="rej">0</b></span>`) {
		t.Errorf("completed plan chip not rendered with its counts: %q", progress)
	}
	if strings.Contains(progress, `class="run`) {
		t.Errorf("a completed step rendered a running count: %q", progress)
	}
}

// AC3: a scoped gate applies_to excludes for the story's tags, and a `when` gate
// whose skip row falls in the latest round, are not counted; a control with the
// tag and a triggered `when` gate counts both.
func TestGateCountExcludesUntriggeredAndTagSkipped(t *testing.T) {
	skipped := append(enteredPlan(),
		evS(ledger.KindReviewReject, "plan", "in_progress", "i1", "rev-a"), // an earlier round
		whenSkip("rev-d", "in_progress"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-a"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-b"),
		ev(ledger.KindStatusTransition, "plan", "in_progress"))
	cur := stageNamed(buildStages(skipped, "in_progress", false, noStep, gatedSpec(), []string{"surface:cli"}, nil), "in_progress")
	if counts(cur.Gates) != [4]int{2, 2, 0, 0} {
		t.Fatalf("in_progress gates = %+v, want 2 (rev-c tag-skipped, rev-d untriggered)", cur.Gates)
	}

	// A skip row from an EARLIER round does not exclude a gate the latest round ran.
	triggered := append(enteredPlan(),
		whenSkip("rev-d", "in_progress"),
		evS(ledger.KindReviewReject, "plan", "in_progress", "i1", "rev-a"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-a"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-b"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-d"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-c"),
		ev(ledger.KindStatusTransition, "plan", "in_progress"))
	cur = stageNamed(buildStages(triggered, "in_progress", false, noStep, gatedSpec(), []string{"surface:ui"}, nil), "in_progress")
	if counts(cur.Gates) != [4]int{4, 4, 0, 0} {
		t.Fatalf("control gates = %+v, want 4 (rev-c by tag, rev-d triggered)", cur.Gates)
	}
	// Without the tag but with rev-d triggered: rev-c alone leaves the count.
	cur = stageNamed(buildStages(triggered, "in_progress", false, noStep, gatedSpec(), nil, nil), "in_progress")
	if counts(cur.Gates) != [4]int{3, 3, 0, 0} {
		t.Fatalf("untagged gates = %+v, want 3 (rev-c tag-skipped)", cur.Gates)
	}
}

// AC4: the tooltip spells the counts in words and lists the round history the
// chip no longer shows — for an in-flight step and for a completed one.
func TestChipTooltipSpellsGatesAndRounds(t *testing.T) {
	inFlight := append(enteredPlan(),
		evS(ledger.KindReviewReject, "plan", "in_progress", "o1", "rev-a"),
		evS(ledger.KindReviewReject, "plan", "in_progress", "o2", "rev-b"))
	cur := stageNamed(buildStages(inFlight, "plan", true, noStep, gatedSpec(), nil, &seatGateVM{Total: 4, Passed: 1, Failed: 1}), "plan")
	want := "plan → in_progress: 4 gates, 1 passed, 1 failed, 2 still running; rounds: 2 rejected" +
		" · plan (current stage, entered from backlog): 3 gates, 3 passed, 0 failed; rounds: 1 accepted, 1 rejected"
	if cur.Title != want {
		t.Errorf("in-flight title =\n  %q\nwant\n  %q", cur.Title, want)
	}

	completed := append(enteredPlan(),
		evS(ledger.KindReviewReject, "plan", "in_progress", "i1", "rev-a"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-a"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-b"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-d"),
		ev(ledger.KindStatusTransition, "plan", "in_progress"),
		ev(ledger.KindStatusTransition, "in_progress", "done"))
	stages := buildStages(completed, "done", false, noStep, gatedSpec(), nil, nil)
	if got, want := stageNamed(stages, "in_progress").Title, "in_progress (entered from plan): 3 gates, 3 passed, 0 failed; rounds: 1 accepted, 1 rejected"; got != want {
		t.Errorf("completed title =\n  %q\nwant\n  %q", got, want)
	}
}

// AC1: the seat row keys cli.listSeatsJSON pushes (TestListSeatsJSONCarriesGateActivity
// pins that half) decode into the running gate round; a mismatch fails one of the two.
func TestSeatPayloadDecodesGateActivity(t *testing.T) {
	row := `{"id":"sty_1","in_flight":true,"stale":false,"activity":"rev-b","activity_index":3,` +
		`"activity_total":4,"activity_gates":true,"activity_passed":1,"activity_failed":1}`
	var seat seatPayload
	if err := json.Unmarshal([]byte(row), &seat); err != nil {
		t.Fatal(err)
	}
	if g := gateActivity(seat); g == nil || *g != (seatGateVM{Total: 4, Passed: 1, Failed: 1}) {
		t.Fatalf("gateActivity = %+v, want 4 gates, 1 passed, 1 failed", g)
	}
	// A row pushed by a binary that predates the stamp carries no gate keys.
	var old seatPayload
	_ = json.Unmarshal([]byte(`{"id":"sty_1","in_flight":true,"activity":"gates","activity_total":4}`), &old)
	if g := gateActivity(old); g != nil {
		t.Errorf("a row with no activity_gates read as gates running: %+v", g)
	}
}
