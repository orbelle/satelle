package web

import (
	"bytes"
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

// The fixture workflow: names are the test's own; the web knows none of them.
var fixtureWorkflow = mirror.Workflow{
	Name: "flow",
	States: []mirror.WorkflowState{
		{Name: "zed", Terminal: true},
		{Name: "alpha", Start: true},
		{Name: "held", Park: true, Resumes: true},
		{Name: "beta", PerformerAgent: "builder", PerformerSkill: "build"},
	},
	Gates: []mirror.WorkflowGate{{Name: "g1", Skill: "g1"}, {Name: "g2", Skill: "g2"}, {Name: "g3", Skill: "g3"}},
	Transitions: []mirror.WorkflowTransition{
		{From: []string{"alpha"}, To: "beta", Groups: []mirror.WorkflowGroup{{Mode: "sequential", Gates: []string{"g1", "g2"}}}},
		{From: []string{"beta"}, To: "zed", Groups: []mirror.WorkflowGroup{{Mode: "parallel", Gates: []string{"g3"}}}},
		{From: []string{"*"}, To: "held"},
		{From: []string{"held"}, To: "@prior"},
	},
}

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// fixtureSnapshot is a full snapshot as uipush sends it.
func fixtureSnapshot() mirror.Snapshot {
	t0 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	ev := func(id, kind string, m int, p any) json.RawMessage {
		return raw(mirror.Event{ID: id, Item: "sto_1", Kind: kind, At: at(m), Actor: "ann", Payload: raw(p)})
	}
	gate := func(g, res string, in, out int, ms int64, notes string) mirror.GateRun {
		return mirror.GateRun{Transition: "alpha->beta", Gate: g, Binding: "judge", Result: res, Notes: notes, TokensIn: in, TokensOut: out, DurationMS: ms}
	}
	return mirror.Snapshot{
		RepoKey: "rk", Slug: "proj",
		Stories: []json.RawMessage{
			raw(mirror.Item{ID: "sto_1", Kind: "story", Title: "Detail Story", Body: "the body", Status: "beta",
				Criteria: []string{"first criterion", "second criterion"}, Tags: []string{"k:v"}, ParentID: "sto_0",
				Category: "cat", Workflow: "flow", Assignee: "ann", CreatedAt: t0, UpdatedAt: at(5)}),
			raw(mirror.Item{ID: "sto_0", Kind: "story", Title: "Parent", Status: "zed", Workflow: "flow", CreatedAt: t0, UpdatedAt: t0}),
		},
		Executions: []json.RawMessage{
			raw(mirror.Item{ID: "exe_1", Kind: "execution", Title: "Run", Status: "alpha", ParentID: "sto_1", Workflow: "flow", CreatedAt: t0, UpdatedAt: t0}),
		},
		LedgerEvents: []json.RawMessage{
			ev("e1", "item.created", 0, map[string]string{"title": "Detail Story"}),
			ev("e2", mirror.EventGateRun, 1, gate("g1", "reject", 10, 3, 900, "missing tests")),
			ev("e3", mirror.EventGateRun, 2, gate("g1", "accept", 12, 7, 1500, "")),
			ev("e4", mirror.EventGateRun, 3, gate("g2", "carried", 0, 0, 0, "")),
			ev("e5", mirror.EventTransitioned, 4, map[string]string{"from": "alpha", "to": "beta"}),
			ev("e6", mirror.EventAttachmentAdded, 5, map[string]any{"name": "notes.md"}),
		},
		StoryDocs: []json.RawMessage{
			raw(mirror.Attachment{ID: "sto_1/notes.md", Item: "sto_1", Name: "notes.md", MediaType: "text/markdown", Size: 26,
				SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", At: at(5), Body: "# Heading\n\nhello **bold**"}),
			raw(mirror.Attachment{ID: "sto_1/blob.bin", Item: "sto_1", Name: "blob.bin", MediaType: "application/octet-stream", Size: 9000, At: at(6), BodyOmitted: true}),
		},
		Seats: []json.RawMessage{raw(mirror.Seat{ID: "sto_1/s1", Item: "sto_1", Session: "s1", Location: "local:/x"})},
		Docs: []json.RawMessage{
			raw(mirror.Doc{Kind: mirror.DocKindWorkflows, Name: "flow", Source: ".satelle/workflows/flow.toml", Body: "name = \"flow\"", Workflow: &fixtureWorkflow}),
		},
		Settings: raw(mirror.Settings{RepoRoot: "/p", Rows: []mirror.SettingRow{{FieldID: "items.prefix", Label: "prefix", Value: "x", Section: "items"}}}),
		Identity: raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p"}),
	}
}

// mirrorWith serves a fresh mirror and posts snap to its ingest.
func mirrorWith(t *testing.T, snap any) *httptest.Server {
	t.Helper()
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := httptest.NewServer(NewMirrorWithInstance(s, "test").Handler)
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(raw(snap)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest snapshot: %d %s", resp.StatusCode, b)
	}
	return srv
}

func inOrder(t *testing.T, body string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(body[at:], p)
		if i < 0 {
			t.Errorf("missing %q (in order after byte %d)", p, at)
			return
		}
		at += i + len(p)
	}
}

func TestStoryDetailShowsRouteVerdictsAttachments(t *testing.T) {
	srv := mirrorWith(t, fixtureSnapshot())
	for _, path := range []string{"/r/proj/story/sto_1", "/r/proj/fragment/story/sto_1"} {
		body := httpGetBody(t, srv.URL+path)
		// Fields.
		for _, want := range []string{"the body", "<li>first criterion</li>", `href="story/sto_0"`, `href="execution/exe_1"`, "ann", "cat", "flow"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing field %q", path, want)
			}
		}
		// Route: route order from the start state, park last; current and
		// visited marked; the transition out of the current state is next.
		inOrder(t, body, `<ol class="route-line"`, ">alpha<", ">beta<", ">zed<", ">held<", "</ol>")
		for _, want := range []string{
			`route-state current visited" aria-current="step">`,
			`route-state visited start"`,
			"park · resumes", "terminal</span>", "builder · build",
			`<tr class="next">`, `<td>1×</td>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: route missing %q", path, want)
			}
		}
		// Verdicts: the refused presentation, then the one that moved.
		inOrder(t, body, "<h4>Verdicts</h4>", "outcome-refused", "missing tests", "outcome-moved", "12 in · 7 out", "1.5s", "v-carried", "<h4>Attachments</h4>")
		// Attachments.
		for _, want := range []string{"<h1>Heading</h1>", "<strong>bold</strong>", "text/markdown", "0123456789ab", "body not pushed", `id="doc-notes-md"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: attachments missing %q", path, want)
			}
		}
	}
}

