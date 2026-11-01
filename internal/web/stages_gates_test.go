package web

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
)

// sty_216d5205, sty_0b1abacc: the PROGRESS chip counts a step's EXIT gates — the
// gates on the edge leaving it — and how many times they rejected the step's
// work; the step being worked shows them not yet run or still running.

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

// exitSpec is the route sty_0b1abacc's criteria name: backlog → plan (3
// reviewers) → in_progress (1) → integration (2) → done (1).
func exitSpec() wfdot.Spec {
	return wfdot.Spec{
		States: []wfdot.State{
			{Name: "backlog", Shape: "Mdiamond"},
			{Name: "plan", Agent: "planner"},
			{Name: "in_progress", Agent: "coder"},
			{Name: "integration", Agent: "executor"},
			{Name: "done", Shape: "Msquare"},
			{Name: "blocked", Agent: "reviewer", From: []string{"*"}},
		},
		Transitions: []wfdot.Transition{
			{From: "backlog", To: "plan", Skills: []string{"arch", "plan-review", "intent"}},
			{From: "plan", To: "in_progress", Skills: []string{"definition"}},
			{From: "in_progress", To: "integration", Skills: []string{"ac", "scope"}},
			{From: "integration", To: "done", Skills: []string{"release"}},
		},
	}
}

// atIntegration is a story whose plan was rejected once on backlog→plan and
// whose code was rejected twice on in_progress→integration; it is now at
// integration.
func atIntegration() []ledger.Entry {
	return []ledger.Entry{
		evS(ledger.KindReviewReject, "backlog", "plan", "r1", "arch"),
		evS(ledger.KindReviewAccept, "backlog", "plan", "r2", "arch"),
		evS(ledger.KindReviewAccept, "backlog", "plan", "r2", "plan-review"),
		evS(ledger.KindReviewAccept, "backlog", "plan", "r2", "intent"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "p1", "definition"),
		ev(ledger.KindStatusTransition, "plan", "in_progress"),
		evS(ledger.KindReviewReject, "in_progress", "integration", "c1", "ac"),
		evS(ledger.KindReviewAccept, "in_progress", "integration", "c2", "ac"),
		evS(ledger.KindReviewReject, "in_progress", "integration", "c2", "scope"),
		evS(ledger.KindReviewAccept, "in_progress", "integration", "c3", "ac"),
		evS(ledger.KindReviewAccept, "in_progress", "integration", "c3", "scope"),
		ev(ledger.KindStatusTransition, "in_progress", "integration"),
	}
}

// sty_0b1abacc AC1 + AC3: each step's chip shows its exit gates — the start
// state's chip carries the readiness panel, plan carries plan→in_progress — and
// the rejections across every round, with no passed, failed or rounds counts;
// the tooltip reads "<step>: <N> exit gates, <R> rejections".
func TestChipShowsExitGatesAndRejections(t *testing.T) {
	stages := buildStages(atIntegration(), "integration", false, noStep, exitSpec(), nil, nil)
	if got, want := names(stages), []string{"backlog", "plan", "in_progress", "integration"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chips = %v, want %v (the start state has a chip)", got, want)
	}
	_, progress := renderRowCells(t, "integration", stages)
	for _, want := range []string{
		`backlog <b class="gates">3</b> · <b class="rej">1 rejected</b></span>`,
		`plan <b class="gates">1</b></span>`,
		`in_progress <b class="gates">2</b> · <b class="rej">2 rejected</b></span>`,
	} {
		if !strings.Contains(progress, want) {
			t.Errorf("progress cell missing %q:\n%s", want, progress)
		}
	}
	for _, bad := range []string{"passed", "failed", "rounds", `class="ok"`, "entered from"} {
		if strings.Contains(progress, bad) {
			t.Errorf("progress cell carries %q:\n%s", bad, progress)
		}
	}
	for name, want := range map[string]string{
		"backlog":     "backlog: 3 exit gates, 1 rejection",
		"plan":        "plan: 1 exit gate",
		"in_progress": "in_progress: 2 exit gates, 2 rejections",
		"integration": "integration: 1 exit gate, not yet run",
	} {
		if got := stageNamed(stages, name).Title; got != want {
			t.Errorf("%s title = %q, want %q", name, got, want)
		}
	}
}

