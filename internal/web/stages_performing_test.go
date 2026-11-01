package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// performingRow assembles one story row the way the mirror does — ledger, seat
// and route through attachStagesFrom — and renders it.
func performingRow(t *testing.T, entries []ledger.Entry, status string, seat seatPayload) (rowVM, string) {
	t.Helper()
	it := workitem.Item{ID: "sty_1", Kind: workitem.KindStory, Title: "T", Status: status}
	dispatches := map[string]*dispatchVM{}
	if vm := buildDispatchIndicator(seat, time.Now().UTC()); vm != nil {
		dispatches[it.ID] = vm
	}
	rows := attachStagesFrom(map[string][]ledger.Entry{it.ID: entries}, []workitem.Item{it},
		map[string]bool{it.ID: seat.InFlight && !seat.Stale}, dispatches,
		map[string]*seatGateVM{it.ID: gateActivity(seat)},
		func(string, string) int { return 0 }, func(string) wfdot.Spec { return routeSpec() })
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "workitemRows", rows); err != nil {
		t.Fatal(err)
	}
	return rows[0], buf.String()
}

// acceptedIntoInProgress is a story whose plan→in_progress entry gates have
// accepted, with no transition yet: the performer for in_progress is what
// takes the status there.
func acceptedIntoInProgress() []ledger.Entry {
	return []ledger.Entry{
		evA(ledger.KindReviewAccept, "backlog", "plan", "a1"),
		ev(ledger.KindStatusTransition, "backlog", "plan"),
		evA(ledger.KindReviewAccept, "plan", "in_progress", "b1"),
		evA(ledger.KindReviewAccept, "plan", "in_progress", "b1"),
	}
}

func coderSeat(activity string) seatPayload {
	now := time.Now().UTC()
	return seatPayload{
		ID: "sty_1", StorySeat: true, InFlight: true, Agent: "coder", Model: "sonnet",
		LastEvent: "working", LastEventAt: now.Add(-10 * time.Second),
		ActivityStartedAt: now.Add(-time.Minute), Activity: activity,
	}
}

// TestRowShowsDispatchedStepAsPerforming (sty_5650fbcc AC1): with the edge into
// in_progress accepted and the coder dispatched for it, the row shows
// in_progress as the in-flight step and plan as completed, while the STATUS
// cell keeps the recorded plan.
func TestRowShowsDispatchedStepAsPerforming(t *testing.T) {
	row, html := performingRow(t, acceptedIntoInProgress(), "plan", coderSeat("dispatch:in_progress"))
	if got := stageNamed(row.Stages, "plan"); got.State != "done" || got.out != nil {
		t.Errorf("plan = %+v, want done with no outgoing gate", got)
	}
	got := stageNamed(row.Stages, "in_progress")
	if got.State != "performing" || got.Performer != "coder" {
		t.Fatalf("in_progress = %+v, want performing by coder; stages %+v", got, row.Stages)
	}
	if !strings.Contains(got.Title, "performing: coder") {
		t.Errorf("in_progress title = %q", got.Title)
	}
	for _, s := range row.Stages {
		if s.State == "current" {
			t.Errorf("stage %q is still current while in_progress is performed", s.Name)
		}
	}
	for _, want := range []string{
		`class="stage-chip stage-done" title="plan`,
		`class="stage-chip stage-performing"`,
		`<span class="badge s-plan">plan</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("row missing %q:\n%s", want, html)
		}
	}
}

// TestRowGatePhaseIsNotPerforming: a seat in flight for something other than a
// performer dispatch (a gate review) leaves the ledger's current stage alone.
func TestRowGatePhaseIsNotPerforming(t *testing.T) {
	row, html := performingRow(t, acceptedIntoInProgress(), "plan", coderSeat("satelle-code-ac-review"))
	if got := stageNamed(row.Stages, "plan"); got.State != "current" {
		t.Errorf("plan = %+v, want current", got)
	}
	if strings.Contains(html, "stage-performing") {
		t.Errorf("a gate phase rendered a performing chip:\n%s", html)
	}
}

// TestRowAfterPerformerFinishes (sty_5650fbcc AC2): once the performer hands
// back and the status moves to in_progress, the row shows in_progress as the
// story's current step and nothing in flight — whether the seat has settled or
// still carries the dispatch label of the step the status now names.
func TestRowAfterPerformerFinishes(t *testing.T) {
	done := append(acceptedIntoInProgress(), ev(ledger.KindStatusTransition, "plan", "in_progress"))
	settled := coderSeat("dispatch:in_progress")
	settled.InFlight = false
	for name, seat := range map[string]seatPayload{
		"settled seat":           settled,
		"seat on the new status": coderSeat("dispatch:in_progress"),
	} {
		t.Run(name, func(t *testing.T) {
			row, html := performingRow(t, done, "in_progress", seat)
			if got := stageNamed(row.Stages, "in_progress"); got.State != "current" {
				t.Errorf("in_progress = %+v, want current", got)
			}
			if got := stageNamed(row.Stages, "plan"); got.State != "done" {
				t.Errorf("plan = %+v, want done", got)
			}
			if strings.Contains(html, "stage-performing") {
				t.Errorf("a finished performer still renders in flight:\n%s", html)
			}
			if !strings.Contains(html, `<span class="badge s-in_progress">in_progress</span>`) {
				t.Errorf("STATUS cell does not show the new status:\n%s", html)
			}
		})
	}
}

// TestStagePerformingCSSRule: the performing chip has its own pulsing rule.
func TestStagePerformingCSSRule(t *testing.T) {
	raw, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	i := strings.Index(css, ".stage-performing {")
	if i < 0 {
		t.Fatal("app.css has no .stage-performing rule")
	}
	if rule, _, _ := strings.Cut(css[i:], "}"); !strings.Contains(rule, "animation: review-pulse") {
		t.Errorf(".stage-performing rule = %q, want the review-pulse animation", rule)
	}
}
