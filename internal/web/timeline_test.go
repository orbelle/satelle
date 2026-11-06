package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// TestSettingsTimelineFieldsControl asserts the project settings page hosts the
// viewer-local Timeline-fields checklist (sty_43d228e4) — the approved placement.
func TestSettingsTimelineFieldsControl(t *testing.T) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "settings", settingsData{RepoRoot: "/repo", TopBar: topBar{}}); err != nil {
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
func evs(entries ...Entry) []eventVM {
	out := make([]eventVM, len(entries))
	for i, e := range entries {
		out[i] = eventVM{Entry: e, Chips: eventChips(e)}
	}
	return out
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

// A ledger row of an older pusher (story_id, created_at, body) still lands on
// its item's timeline.
func TestTimelineReadsLegacyEventRows(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	rk, slug := "rk-legacy", "legacy"
	if _, err := s.TouchPartition(ctx, rk, slug, now); err != nil {
		t.Fatal(err)
	}
	it, _ := json.Marshal(Item{ID: "sty_old", Kind: "story", Title: "old", Status: "s", CreatedAt: now, UpdatedAt: now})
	ev, _ := json.Marshal(map[string]any{"id": "evt_1", "story_id": "sty_old", "kind": "comment", "actor": "alice", "body": "legacy body", "created_at": now})
	for kind, rows := range map[string][]mirror.ItemRow{
		"story":        {{ID: "sty_old", Payload: string(it)}},
		"ledger_event": {{ID: "evt_1", Payload: string(ev)}},
	} {
		if err := s.ReplaceKind(ctx, rk, kind, rows, now); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(NewMirrorWithInstance(s, "test").Handler)
	t.Cleanup(ts.Close)
	body := httpGetBody(t, ts.URL+"/r/"+slug+"/story/sty_old")
	for _, want := range []string{"legacy body", "alice", "comment"} {
		if !strings.Contains(body, want) {
			t.Errorf("legacy event row: missing %q", want)
		}
	}
}