// sty_0b1abacc AC2: the step being worked shows its exit gates as not yet run,
// and while they run how many are running — on the backlog chip for a story in
// readiness, and on a performing step's own exit edge.
func TestChipShowsStepBeingWorkedNotRunOrRunning(t *testing.T) {
	_, progress := renderRowCells(t, "integration", buildStages(atIntegration(), "integration", false, noStep, exitSpec(), nil, nil))
	if want := `integration <b class="gates">1</b> <b class="pend">not run</b></span>`; !strings.Contains(progress, want) {
		t.Errorf("not-run cell missing %q:\n%s", want, progress)
	}

	running := buildStages(atIntegration(), "integration", true, noStep, exitSpec(), nil, &seatGateVM{Total: 1})
	_, progress = renderRowCells(t, "integration", running)
	if want := `integration <b class="gates">1</b> <b class="run live">1 running</b></span>`; !strings.Contains(progress, want) {
		t.Errorf("running cell missing %q:\n%s", want, progress)
	}
	if got, want := stageNamed(running, "integration").Title, "integration: 1 exit gate, 1 running"; got != want {
		t.Errorf("running title = %q, want %q", got, want)
	}

	// Readiness: still in backlog, no transition, the readiness panel running.
	ready := buildStages(nil, "backlog", true, noStep, exitSpec(), nil, &seatGateVM{Total: 3})
	_, progress = renderRowCells(t, "backlog", ready)
	if want := `backlog <b class="gates">3</b> <b class="run live">3 running</b></span>`; len(ready) != 1 || !strings.Contains(progress, want) {
		t.Errorf("readiness cell = %q (stages %+v), want %q", progress, ready, want)
	}
	if got, want := ready[0].Title, "backlog: 3 exit gates, 3 running"; got != want {
		t.Errorf("readiness title = %q, want %q", got, want)
	}

	// A performing step: the coder dispatched for in_progress while the status is
	// still plan. Its own exit edge (in_progress → integration) is not yet run.
	es := atIntegration()[:7]
	stages := applyPerforming(buildStages(es, "plan", true, noStep, exitSpec(), nil, nil), "plan",
		&dispatchVM{Agent: "coder", Step: "in_progress"}, stageRoute(exitSpec(), nil))
	_, progress = renderRowCells(t, "plan", stages)
	if want := `in_progress <b class="gates">2</b> <b class="pend">not run</b></span>`; !strings.Contains(progress, want) {
		t.Errorf("performing cell missing %q:\n%s", want, progress)
	}
	if got, want := stageNamed(stages, "in_progress").Title, "in_progress: 2 exit gates, not yet run; performing: coder"; got != want {
		t.Errorf("performing title = %q, want %q", got, want)
	}
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
// the previous rejected round's verdicts; that rejected round is still counted.
func TestInFlightNewRoundNothingReported(t *testing.T) {
	es := append(enteredPlan(),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "o1", "rev-a"),
		evS(ledger.KindReviewReject, "plan", "in_progress", "o1", "rev-b"))
	cur := stageNamed(buildStages(es, "plan", true, noStep, gatedSpec(), nil, &seatGateVM{Total: 4}), "plan")
	if counts(cur.Gates) != [4]int{4, 0, 0, 4} || !cur.Gates.Live || cur.Gates.Rejected != 1 {
		t.Fatalf("plan gates = %+v, want 4 gates, 4 running, 1 rejected", cur.Gates)
	}
}

