package verb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func workingOnRows(t *testing.T, db *store.DB, id string) []verb.WorkingOnPayload {
	t.Helper()
	entries, err := db.Ledger.ListByStory(context.Background(), id, ledger.KindWorkingOn)
	if err != nil {
		t.Fatal(err)
	}
	var out []verb.WorkingOnPayload
	for _, e := range entries {
		var p verb.WorkingOnPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("working_on payload %s: %v", e.Payload, err)
		}
		out = append(out, p)
	}
	return out
}

// Every move into a performing state appends one working_on row naming the
// location, session and state (sty_40853d02 AC4, sty_9c782f0e AC2) — not only a
// move that took a seat: a step under a seat already held writes one too. A move
// into a state that performs nothing (a park, the terminal state) writes none,
// and an engage the seat refuses writes none (the seat still works, AC4).
func TestEveryMoveIntoAPerformingStateAppendsWorkingOn(t *testing.T) {
	db := wireWithWorkflowsStore(t, singleStoryWF)
	verb.SetLocationResolver(func() (string, string) { return "loc_unit", "unit box" })
	t.Setenv(config.SessionEnv, "sess_unit")

	var a, b workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "First", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "Second", "category": "feature"}), &b)
	if rows := workingOnRows(t, db, a.ID); len(rows) != 0 {
		t.Fatalf("a story nobody engaged has working_on rows: %+v", rows)
	}
	states := func(id string) []string {
		var out []string
		for _, r := range workingOnRows(t, db, id) {
			out = append(out, r.State)
		}
		return out
	}
	want := func(id string, w ...string) {
		t.Helper()
		if got := states(id); strings.Join(got, ",") != strings.Join(w, ",") {
			t.Fatalf("working_on states = %v, want %v", got, w)
		}
	}

	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)
	want(a.ID, "plan")
	if r := workingOnRows(t, db, a.ID)[0]; r.Location != "loc_unit" || r.Label != "unit box" || r.Session != "sess_unit" || r.State != "plan" {
		t.Errorf("row = %+v, want loc_unit / unit box / sess_unit / plan", r)
	}

	// The next step under the seat already held is a move into a performing state.
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "in_progress"}), &a)
	want(a.ID, "plan", "in_progress")

	// The seat still refuses a second engage, and the refused move leaves no row.
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": b.ID, "status": "plan"}); err == nil || !strings.Contains(err.Error(), "engagement seat") {
		t.Fatalf("second engage should be refused on the seat, got %v", err)
	}
	want(b.ID)

	// A park performs nothing; resuming from it is a move back in.
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "blocked"}), &a)
	want(a.ID, "plan", "in_progress")
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "in_progress"}), &a)
	want(a.ID, "plan", "in_progress", "in_progress")

	// The terminal state performs nothing.
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "done"}), &a)
	if a.Status != "done" {
		t.Fatalf("a did not close: %s", a.Status)
	}
	want(a.ID, "plan", "in_progress", "in_progress")
}

// A working_on row is presence, never a refusal (sty_9c782f0e AC2): a story
// another location's row says it is working on still engages here, and the
// engage writes this location's row after it.
func TestWorkingOnElsewhereNeverRefusesAnEngage(t *testing.T) {
	db := wireWithWorkflowsStore(t, singleStoryWF)
	verb.SetLocationResolver(func() (string, string) { return "loc_here", "" })
	t.Setenv(config.SessionEnv, "sess_here")

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "Worked elsewhere", "category": "feature"}), &it)
	other, _ := json.Marshal(verb.WorkingOnPayload{Location: "loc_elsewhere", Session: "sess_there", State: "plan"})
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: it.ID, Kind: ledger.KindWorkingOn, Actor: "executor", Body: "elsewhere", Payload: other,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "plan"}); err != nil {
		t.Fatalf("another location's working_on must not refuse an engage: %v", err)
	}
	rows := workingOnRows(t, db, it.ID)
	if len(rows) != 2 || rows[0].Location != "loc_elsewhere" || rows[1].Location != "loc_here" {
		t.Fatalf("rows = %+v, want the other location's then this one's", rows)
	}
}

// With no location wired (an unbound repo) the row still records the engagement,
// naming only the session and state.
func TestWorkingOnRowWithoutALocation(t *testing.T) {
	db := wireWithWorkflowsStore(t, singleStoryWF)
	verb.ClearLocationResolver()
	t.Setenv(config.SessionEnv, "sess_solo")

	var a workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "Solo", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)
	rows := workingOnRows(t, db, a.ID)
	if len(rows) != 1 || rows[0].Location != "" || rows[0].Session != "sess_solo" {
		t.Fatalf("rows = %+v, want one row with no location and session sess_solo", rows)
	}
}
