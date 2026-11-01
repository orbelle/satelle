package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestBuildDispatchIndicatorRunning (sty_752c4ef2 AC6, rendering 1: running):
// an in-flight, non-stale seat with agent/model dispatch detail produces a
// running indicator carrying that detail — not shown when the seat is stale,
// not in flight, or has no dispatch metadata (a gate-only phase stamp).
func TestBuildDispatchIndicatorRunning(t *testing.T) {
	now := time.Now().UTC()
	seat := seatPayload{
		ID: "sty_1", InFlight: true, Stale: false,
		Agent: "coder", Model: "sonnet", LastEvent: "tool: Bash",
		LastEventAt: now.Add(-10 * time.Second), ActivityStartedAt: now.Add(-18 * time.Minute),
		IdleTimeoutNs: int64(5 * time.Minute),
	}
	vm := buildDispatchIndicator(seat, now)
	if vm == nil {
		t.Fatal("expected a running indicator for an in-flight seat with dispatch detail")
	}
	if vm.Agent != "coder" || vm.Model != "sonnet" || vm.LastEvent != "tool: Bash" {
		t.Errorf("indicator = %+v", vm)
	}
	if vm.Warn {
		t.Error("10s idle against a 5m idle_timeout must not warn")
	}

	for _, bad := range []seatPayload{
		{ID: "x", InFlight: false, Agent: "coder"},             // not in flight
		{ID: "x", InFlight: true, Stale: true, Agent: "coder"}, // stale
		{ID: "x", InFlight: true, Stale: false},                // no dispatch detail
	} {
		if buildDispatchIndicator(bad, now) != nil {
			t.Errorf("expected no indicator for %+v", bad)
		}
	}
}

// TestBuildDispatchIndicatorWarnsPastHalfIdleTimeout (sty_752c4ef2 AC6,
// rendering 2: warning): the indicator turns to warn once idle time (since
// the last real event) crosses HALF of the seat's resolved idle_timeout —
// not before.
func TestBuildDispatchIndicatorWarnsPastHalfIdleTimeout(t *testing.T) {
	now := time.Now().UTC()
	base := seatPayload{
		ID: "sty_1", InFlight: true, Stale: false, Agent: "coder",
		ActivityStartedAt: now.Add(-time.Minute), IdleTimeoutNs: int64(5 * time.Minute),
	}
	justUnder := base
	justUnder.LastEventAt = now.Add(-149 * time.Second) // < 2m30s (half of 5m)
	if vm := buildDispatchIndicator(justUnder, now); vm == nil || vm.Warn {
		t.Errorf("idle just under half idle_timeout must not warn: %+v", vm)
	}
	over := base
	over.LastEventAt = now.Add(-3 * time.Minute) // > 2m30s
	if vm := buildDispatchIndicator(over, now); vm == nil || !vm.Warn {
		t.Errorf("idle past half idle_timeout must warn: %+v", vm)
	}
}

// TestBuildStagesRefusedAttemptIsABadgeNotAStage (sty_752c4ef2 AC6, rendering 3)
// reproduces the exact sty_7069bced evidence: the story is SITTING AT plan
// (entered via a status_transition, never having left it — the next edge has not
// committed), and an EARLIER coder attempt on plan→in_progress was refused. That
// must render the pulsing current plan stage AND the refused round as a red
// rejection on plan's own exit gates — a refused next-edge attempt is never a stage of
// its own, nor a single ambiguous red mark standing in for "currently broken"
// while a new coder dispatch is in flight toward the same edge.
func TestBuildStagesRefusedAttemptIsABadgeNotAStage(t *testing.T) {
	chrono := []ledger.Entry{
		ev(ledger.KindStatusTransition, "backlog", "plan"), // entry into the live current state
		ev(ledger.KindReviewReject, "plan", "in_progress"), // the refused earlier attempt
	}
	stages := buildStages(chrono, "plan", false, noStep, wfdot.Spec{}, nil, nil)
	if got, want := names(stages), []string{"backlog", "plan"}; !reflect.DeepEqual(got, want) || stages[1].State != "current" {
		t.Fatalf("stages = %+v, want the done start state and the current plan stage", stages)
	}
	if g := stages[1].Gates; g == nil || g.Rejected != 1 || stages[1].Title != "plan: 1 exit gate, 1 rejection" {
		t.Errorf("plan exit gates = %+v (title %q), want the refused round as 1 rejection", g, stages[1].Title)
	}
}