func TestStoryListRendersFromPushedWorkflow(t *testing.T) {
	snap := fixtureSnapshot()
	seats, home := seatsHere(t, snap.Seats)
	snap.Seats = seats
	srv := mirrorWith(t, snap)
	t.Setenv("SATELLE_HOME", home)
	body := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	fullRow := func(id string) string {
		start := strings.Index(body, rowFor(body, id))
		end := strings.Index(body[start:], "</tr>")
		return body[start : start+end]
	}
	row := fullRow("sto_1")
	for _, want := range []string{`data-status="beta"`, `data-workflow="flow"`, `data-assignee="ann"`, `badge st-active`} {
		if !strings.Contains(row, want) {
			t.Errorf("story row missing %q: %s", want, row)
		}
	}
	if parent := fullRow("sto_0"); !strings.Contains(parent, `data-terminal="1"`) || !strings.Contains(parent, "st-terminal") {
		t.Errorf("a terminal item's row is not marked terminal: %s", parent)
	}
	page := httpGetBody(t, srv.URL+"/r/proj/")
	for _, gone := range []string{`data-panel="docs"`, "Documents"} {
		if strings.Contains(page, gone) {
			t.Errorf("project page still has the removed %q", gone)
		}
	}
	if !strings.Contains(page, "1 engaged") {
		t.Error("the seat does not show as engaged")
	}
	if wf := httpGetBody(t, srv.URL+"/r/proj/fragment/workflow/flow"); !strings.Contains(wf, `<ol class="route-line"`) || strings.Contains(wf, "current") {
		t.Errorf("workflow fragment: want the route with no current state:\n%s", wf)
	}
}

