package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// Every page and fragment a partition serves renders from pushed rows: the
// templates execute against the view models without error.
func TestPartitionPagesRender(t *testing.T) {
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	const rk = "rk-smoke"
	if _, err := s.TouchPartition(ctx, rk, "proj", now); err != nil {
		t.Fatal(err)
	}
	row := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	story := Item{ID: "sto_1", Kind: "story", Title: "Smoke Story", Status: "s1", Tags: []string{"a:b"}, CreatedAt: now, UpdatedAt: now}
	task := Item{ID: "tas_1", Kind: "task", Title: "Smoke Task", Status: "s1", CreatedAt: now, UpdatedAt: now}
	ev := Entry{ID: "e1", StoryID: "sto_1", Kind: "gate.run", Payload: json.RawMessage(`{"result":"accept","duration_ms":1200,"tokens_in":10,"tokens_out":5}`), CreatedAt: now}
	for kind, rows := range map[string][]mirror.ItemRow{
		"story":        {{ID: story.ID, Payload: row(story)}},
		"task":         {{ID: task.ID, Payload: row(task)}},
		"ledger_event": {{ID: ev.ID, Payload: row(ev)}},
		"identity":     {{ID: "meta", Payload: row(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p"})}},
	} {
		if err := s.ReplaceKind(ctx, rk, kind, rows, now); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(NewMirror(s).Handler)
	t.Cleanup(srv.Close)
	for path, want := range map[string]string{
		"/":                            "proj",
		"/r/proj/":                     "Smoke Story",
		"/r/proj/fragment/stories":     "Smoke Story",
		"/r/proj/fragment/tasks":       "Smoke Task",
		"/r/proj/fragment/activity":    "",
		"/r/proj/fragment/story/sto_1": "",
		"/r/proj/story/sto_1":          "accept",
		"/r/proj/task/tas_1":           "Smoke Task",
		"/r/proj/help":                 "",
		"/r/proj/settings":             "",
		"/settings/global":             "",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %d\n%s", path, resp.StatusCode, body)
			continue
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("GET %s: missing %q", path, want)
		}
	}
}