// AC1: a seat in flight on the step performer's dispatch is not gates running —
// the chip shows its exit gates not yet run and no running count.
func TestPerformerDispatchIsNotGatesRunning(t *testing.T) {
	seat := seatPayload{ID: "sty_1", InFlight: true, Activity: "perform:plan", ActivityTotal: 1}
	if g := gateActivity(seat); g != nil {
		t.Fatalf("a performer's stamp read as gates running: %+v", g)
	}
	stages := buildStages(enteredPlan(), "plan", true, noStep, gatedSpec(), nil, gateActivity(seat))
	cur := stageNamed(stages, "plan")
	if cur.Gates == nil || cur.Gates.Live || !cur.Gates.NotRun {
		t.Fatalf("plan gates = %+v, want its exit gates not yet run", cur.Gates)
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

// AC1: the current chip of a story with exit gates running holds ONE gate
// count — its exit edge's — with the running count carrying the live class.
func TestCurrentChipShowsOnlyOutgoingGates(t *testing.T) {
	stages := buildStages(enteredPlan(), "plan", true, noStep, gatedSpec(), nil, &seatGateVM{Total: 4, Passed: 1, Failed: 1})
	_, progress := renderRowCells(t, "plan", stages)
	want := `plan <b class="gates">4</b> <b class="run live">2 running</b></span>`
	if !strings.Contains(progress, want) {
		t.Errorf("progress cell = %q, want %q", progress, want)
	}
	if strings.Contains(progress, "pending-gate") {
		t.Errorf("progress cell still carries a pending-gate: %q", progress)
	}
}

// AC1: the live class pulses — it reuses the chip's pulse animation; the passed
// count's class is gone and the not-run mark has its own rule.
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
	if strings.Contains(css, "\n.ok {") {
		t.Error("app.css still styles the removed .ok passed count")
	}
	if !strings.Contains(css, ".stage-chip .pend {") {
		t.Error("app.css has no .stage-chip .pend rule for not-run exit gates")
	}
}

// AC2: a completed step shows its exit gate count and no running or not-run
// mark, and an earlier rejected round on the edge is counted.
func TestCompletedStageShowsItsExitGates(t *testing.T) {
	es := append(enteredPlan(),
		evS(ledger.KindReviewReject, "plan", "in_progress", "i0", "rev-a"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i1", "rev-a"),
		ev(ledger.KindStatusTransition, "plan", "in_progress"))
	stages := buildStages(es, "in_progress", false, noStep, gatedSpec(), nil, nil)
	plan := stageNamed(stages, "plan")
	if plan.State != "done" || plan.Gates == nil || plan.Gates.Gates != 3 || plan.Gates.Rejected != 1 || plan.Gates.Live || plan.Gates.NotRun {
		t.Fatalf("plan chip = %+v gates %+v, want done with 3 exit gates, 1 rejected", plan, plan.Gates)
	}
	_, progress := renderRowCells(t, "in_progress", stages)
	if !strings.Contains(progress, `plan <b class="gates">3</b> · <b class="rej">1 rejected</b></span>`) {
		t.Errorf("completed plan chip not rendered with its counts: %q", progress)
	}
}

// AC3: a scoped gate applies_to excludes for the story's tags, and a `when` gate
// whose skip row falls in the latest round, are not counted; a control with the
// tag and a triggered `when` gate counts both. The gates are plan's exit gates.
func TestGateCountExcludesUntriggeredAndTagSkipped(t *testing.T) {
	gates := func(es []ledger.Entry, tags []string) int {
		g := stageNamed(buildStages(es, "in_progress", false, noStep, gatedSpec(), tags, nil), "plan").Gates
		if g == nil {
			return 0
		}
		return g.Gates
	}
	skipped := append(enteredPlan(),
		evS(ledger.KindReviewReject, "plan", "in_progress", "i1", "rev-a"), // an earlier round
		whenSkip("rev-d", "in_progress"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-a"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-b"),
		ev(ledger.KindStatusTransition, "plan", "in_progress"))
	if n := gates(skipped, []string{"surface:cli"}); n != 2 {
		t.Fatalf("plan exit gates = %d, want 2 (rev-c tag-skipped, rev-d untriggered)", n)
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
	if n := gates(triggered, []string{"surface:ui"}); n != 4 {
		t.Fatalf("control exit gates = %d, want 4 (rev-c by tag, rev-d triggered)", n)
	}
	// Without the tag but with rev-d triggered: rev-c alone leaves the count.
	if n := gates(triggered, nil); n != 3 {
		t.Fatalf("untagged exit gates = %d, want 3 (rev-c tag-skipped)", n)
	}
}

// AC3: the tooltip spells the exit gates and their rejections in words — for a
// step whose exit gates are running and for completed ones.
func TestChipTooltipSpellsExitGatesAndRejections(t *testing.T) {
	inFlight := append(enteredPlan(),
		evS(ledger.KindReviewReject, "plan", "in_progress", "o1", "rev-a"),
		evS(ledger.KindReviewReject, "plan", "in_progress", "o2", "rev-b"))
	stages := buildStages(inFlight, "plan", true, noStep, gatedSpec(), nil, &seatGateVM{Total: 4, Passed: 1, Failed: 1})
	if got, want := stageNamed(stages, "plan").Title, "plan: 4 exit gates, 2 rejections, 2 running"; got != want {
		t.Errorf("in-flight title = %q, want %q", got, want)
	}
	if got, want := stageNamed(stages, "backlog").Title, "backlog: 3 exit gates, 1 rejection"; got != want {
		t.Errorf("backlog title = %q, want %q", got, want)
	}

	completed := append(enteredPlan(),
		evS(ledger.KindReviewReject, "plan", "in_progress", "i1", "rev-a"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-a"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-b"),
		evS(ledger.KindReviewAccept, "plan", "in_progress", "i2", "rev-d"),
		ev(ledger.KindStatusTransition, "plan", "in_progress"),
		ev(ledger.KindStatusTransition, "in_progress", "done"))
	stages = buildStages(completed, "done", false, noStep, gatedSpec(), nil, nil)
	for name, want := range map[string]string{
		"plan":        "plan: 3 exit gates, 1 rejection",
		"in_progress": "in_progress: 4 exit gates",
		"done":        "done",
	} {
		if got := stageNamed(stages, name).Title; got != want {
			t.Errorf("%s title = %q, want %q", name, got, want)
		}
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
