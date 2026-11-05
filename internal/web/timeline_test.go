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

	"github.com/bobmcallan/satelle/internal/mirror"
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
		b, _ := json.Marshal(Item{ID: id, Kind: "story", Title: id, Status: "backlog", CreatedAt: now, UpdatedAt: now})
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
