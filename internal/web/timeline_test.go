package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestSettingsTimelineFieldsControl asserts the project settings page hosts the
// viewer-local Timeline-fields checklist (sty_43d228e4) — the approved placement.
func TestSettingsTimelineFieldsControl(t *testing.T) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "settings", settingsData{RepoRoot: "/repo", TopBar: newTopBar("")}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, f := range []string{"walltime", "tokens", "model", "outcome"} {
		if !strings.Contains(out, `data-tlfield="`+f+`"`) {
			t.Errorf("settings page missing the Timeline fields checkbox for %q:\n%s", f, out)
		}
	}
}

// evs wraps ledger entries as the timeline view-models the detail template now
// takes, computing each row's data-driven telemetry chips (sty_43d228e4).
func evs(entries ...ledger.Entry) []eventVM {
	out := make([]eventVM, len(entries))
	for i, e := range entries {
		out[i] = eventVM{Entry: e, Chips: eventChips(e)}
	}
	return out
}

// TestTimelineChips asserts the timeline renders data-driven agent-action chips
// (sty_43d228e4 / sty_56aae77a): an agent_invocation row shows wall-time/tokens/model
// chips (no outcome) when usage is measured; an unreported invocation never shows a
// token chip; a telemetry_event shows its outcome; a plain status_transition shows none.
// The model chip (sty_87b86044) prefers the resolved id, marks an alias-only legacy
// row "(unknown)", and never falls back to the dispatching agent's name — but an
// agent_invocation row with neither field still names a model ("unknown"), matching
// `satelle story cost`'s unconditional Model column rather than showing no chip at all.
func TestTimelineChips(t *testing.T) {
	now := time.Now()
	// Legacy non-zero total still measures (no usage_available field); alias
	// only, no model_resolved (pre-sty_87b86044 row) — renders "sonnet (unknown)".
	inv, _ := json.Marshal(map[string]any{"agent": "reviewer", "model": "sonnet", "tokens_total": 2000, "duration_ms": 2400})
	// Explicit unreported: must not show a tokens chip (sty_56aae77a).
	unrep, _ := json.Marshal(map[string]any{"agent": "reviewer", "model": "grok-4.5", "tokens_total": 0, "usage_available": false, "duration_ms": 800})
	tel, _ := json.Marshal(map[string]any{"kind": "agent-retry", "data": map[string]any{"outcome": "error"}})
	// A row whose model resolved: shows the resolved id verbatim, not "(unknown)".
	resolved, _ := json.Marshal(map[string]any{"agent": "reviewer", "model": "opus", "model_resolved": "claude-opus-5-5", "duration_ms": 100})
	// An agent-only row with no model info at all: the removed agent-name
	// fallback means the chip reads "unknown", never the agent's name
	// (sty_87b86044).
	agentOnly, _ := json.Marshal(map[string]any{"agent": "planner", "duration_ms": 50})
	// A legacy review_accept row (predates this field entirely): a verdict row
	// always names a model, even "unknown" — unlike an invocation row, it is
	// never suppressed.
	legacyVerdict, _ := json.Marshal(map[string]any{"accept": true, "skill": "satelle-story-plan-review"})
	d := detailData{
		Item: workitem.Item{ID: "sty_x", Kind: workitem.KindStory, Title: "x", Status: "in_progress", CreatedAt: now, UpdatedAt: now},
		Events: evs(
			ledger.Entry{Kind: ledger.KindAgentInvocation, Body: "invoked", Payload: inv, CreatedAt: now},
			ledger.Entry{Kind: ledger.KindAgentInvocation, Body: "unreported", Payload: unrep, CreatedAt: now},
			ledger.Entry{Kind: ledger.KindTelemetryEvent, Body: "telemetry", Payload: tel, CreatedAt: now},
			ledger.Entry{Kind: ledger.KindStatusTransition, Body: "backlog → plan", CreatedAt: now},
			ledger.Entry{Kind: ledger.KindAgentInvocation, Body: "resolved", Payload: resolved, CreatedAt: now},
			ledger.Entry{Kind: ledger.KindAgentInvocation, Body: "agent-only", Payload: agentOnly, CreatedAt: now},
			ledger.Entry{Kind: ledger.KindReviewAccept, Body: "accepted", Payload: legacyVerdict, CreatedAt: now},
		),
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "itemDetail", d); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`<span class="chip chip-walltime">2.4s</span>`,
		`<span class="chip chip-tokens">2,000 tok</span>`,
		`<span class="chip chip-model">sonnet (unknown)</span>`,
		`<span class="chip chip-outcome">error</span>`,
		`<span class="chip chip-model">claude-opus-5-5</span>`,
		// A legacy verdict row (no model_resolved, no model, no alias at all)
		// and a legacy agent_invocation row (agent-only) both still name a
		// model — "unknown" — never suppressed (sty_87b86044).
		`<span class="chip chip-outcome">accept</span>`,
		`<span class="chip chip-model">unknown</span>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline missing chip %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, `chip-model">planner</span>`) {
		t.Errorf("agent-only row must not fall back to the agent name as a model chip; got:\n%s", out)
	}
	// Two rows carry neither an alias nor a resolved id (agent-only, legacy
	// verdict): both must render the "unknown" model chip, not just one of them.
	if got := strings.Count(out, `chip-model">unknown</span>`); got != 2 {
		t.Errorf("want 2 unknown model chips (agent-only + legacy verdict); got %d:\n%s", got, out)
	}
	// Unreported row: walltime + model only — never a tokens chip (measured zero
	// would still need UsageAvailable; unreported must not invent one).
	if strings.Contains(out, `chip-tokens">0`) || strings.Count(out, `chip-tokens`) != 1 {
		t.Errorf("want exactly one tokens chip (the measured 2,000); got:\n%s", out)
	}
	// Measured inv: walltime+tokens+model=3; unreported: walltime+model=2; tel: outcome=1;
	// resolved: walltime+model=2; agent-only: walltime+model=2 (unknown, no longer
	// suppressed); legacy verdict: outcome+model=2. Total 12.
	if strings.Count(out, `class="chip `) != 12 {
		t.Errorf("want 12 chips; got %d:\n%s", strings.Count(out, `class="chip `), out)
	}
}