// A snapshot carrying removed features' fields (waves, repo documents) and an
// older pusher's row shapes is accepted, and the pages still render.
func TestIngestAcceptsLegacySnapshot(t *testing.T) {
	legacy := map[string]any{
		"repo_key": "rk", "slug": "proj",
		"stories": []any{map[string]any{"id": "sty_1", "kind": "story", "title": "Legacy", "status": "open",
			"priority": "high", "acceptance_criteria": "- old criterion", "tags": []string{}}},
		"docs":          []any{map[string]any{"kind": "documents", "name": "design", "body": "# Design", "provenance": "authored"}},
		"waves":         []any{map[string]any{"container": "sty_1", "waves": []any{}}},
		"story_docs":    []any{map[string]any{"id": "sty_1/plan.md", "name": "plan.md", "type": "markdown", "body": "plan body"}},
		"seats":         []any{map[string]any{"id": "sty_1", "story_seat": true, "in_flight": true}},
		"ledger_events": []any{map[string]any{"id": "l1", "story_id": "sty_1", "kind": "status_transition", "body": "moved", "created_at": time.Now()}},
		"settings":      map[string]any{"repo_root": "/p", "rows": []any{}},
		"identity":      map[string]any{"project_name": "proj", "repo_root": "/p"},
	}
	srv := mirrorWith(t, legacy)
	body := httpGetBody(t, srv.URL+"/r/proj/story/sty_1")
	for _, want := range []string{"Legacy", "old criterion", "plan body", "moved", "names no workflow"} {
		if !strings.Contains(body, want) {
			t.Errorf("legacy story page missing %q", want)
		}
	}
	// A legacy seat row records no machine: it is unverified, so not engaged
	// here (sto_e127b2e6).
	if page := httpGetBody(t, srv.URL+"/r/proj/"); strings.Contains(page, "engaged</") || !strings.Contains(page, "Legacy") {
		t.Error("a legacy story seat counts as engaged here, or the page did not render")
	}
}

// Every maintained route answers, and none with a 500.
func TestRoutesAllAnswer(t *testing.T) {
	srv := mirrorWith(t, fixtureSnapshot())
	for path, code := range map[string]int{
		"/":                                 200,
		"/healthz":                          200,
		"/theme":                            200,
		"/favicon.ico":                      200,
		"/static/app.css":                   200,
		"/fragment/projects":                200,
		"/logs":                             200,
		"/fragment/logs":                    200,
		"/settings/global":                  200,
		"/r/proj/":                          200,
		"/r/proj/fragment/stories":          200,
		"/r/proj/fragment/tasks":            200,
		"/r/proj/fragment/activity":         200,
		"/r/proj/fragment/docs":             200,
		"/r/proj/fragment/engagement":       200,
		"/r/proj/fragment/backlog":          200,
		"/r/proj/fragment/story/sto_1":      200,
		"/r/proj/fragment/execution/exe_1":  200,
		"/r/proj/fragment/workflow/flow":    200,
		"/r/proj/story/sto_1":               200,
		"/r/proj/execution/exe_1":           200,
		"/r/proj/doc/workflows/flow":        200,
		"/r/proj/help":                      200,
		"/r/proj/settings":                  200,
		"/r/proj/task/tas_none":             404,
		"/r/proj/fragment/task/tas_none":    404,
		"/r/proj/story/sto_none":            404,
		"/r/proj/fragment/workflow/nothing": 404,
		"/r/proj/doc/documents/design":      404,
		"/r/nope/":                          404,
		"/r/nope/story/sto_1":               404,
		"/r/nope/fragment/docs":             404,
		"/r/nope/help":                      404,
		"/r/nope/settings":                  404,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != code {
			t.Errorf("GET %s: %d, want %d\n%.300s", path, resp.StatusCode, code, b)
		}
	}
	for _, path := range []string{"/ingest/change", "/ingest/snapshot", "/ingest/remove"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d, want 405", path, resp.StatusCode)
		}
	}
}
