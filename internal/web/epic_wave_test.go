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
	"github.com/bobmcallan/satelle/internal/workitem"
)

// epicFixture is a mirror partition holding an epic-parent, three children and
// whatever wave / seat rows a test seeds. Children: a (startable), b (held on a),
// c (in flight).
type epicFixture struct {
	srv *httptest.Server
	s   *mirror.Store
	rk  string
	now time.Time
}

func newEpicFixture(t *testing.T, wave map[string]any, seats ...map[string]any) *epicFixture {
	t.Helper()
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	rk := "rk-epic"
	if _, err := s.TouchPartition(ctx, rk, "epicp", now); err != nil {
		t.Fatal(err)
	}
	mk := func(id, category string, status string, tags ...string) mirror.ItemRow {
		b, _ := json.Marshal(workitem.Item{
			ID: id, Kind: workitem.KindStory, Title: "T " + id, Status: status,
			Category: category, Tags: tags, UpdatedAt: now, CreatedAt: now,
		})
		return mirror.ItemRow{ID: id, Payload: string(b)}
	}
	stories := []mirror.ItemRow{
		mk("sty_epic", "epic-parent", "ready", "epic:x"),
		mk("sty_a", "fix", "backlog", "epic:x"),
		mk("sty_b", "fix", "backlog", "epic:x", "depends-on:sty_a"),
		mk("sty_c", "fix", "in_progress", "epic:x"),
		mk("sty_plain", "chore", "backlog"),
	}
	if err := s.ReplaceKind(ctx, rk, "story", stories, now); err != nil {
		t.Fatal(err)
	}
	if wave != nil {
		b, _ := json.Marshal(wave)
		if err := s.ReplaceKind(ctx, rk, "wave", []mirror.ItemRow{{ID: "sty_epic", Payload: string(b)}}, now); err != nil {
			t.Fatal(err)
		}
	}
	var seatRows []mirror.ItemRow
	for _, seat := range seats {
		b, _ := json.Marshal(seat)
		seatRows = append(seatRows, mirror.ItemRow{ID: seat["id"].(string), Payload: string(b)})
	}
	if err := s.ReplaceKind(ctx, rk, "seat", seatRows, now); err != nil {
		t.Fatal(err)
	}
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: "epicp", RepoRoot: "/e", FooterEmail: "a@b.c"})
	_ = s.ReplaceKind(ctx, rk, "identity", []mirror.ItemRow{{ID: "meta", Payload: string(ident)}}, now)
	srv := httptest.NewServer(NewMirror(s).Handler)
	t.Cleanup(srv.Close)
	return &epicFixture{srv: srv, s: s, rk: rk, now: now}
}

func (f *epicFixture) get(t *testing.T, path string) string {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, resp.StatusCode, raw)
	}
	return string(raw)
}

// memberRow is the members-table row of id in the epic's detail page.
func memberRow(t *testing.T, body, id string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, `<td class="id"><a href="story/`+id+`">`)
	if !ok {
		t.Fatalf("no member row for %s in:\n%s", id, body)
	}
	row, _, _ := strings.Cut(after, "</tr>")
	return row
}

func inFlightSeat(id string, now time.Time) map[string]any {
	return map[string]any{
		"id": id, "in_flight": true, "stale": false,
		"agent": "coder", "model": "sonnet", "last_event": "tool: Bash",
		"last_event_at": now.Add(-time.Minute), "activity_started_at": now.Add(-5 * time.Minute),
	}
}

func waveRow() map[string]any {
	return map[string]any{
		"container": "sty_epic", "theme": "epic:x", "schedule": "parallel",
		"runnable": []string{"sty_a"},
		"omitted": []map[string]any{{
			"id": "sty_b", "cause": "waiting_on_dependency",
			"waiting_on": []map[string]any{{"id": "sty_a", "status": "backlog"}},
			"reason":     "sty_b waits on sty_a (backlog)",
		}},
	}
}