// TestTimelineDotOutcomeClass asserts the timeline dots are coloured by event
// outcome (sty_f19d2ec4): a review_reject <li> carries the fail class, a
// review_accept <li> the pass class, and neutral process events carry no outcome
// class (keeping the default accent dot). The class is rendered server-side, so the
// same itemDetail template covers BOTH the inline expansion and the standalone
// detail page.
func TestTimelineDotOutcomeClass(t *testing.T) {
	now := time.Now()
	d := detailData{
		Item: workitem.Item{ID: "sty_x", Kind: workitem.KindStory, Title: "x", Status: "in_progress", CreatedAt: now, UpdatedAt: now},
		Events: evs(
			ledger.Entry{Kind: ledger.KindStoryCreated, Body: "created", CreatedAt: now},
			ledger.Entry{Kind: ledger.KindStatusTransition, Body: "backlog → in_progress", CreatedAt: now},
			ledger.Entry{Kind: ledger.KindReviewReject, Body: "rejected x→y", CreatedAt: now},
			ledger.Entry{Kind: ledger.KindReviewAccept, Body: "accepted x→y", CreatedAt: now},
			ledger.Entry{Kind: ledger.KindStepSummary, Body: "summary", CreatedAt: now},
		),
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "itemDetail", d); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, `<li class="tl-fail">`) {
		t.Errorf("a review_reject event should carry the fail dot class (tl-fail); got:\n%s", out)
	}
	if !strings.Contains(out, `<li class="tl-pass">`) {
		t.Errorf("a review_accept event should carry the pass dot class (tl-pass); got:\n%s", out)
	}
	// Neutral process events stay un-classed (default accent dot) — they render as a
	// bare <li>, never tl-pass/tl-fail.
	if strings.Count(out, `<li class="tl-`) != 2 {
		t.Errorf("only the two outcome-bearing events should be coloured; got %d classed dots", strings.Count(out, `<li class="tl-`))
	}
	if !strings.Contains(out, "<li>") {
		t.Errorf("neutral events should render as a bare <li> (default accent dot); got:\n%s", out)
	}
}