// TestMirrorRowShowsRunningDispatchBesideEarlierRefusal (sty_752c4ef2 AC6):
// the full sty_7069bced reproduction, end-to-end through mirrorLoadPanels —
// a story sitting at plan with an earlier refused attempt on plan→in_progress
// AND a live coder dispatch now working toward that same edge. The row must
// show BOTH: the red round count for the past refusal (distinct from the
// pulsing current stage) AND the running dispatch indicator — a live agent at work is
// never rendered as only a red failure.
func TestMirrorRowShowsRunningDispatchBesideEarlierRefusal(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	rk := "rk-recovering"
	if _, err := s.TouchPartition(ctx, rk, "rec", now); err != nil {
		t.Fatal(err)
	}
	story := workitem.Item{
		ID: "sty_7069bced", Kind: workitem.KindStory, Title: "Recovering Story",
		Status: "plan", Category: "chore", UpdatedAt: now, CreatedAt: now,
	}
	sb, _ := json.Marshal(story)
	entryLed := map[string]any{
		"id": "evt_entry", "story_id": "sty_7069bced", "kind": "status_transition",
		"created_at": now.Add(-10 * time.Minute), "payload": map[string]string{"from": "backlog", "to": "plan"},
	}
	elb, _ := json.Marshal(entryLed)
	rejectLed := map[string]any{
		"id": "evt_reject", "story_id": "sty_7069bced", "kind": "review_reject",
		"created_at": now.Add(-5 * time.Minute), "payload": map[string]string{"from": "plan", "to": "in_progress", "skill": "gate"},
	}
	rlb, _ := json.Marshal(rejectLed)
	seat, _ := json.Marshal(map[string]any{
		"id": "sty_7069bced", "in_flight": true, "stale": false,
		"agent": "coder", "model": "sonnet", "last_event": "tool: Bash",
		"last_event_at": now.Add(-10 * time.Second), "activity_started_at": now.Add(-2 * time.Minute),
		"idle_timeout_ns": int64(5 * time.Minute),
	})
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: "rec", RepoRoot: "/r", FooterEmail: "a@b.c"})
	_ = s.ReplaceKind(ctx, rk, "story", []mirror.ItemRow{{ID: "sty_7069bced", Payload: string(sb)}}, now)
	_ = s.ReplaceKind(ctx, rk, "ledger_event", []mirror.ItemRow{
		{ID: "evt_entry", Payload: string(elb)}, {ID: "evt_reject", Payload: string(rlb)},
	}, now)
	_ = s.ReplaceKind(ctx, rk, "seat", []mirror.ItemRow{{ID: "sty_7069bced", Payload: string(seat)}}, now)
	_ = s.ReplaceKind(ctx, rk, "identity", []mirror.ItemRow{{ID: "meta", Payload: string(ident)}}, now)

	data, _, err := mirrorLoadPanels(ctx, s, rk, "rec")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Stories) != 1 {
		t.Fatalf("expected 1 story, got %d", len(data.Stories))
	}
	row := data.Stories[0]
	if row.Dispatch == nil || row.Dispatch.Agent != "coder" {
		t.Fatalf("expected a running dispatch indicator on the recovering row, got %+v", row.Dispatch)
	}
	cur := stageNamed(row.Stages, "plan")
	if len(row.Stages) != 2 || cur.State != "current" {
		t.Fatalf("Stages = %+v, want the start state and the current plan stage (the live plan step)", row.Stages)
	}
	if g := cur.Gates; g == nil || g.Rejected != 1 {
		t.Fatalf("plan exit gates = %+v, want 1 rejection (the refused attempt)", g)
	}

	ms := NewMirror(s)
	srv := httptest.NewServer(ms.Handler)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/r/rec/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	for _, want := range []string{
		`class="stage-chip stage-current"`,
		`class="dispatch-indicator"`,
		"coder (sonnet) running",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("recovering row missing %q; body:\n%s", want, body)
		}
	}
	// The refused round reads from the PROGRESS cell, and the STATUS cell has no
	// gate badge of its own.
	if strings.Contains(body, "gate-badge") {
		t.Errorf("a gate-badge was rendered in the story row; body:\n%s", body)
	}
	_, afterCol, ok := strings.Cut(body, `class="col-reviews">`)
	if !ok {
		t.Fatalf("no PROGRESS cell in body:\n%s", body)
	}
	progress, _, _ := strings.Cut(afterCol, "</td>")
	// The refusal is a rejection of plan's work by its exit gates (sty_0b1abacc).
	if !strings.Contains(progress, `plan <b class="gates">1</b> · <b class="rej">1 rejected</b>`) {
		t.Errorf("PROGRESS cell = %q, want the refused round as plan's rejection", progress)
	}
}

// TestMirrorRendersDispatchIndicatorAndWarn (sty_752c4ef2 AC6): end-to-end
// through mirrorLoadPanels + the workitemRows template — a running dispatch
// renders the indicator text and CSS class; once idle passes half
// idle_timeout it also carries the warn class.
func TestMirrorRendersDispatchIndicatorAndWarn(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	rk := "rk-dispatch"
	if _, err := s.TouchPartition(ctx, rk, "disp", now); err != nil {
		t.Fatal(err)
	}
	story := workitem.Item{
		ID: "sty_running", Kind: workitem.KindStory, Title: "Running Story",
		Status: workitem.StatusInProgress, Category: "chore", UpdatedAt: now, CreatedAt: now,
	}
	sb, _ := json.Marshal(story)
	seat, _ := json.Marshal(map[string]any{
		"id": "sty_running", "in_flight": true, "stale": false,
		"agent": "coder", "model": "sonnet", "last_event": "tool: Bash",
		"last_event_at": now.Add(-3 * time.Minute), "activity_started_at": now.Add(-20 * time.Minute),
		"idle_timeout_ns": int64(5 * time.Minute), // warn: 3m idle > half of 5m
	})
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: "disp", RepoRoot: "/d", FooterEmail: "a@b.c"})
	_ = s.ReplaceKind(ctx, rk, "story", []mirror.ItemRow{{ID: "sty_running", Payload: string(sb)}}, now)
	_ = s.ReplaceKind(ctx, rk, "seat", []mirror.ItemRow{{ID: "sty_running", Payload: string(seat)}}, now)
	_ = s.ReplaceKind(ctx, rk, "identity", []mirror.ItemRow{{ID: "meta", Payload: string(ident)}}, now)

	data, _, err := mirrorLoadPanels(ctx, s, rk, "disp")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Stories) != 1 || data.Stories[0].Dispatch == nil {
		t.Fatalf("expected a populated Dispatch indicator, got %+v", data.Stories)
	}
	if !data.Stories[0].Dispatch.Warn {
		t.Error("3m idle against a 5m idle_timeout must warn")
	}

	ms := NewMirror(s)
	srv := httptest.NewServer(ms.Handler)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/r/disp/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	for _, want := range []string{
		`class="dispatch-indicator dispatch-warn"`,
		"coder (sonnet) running",
		"tool: Bash",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("project page missing %q; body:\n%s", want, body)
		}
	}
}
