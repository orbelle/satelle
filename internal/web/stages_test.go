package web

import (
	"bytes"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// routeSpec is a resolved route: a start, three work stages, a terminal done, the
// resume park (reviewer role, from="*") and a cancel sink (reviewer role, no
// from). The classification the stage builder reads is all in the Spec — the test
// names no state the production code does not receive.
func routeSpec() wfdot.Spec {
	return wfdot.Spec{States: []wfdot.State{
		{Name: "backlog", Shape: "Mdiamond"},
		{Name: "plan", Agent: "planner"},
		{Name: "in_progress", Agent: "coder"},
		{Name: "integration", Agent: "executor"},
		{Name: "done", Shape: "Msquare"},
		{Name: "blocked", Agent: "reviewer", From: []string{"*"}},
		{Name: "cancelled", Agent: "reviewer"},
	}}
}

// ddbeLedger is the sty_ddbe2669 shape: backlog→plan took 6 rounds (5 rejected,
// 1 accepted), with a park into blocked and its recovery in the middle, and the
// first rejected round had TWO rejecting reviewers; then plan→in_progress took 1
// accepted round with both reviewers accepting.
func ddbeLedger() []ledger.Entry {
	const bp = "backlog"
	return []ledger.Entry{
		evA(ledger.KindReviewReject, bp, "plan", "a1"),
		evA(ledger.KindReviewReject, bp, "plan", "a1"), // second reviewer, same round
		evA(ledger.KindReviewAccept, bp, "plan", "a2"), // dissent in a rejected round
		evA(ledger.KindReviewReject, bp, "plan", "a2"),
		evA(ledger.KindReviewReject, bp, "plan", "a3"),
		ev(ledger.KindStatusTransition, bp, "blocked"), // park
		ev(ledger.KindStatusTransition, "blocked", bp), // recover
		evA(ledger.KindReviewReject, bp, "plan", "a4"),
		evA(ledger.KindReviewReject, bp, "plan", "a5"),
		evA(ledger.KindReviewAccept, bp, "plan", "a6"),
		evA(ledger.KindReviewAccept, bp, "plan", "a6"),
		ev(ledger.KindStatusTransition, bp, "plan"),
		evA(ledger.KindReviewAccept, "plan", "in_progress", "b1"),
		evA(ledger.KindReviewAccept, "plan", "in_progress", "b1"),
		ev(ledger.KindStatusTransition, "plan", "in_progress"),
	}
}

func checkDdbe(t *testing.T, spec wfdot.Spec) {
	t.Helper()
	stages := buildStages(ddbeLedger(), "in_progress", false, noStep, spec, nil, nil)
	if got, want := names(stages), []string{"plan", "in_progress"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want exactly %v (no blocked, no backlog)", got, want)
	}
	plan := stages[0]
	if plan.accepted != 1 || plan.rejected != 5 || plan.Parked != 1 || plan.State != "done" {
		t.Errorf("plan = %+v, want 1 accepted, 5 rejected, parked 1, done", plan)
	}
	cur := stages[1]
	if cur.State != "current" || cur.accepted != 1 || cur.rejected != 0 {
		t.Errorf("in_progress = %+v, want current with 1 accepted", cur)
	}
	if cur.out != nil {
		t.Errorf("in_progress pending = %+v, want none (no outgoing gate presented)", cur.out)
	}
}

// AC1/AC2/AC3: the ddbe2669 fixture, with the route resolved…
func TestStagesDdbeFixtureResolvedRoute(t *testing.T) { checkDdbe(t, routeSpec()) }

// …and with no Spec at all (the degraded fallback reads the A→X→A shape).
func TestStagesDdbeFixtureNoRoute(t *testing.T) { checkDdbe(t, wfdot.Spec{}) }

// AC2: however many reviewers reject in one attempt, it is ONE rejected round.
func TestStagesTwoRejectingReviewersAreOneRound(t *testing.T) {
	entries := []ledger.Entry{
		evA(ledger.KindReviewReject, "backlog", "plan", "x1"),
		evA(ledger.KindReviewReject, "backlog", "plan", "x1"),
		evA(ledger.KindReviewReject, "backlog", "plan", "x1"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
	}
	stages := buildStages(entries, "plan", false, noStep, routeSpec(), nil, nil)
	if s := stageNamed(stages, "plan"); s.rejected != 1 || s.accepted != 0 {
		t.Fatalf("plan = %+v, want 1 rejected round from three rejecting rows", s)
	}
	if a, r := ledger.EdgeRounds(entries, "backlog", "plan"); a != 0 || r != 1 {
		t.Errorf("EdgeRounds = %d/%d, want 0/1", a, r)
	}
}

// AC4: rejected rounds before an accepted one on the same edge stay red; the
// accept does not turn them green.
func TestStagesRejectedRoundsStayRedAfterAnAccept(t *testing.T) {
	var entries []ledger.Entry
	for _, id := range []string{"r1", "r2", "r3", "r4", "r5"} {
		entries = append(entries, evA(ledger.KindReviewReject, "backlog", "plan", id))
	}
	entries = append(entries,
		evA(ledger.KindReviewAccept, "backlog", "plan", "r6"),
		ev(ledger.KindStatusTransition, "backlog", "plan"))
	stages := buildStages(entries, "done", false, noStep, wfdot.Spec{}, nil, nil)
	if s := stageNamed(stages, "plan"); s.accepted != 1 || s.rejected != 5 {
		t.Fatalf("plan = %+v, want 1 accepted / 5 rejected", s)
	}
}

// AC3: a transition into the cancel sink is not a park note, and a cancelled
// story is finished — its chip never pulses and it has no pending outgoing gate.
func TestStagesCancelIsNotAParkAndNeverPulses(t *testing.T) {
	entries := []ledger.Entry{
		evA(ledger.KindReviewAccept, "backlog", "plan", "c1"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
		evA(ledger.KindReviewReject, "plan", "in_progress", "c2"), // a presented outgoing edge
		ev(ledger.KindStatusTransition, "plan", "cancelled"),
	}
	stages := buildStages(entries, "cancelled", false, noStep, routeSpec(), nil, nil)
	for _, s := range stages {
		if s.Parked != 0 {
			t.Errorf("cancel produced a park note: %+v", s)
		}
		if s.State == "current" {
			t.Errorf("a cancelled story must show no pulse: %+v", stages)
		}
		// Finished: never the rejected outgoing plan→in_progress round.
		if s.out != nil {
			t.Errorf("a finished story must carry no pending gate: %+v", s)
		}
	}
	if got, want := names(stages), []string{"plan", "cancelled"}; !reflect.DeepEqual(got, want) {
		t.Errorf("stages = %v, want %v (the sink only as the final chip)", got, want)
	}
}

// AC3: with no Spec the finished decision reads the workitem lifecycle names.
func TestStagesDoneWithoutRouteNeverPulses(t *testing.T) {
	entries := []ledger.Entry{
		evA(ledger.KindReviewAccept, "backlog", "in_progress", "d1"),
		ev(ledger.KindStatusTransition, "backlog", "in_progress"),
		evA(ledger.KindReviewAccept, "in_progress", workitem.StatusDone, "d2"),
		ev(ledger.KindStatusTransition, "in_progress", workitem.StatusDone),
	}
	stages := buildStages(entries, workitem.StatusDone, false, noStep, wfdot.Spec{}, nil, nil)
	for _, s := range stages {
		if s.State == "current" {
			t.Fatalf("a done story with no route must show no pulse: %+v", stages)
		}
	}
	// The done entry gate's ✓1 reads from the done chip itself.
	if s := stageNamed(stages, workitem.StatusDone); s.accepted != 1 {
		t.Errorf("done chip = %+v, want the entry gate's ✓1", s)
	}
}

// AC3: a park the story is still in names the park as the current chip, with no
// counts, and the stage whose gate was in flight carries the note.
func TestStagesStillParked(t *testing.T) {
	entries := []ledger.Entry{
		evA(ledger.KindReviewAccept, "backlog", "plan", "p1"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
		evA(ledger.KindReviewReject, "plan", "in_progress", "p2"),
		ev(ledger.KindStatusTransition, "plan", "blocked"),
	}
	stages := buildStages(entries, "blocked", false, noStep, routeSpec(), nil, nil)
	if got, want := names(stages), []string{"plan", "blocked"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	if stages[0].Parked != 1 {
		t.Errorf("plan = %+v, want the park noted on the stage the story was in", stages[0])
	}
	last := stages[len(stages)-1]
	if last.Name != "blocked" || last.State != "current" || last.accepted+last.rejected+last.Parked != 0 {
		t.Errorf("last = %+v, want a bare current blocked chip", last)
	}
}

// AC2: the outgoing gate a story is waiting on rides the current PROGRESS chip —
// mid-gate, entry gate only, no rows, and a stale outgoing round.
func TestPendingGateOnCurrentStage(t *testing.T) {
	enter := []ledger.Entry{
		evA(ledger.KindReviewReject, "backlog", "plan", "g0"),
		evA(ledger.KindReviewAccept, "backlog", "plan", "g1"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
	}
	// Entry gate only: nothing presented since entering plan. Its rounds are in
	// the plan chip's totals, not pending.
	cur := stageNamed(buildStages(enter, "plan", false, noStep, routeSpec(), nil, nil), "plan")
	if cur.out != nil || cur.accepted != 1 || cur.rejected != 1 {
		t.Errorf("entry-only plan chip = %+v, want ✓1 ✗1 totals and no pending gate", cur)
	}
	// Mid-gate: an outgoing edge has been presented since entering plan.
	mid := append(append([]ledger.Entry(nil), enter...),
		evA(ledger.KindReviewReject, "plan", "in_progress", "g2"),
		evA(ledger.KindReviewReject, "plan", "in_progress", "g2"),
		evA(ledger.KindReviewReject, "plan", "in_progress", "g3"))
	cur = stageNamed(buildStages(mid, "plan", false, noStep, routeSpec(), nil, nil), "plan")
	if p := cur.out; cur.State != "current" || p == nil || p.accepted != 0 || p.rejected != 2 || p.Edge != "plan → in_progress" {
		t.Errorf("mid-gate plan chip = %+v, want current with ✗2 pending on plan → in_progress", cur)
	}
	if cur.accepted != 1 || cur.rejected != 1 {
		t.Errorf("mid-gate plan chip totals = ✓%d ✗%d, want the entry gate's ✓1 ✗1 untouched", cur.accepted, cur.rejected)
	}
	// Mid-gate on a finished story is not pending: the story reached done.
	fin := append(append([]ledger.Entry(nil), mid...), ev(ledger.KindStatusTransition, "plan", "done"))
	for _, s := range buildStages(fin, "done", false, noStep, routeSpec(), nil, nil) {
		if s.out != nil {
			t.Errorf("finished story chip %+v carries a pending gate", s)
		}
	}
	// No review rows → nothing pending.
	bare := []ledger.Entry{ev(ledger.KindStatusTransition, "backlog", "plan")}
	if s := stageNamed(buildStages(bare, "plan", false, noStep, routeSpec(), nil, nil), "plan"); s.out != nil {
		t.Errorf("no review rows: pending = %+v, want none", s.out)
	}
	// An outgoing round from BEFORE the story entered its status is not mid-gate.
	stale := []ledger.Entry{
		evA(ledger.KindReviewReject, "plan", "in_progress", "s1"),
		evA(ledger.KindReviewAccept, "backlog", "plan", "s2"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
	}
	if s := stageNamed(buildStages(stale, "plan", false, noStep, routeSpec(), nil, nil), "plan"); s.out != nil {
		t.Errorf("stale outgoing round: pending = %+v, want none", s.out)
	}
}

// AC6: a category that resolved no route spine (empty Spec, every depth 0) orders
// its stages by first entry in the ledger, each once.
func TestStagesNoSpineOrdersByLedger(t *testing.T) {
	stages := buildStages(ddbeLedger(), "in_progress", false, noStep, wfdot.Spec{}, nil, nil)
	if got, want := names(stages), []string{"plan", "in_progress"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
}

// renderRowCells renders one story row and returns its STATUS and PROGRESS
// cells (the rendered row's third and fourth <td>).
func renderRowCells(t *testing.T, status string, stages []stageVM) (statusCell, progressCell string) {
	t.Helper()
	it := workitem.Item{ID: "sty_ddbe2669", Kind: workitem.KindStory, Title: "T", Status: status}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "workitemRows", []rowVM{{Item: it, Stages: stages}}); err != nil {
		t.Fatal(err)
	}
	cells := strings.Split(buf.String(), "<td")
	if len(cells) < 5 {
		t.Fatalf("rendered row has %d cells, want at least 4:\n%s", len(cells)-1, buf.String())
	}
	return cells[3], cells[4]
}

// AC1: the rendered row carries stage chips and no numbered review-light circle.
func TestStagesRenderNoNumberedLights(t *testing.T) {
	stages := buildStages(ddbeLedger(), "in_progress", false, noStep, routeSpec(), nil, nil)
	it := workitem.Item{ID: "sty_ddbe2669", Kind: workitem.KindStory, Title: "T", Status: "in_progress"}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "workitemRows", []rowVM{{Item: it, Stages: stages}}); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if strings.Contains(html, "review-light") {
		t.Errorf("a numbered review-light circle was rendered:\n%s", html)
	}
	for _, want := range []string{
		`class="stage-chip stage-done"`, `class="stage-chip stage-current"`, `<b class="blk">1</b>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered row missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "parked") {
		t.Errorf("the chip must carry no 'parked' word:\n%s", html)
	}
	if !strings.Contains(html, "rounds: 1 accepted, 5 rejected; blocked once") {
		t.Errorf("tooltip should spell the round history in words:\n%s", html)
	}
}

// AC1: the blocked count is painted in the theme's warn colour.
func TestBlockedCountUsesWarnColour(t *testing.T) {
	raw, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\.stage-chip \.blk\s*\{[^}]*\}`).FindString(string(raw))
	if m == "" || !strings.Contains(m, "color: var(--warn)") {
		t.Errorf("app.css needs a .stage-chip .blk rule using var(--warn); got %q", m)
	}
}

// AC2: the title names the episodes by the park transition's own state, so a
// route whose park state is not "blocked" reads its own word — on a resolved
// route and on the degraded no-Spec shape.
func TestStageTitleUsesParkStateName(t *testing.T) {
	spec := wfdot.Spec{States: []wfdot.State{
		{Name: "backlog", Shape: "Mdiamond"},
		{Name: "plan", Agent: "planner"},
		{Name: "in_progress", Agent: "coder"},
		{Name: "on_hold", Agent: "reviewer", From: []string{"*"}},
	}}
	entries := []ledger.Entry{
		evA(ledger.KindReviewAccept, "backlog", "plan", "h1"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
		ev(ledger.KindStatusTransition, "plan", "on_hold"),
		ev(ledger.KindStatusTransition, "on_hold", "plan"),
		ev(ledger.KindStatusTransition, "plan", "on_hold"),
		ev(ledger.KindStatusTransition, "on_hold", "plan"),
	}
	for name, sp := range map[string]wfdot.Spec{"route": spec, "no route": {}} {
		stages := buildStages(entries, "plan", false, noStep, sp, nil, nil)
		plan := stageNamed(stages, "plan")
		if plan.Parked != 2 || plan.ParkName != "on_hold" {
			t.Fatalf("%s: plan = %+v, want 2 parks into on_hold", name, plan)
		}
		if got := stageTitle(plan); !strings.Contains(got, "on_hold 2 times") || strings.Contains(got, "blocked") {
			t.Errorf("%s: title = %q, want the park state's own name", name, got)
		}
	}
	once := stageVM{Name: "plan", State: "done", Parked: 1, ParkName: "on_hold"}
	if got := stageTitle(once); !strings.Contains(got, "on_hold once") {
		t.Errorf("title = %q, want 'on_hold once'", got)
	}
}

// AC3: a stage never parked shows no blocked count in its chip and no park
// wording in its title.
func TestNeverParkedStageShowsNoBlockedCount(t *testing.T) {
	entries := []ledger.Entry{
		evA(ledger.KindReviewReject, "backlog", "plan", "n1"),
		evA(ledger.KindReviewAccept, "backlog", "plan", "n2"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
	}
	stages := buildStages(entries, "plan", false, noStep, routeSpec(), nil, nil)
	it := workitem.Item{ID: "sty_nopark01", Kind: workitem.KindStory, Title: "T", Status: "plan"}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "workitemRows", []rowVM{{Item: it, Stages: stages}}); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if strings.Contains(html, `class="blk"`) {
		t.Errorf("a never-parked stage rendered a blocked count:\n%s", html)
	}
	for _, s := range stages {
		for _, word := range []string{"blocked", "parked"} {
			if strings.Contains(s.Title, word) {
				t.Errorf("title %q carries park wording %q", s.Title, word)
			}
		}
	}
}

// AC1: the STATUS cell holds the status badge alone — no gate badge, no round
// count — for a done story and for a story mid-gate.
func TestStatusCellHoldsOnlyTheStatusBadge(t *testing.T) {
	midLedger := []ledger.Entry{
		evA(ledger.KindReviewAccept, "backlog", "plan", "m1"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
		evA(ledger.KindReviewReject, "plan", "in_progress", "m2"),
	}
	doneLedger := []ledger.Entry{
		evA(ledger.KindReviewAccept, "backlog", "plan", "d1"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
		evA(ledger.KindReviewAccept, "plan", "done", "d2"),
		ev(ledger.KindStatusTransition, "plan", "done"),
	}
	for name, tc := range map[string]struct {
		status  string
		entries []ledger.Entry
	}{"done": {"done", doneLedger}, "mid-gate": {"plan", midLedger}} {
		status, _ := renderRowCells(t, tc.status, buildStages(tc.entries, tc.status, false, noStep, routeSpec(), nil, nil))
		want := `><span class="badge s-` + tc.status + `">` + tc.status + `</span></td>`
		if strings.TrimSpace(status) != strings.TrimSpace(want) {
			t.Errorf("%s: status cell = %q, want only %q", name, status, want)
		}
		for _, bad := range []string{"gate-badge", "✓", "✗"} {
			if strings.Contains(status, bad) {
				t.Errorf("%s: status cell carries %q: %q", name, bad, status)
			}
		}
	}
}

// sty_216d5205: a story whose outgoing round was presented and is not running
// keeps that round's history in its current chip's tooltip, never as a second
// mark in the chip; a done story's cell names no outgoing edge.
func TestProgressCellPresentedOutgoingRoundIsInTheTooltip(t *testing.T) {
	mid := []ledger.Entry{
		evA(ledger.KindReviewAccept, "backlog", "plan", "m1"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
		evA(ledger.KindReviewReject, "plan", "in_progress", "m2"),
	}
	_, progress := renderRowCells(t, "plan", buildStages(mid, "plan", false, noStep, routeSpec(), nil, nil))
	if !strings.Contains(progress, `plan → in_progress: rounds: 1 rejected`) {
		t.Errorf("mid-gate progress cell missing the outgoing round's history: %q", progress)
	}
	for _, bad := range []string{"pending-gate", `class="rej"`, `class="run`} {
		if strings.Contains(progress, bad) {
			t.Errorf("mid-gate progress cell carries %q: %q", bad, progress)
		}
	}
	_, progress = renderRowCells(t, "done", buildStages(append(mid[:2:2],
		evA(ledger.KindReviewAccept, "plan", "done", "d2"),
		ev(ledger.KindStatusTransition, "plan", "done")), "done", false, noStep, routeSpec(), nil, nil))
	if strings.Contains(progress, "in_progress") {
		t.Errorf("done story's progress cell names an outgoing edge: %q", progress)
	}
}

// sty_434d2fd9 AC1: the rows of ledger.ProgressKinds — all a snapshot sends
// of a story's old history — are enough for the fold. Each fixture is
// interleaved with edge-shaped rows of other kinds; dropping them must not
// change a stage, so the test fails if buildStages starts reading another kind.
func TestProgressKindsCoverBuildStages(t *testing.T) {
	fixtures := map[string]struct {
		status  string
		entries []ledger.Entry
	}{
		"ddbe": {"in_progress", ddbeLedger()},
		"parked": {"blocked", []ledger.Entry{
			evA(ledger.KindReviewAccept, "backlog", "plan", "p1"),
			ev(ledger.KindStatusTransition, "backlog", "plan"),
			evA(ledger.KindReviewReject, "plan", "in_progress", "p2"),
			ev(ledger.KindStatusTransition, "plan", "blocked"),
		}},
		"sink": {"cancelled", []ledger.Entry{
			evA(ledger.KindReviewAccept, "backlog", "plan", "c1"),
			ev(ledger.KindStatusTransition, "backlog", "plan"),
			evA(ledger.KindPerformerReject, "plan", "in_progress", "c2"),
			ev(ledger.KindStatusTransition, "plan", "cancelled"),
		}},
		"mid-gate": {"plan", []ledger.Entry{
			evA(ledger.KindReadinessAccept, "backlog", "plan", "g1"),
			ev(ledger.KindStatusTransition, "backlog", "plan"),
			evA(ledger.KindReviewReject, "plan", "in_progress", "g2"),
		}},
	}
	other := []string{ledger.KindComment, ledger.KindStatusReconcile, ledger.KindAgentInvocation,
		ledger.KindReadinessReuse, ledger.KindGateSkipped, ledger.KindStepSummary, ledger.KindStoryDocAttached}
	for name, f := range fixtures {
		var noisy, kept []ledger.Entry
		for i, e := range f.entries {
			lp := parseEdge(e)
			noisy = append(noisy, e, evA(other[i%len(other)], lp.From, lp.To, "n"))
		}
		for _, e := range noisy {
			if slices.Contains(ledger.ProgressKinds, e.Kind) {
				kept = append(kept, e)
			}
		}
		for _, spec := range []wfdot.Spec{routeSpec(), {}} {
			all := buildStages(noisy, f.status, false, noStep, spec, nil, nil)
			if len(all) == 0 {
				t.Fatalf("%s: no stages from the fixture", name)
			}
			if got := buildStages(kept, f.status, false, noStep, spec, nil, nil); !reflect.DeepEqual(got, all) {
				t.Errorf("%s (route %v): progress-kind rows give %+v, every row gives %+v", name, len(spec.States) > 0, got, all)
			}
		}
	}
}

func TestFinishedRule(t *testing.T) {
	spec := routeSpec()
	for status, want := range map[string]bool{"done": true, "cancelled": true, "plan": false, "blocked": false, "backlog": false} {
		if got := finished(spec, status); got != want {
			t.Errorf("finished(route, %q) = %v, want %v", status, got, want)
		}
	}
	for status, want := range map[string]bool{"done": true, "cancelled": true, "plan": false, "blocked": false} {
		if got := finished(wfdot.Spec{}, status); got != want {
			t.Errorf("finished(no route, %q) = %v, want %v", status, got, want)
		}
	}
}