// TestTimelineDotPaletteReused pins the timeline dot fills (tl-pass green, tl-fail
// red, fixed hexes that read on both themes) and that the progress column's
// .rej text uses a theme token rather than those hexes.
func TestTimelineDotPaletteReused(t *testing.T) {
	raw, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for _, want := range []string{
		"ol.timeline li.tl-pass::before { background: #2ecc71; }",
		"ol.timeline li.tl-fail::before { background: #e74c3c; }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing timeline dot fill rule: %q", want)
		}
	}
	// The progress column's rejected text uses a theme token (legible in both
	// themes), never a hard-coded hex.
	for _, want := range []string{".rej { color: var(--fail);"} {
		if !strings.Contains(css, want) {
			t.Errorf("progress-column text token rule missing (%q)", want)
		}
	}
}

// TestTimelineChipsExcludeToolPermissionRows pins sty_8eae81ac AC4: a legacy
// agent_invocation row carrying the decided_by/decision/tool tool-permission
// shape must never render the unconditional invocation model chip (it used to
// show "unknown" for every such row before it got its own ledger kind).
func TestTimelineChipsExcludeToolPermissionRows(t *testing.T) {
	perm, _ := json.Marshal(map[string]any{"tool": "Edit", "kind": "permission", "decision": "allow", "decided_by": "policy"})
	chips := eventChips(ledger.Entry{Kind: ledger.KindAgentInvocation, Payload: perm})
	for _, c := range chips {
		if c.Type == "model" {
			t.Errorf("a tool-permission row rendered a model chip: %+v", chips)
		}
	}
}

// sty_0dc600e1 AC2: the story page of a story the mirror holds no event of asks
// FetchStory for its history in the checkout's repo root and renders what the
// fetch brought; a story whose history is held is not fetched.
func TestStoryPageFetchesHistoryItHoldsNoEventOf(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	rk, slug := "rk-fetch", "fetch"
	if _, err := s.TouchPartition(ctx, rk, slug, now); err != nil {
		t.Fatal(err)
	}
	row := func(id string) mirror.ItemRow {
		b, _ := json.Marshal(workitem.Item{ID: id, Kind: workitem.KindStory, Title: id, Status: "backlog", CreatedAt: now, UpdatedAt: now})
		return mirror.ItemRow{ID: id, Payload: string(b)}
	}
	event := func(id, story, body string) mirror.ItemRow {
		b, _ := json.Marshal(map[string]any{"id": id, "story_id": story, "kind": "comment", "actor": "alice", "body": body, "created_at": now})
		return mirror.ItemRow{ID: id, Payload: string(b)}
	}
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: slug, RepoRoot: "/repos/fetch"})
	held := []mirror.ItemRow{event("evt_held", "sty_held", "held here")}
	for kind, rows := range map[string][]mirror.ItemRow{
		"story":        {row("sty_remote"), row("sty_held")},
		"ledger_event": held,
		"identity":     {{ID: "meta", Payload: string(ident)}},
	} {
		if err := s.ReplaceKind(ctx, rk, kind, rows, now); err != nil {
			t.Fatal(err)
		}
	}

	srv := NewMirrorWithInstance(s, "test")
	var calls []string
	srv.FetchStory = func(ctx context.Context, root, id string) error {
		calls = append(calls, root+" "+id)
		return s.ReplaceKind(ctx, rk, "ledger_event", append(slices.Clone(held), event("evt_fetched", id, "fetched from the server")), now)
	}
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)

	if body := httpGetBody(t, ts.URL+"/r/"+slug+"/story/sty_remote"); !strings.Contains(body, "fetched from the server") {
		t.Errorf("the story page did not render the fetched history:\n%s", body)
	}
	if body := httpGetBody(t, ts.URL+"/r/"+slug+"/story/sty_held"); !strings.Contains(body, "held here") {
		t.Errorf("the held story's page lost its history:\n%s", body)
	}
	if want := []string{"/repos/fetch sty_remote"}; !slices.Equal(calls, want) {
		t.Errorf("FetchStory calls = %v, want %v", calls, want)
	}
}
