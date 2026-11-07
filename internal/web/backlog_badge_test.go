package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// backlogSnapshot holds one story per status, in order, all on workflow w.
func backlogSnapshot(w mirror.Workflow, statuses ...string) mirror.Snapshot {
	stories := []json.RawMessage{}
	for i, st := range statuses {
		id := "sto_" + string(rune('a'+i))
		stories = append(stories, raw(mirror.Item{ID: id, Kind: "story", Title: "T " + id, Status: st, Workflow: w.Name, CreatedAt: activityT0, UpdatedAt: activityT0}))
	}
	return mirror.Snapshot{
		RepoKey: "rk", Slug: "proj",
		Stories: stories,
		Docs: []json.RawMessage{
			raw(mirror.Doc{Kind: mirror.DocKindWorkflows, Name: w.Name, Source: ".satelle/workflows/" + w.Name + ".toml", Body: "name = \"" + w.Name + "\"", Workflow: &w}),
		},
		Identity: raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p"}),
	}
}

// storiesTabBacklogRE finds the backlog chip inside the Stories tab anchor.
var storiesTabBacklogRE = regexp.MustCompile(`<a class="tab"[^>]*data-panel="stories"[^>]*>[^\n]*?<span class="n-backlog"[^>]*>(\d+) backlog</span></a>`)

// A fresh page load renders the 'N backlog' badge server-side in the Stories
// tab, counting the stories in the workflow's start state (alpha in the
// fixture); with none there is no badge.
func TestBacklogBadgeOnFreshLoad(t *testing.T) {
	srv := mirrorWith(t, backlogSnapshot(fixtureWorkflow, "alpha", "beta", "alpha"))
	page := httpGetBody(t, srv.URL+"/r/proj/")
	if m := storiesTabBacklogRE.FindStringSubmatch(page); m == nil || m[1] != "2" {
		t.Fatalf("Stories tab backlog badge = %v, want 2", m)
	}
	if !strings.Contains(page, `data-backlog-count="2"`) {
		t.Error("the badge lacks its count attribute")
	}
	none := mirrorWith(t, backlogSnapshot(fixtureWorkflow, "beta", "zed"))
	if p := httpGetBody(t, none.URL+"/r/proj/"); strings.Contains(p, `class="n-backlog"`) {
		t.Error("a partition with no backlog story renders a backlog badge")
	}
}

// The badge follows the workflow's start state, not a state name: a workflow
// whose start state is "queued" counts its queued stories, and a state that
// is merely called "backlog" is not counted.
func TestBacklogBadgeFollowsWorkflowStartState(t *testing.T) {
	queue := mirror.Workflow{Name: "queue", States: []mirror.WorkflowState{
		{Name: "queued", Start: true}, {Name: "backlog"}, {Name: "fin", Terminal: true},
	}}
	srv := mirrorWith(t, backlogSnapshot(queue, "queued", "backlog", "queued", "queued", "fin"))
	if m := storiesTabBacklogRE.FindStringSubmatch(httpGetBody(t, srv.URL+"/r/proj/")); m == nil || m[1] != "3" {
		t.Fatalf("Stories tab backlog badge = %v, want 3", m)
	}
	if f := httpGetBody(t, srv.URL+"/r/proj/fragment/backlog"); !strings.Contains(f, ">3 backlog<") {
		t.Errorf("fragment = %q, want 3 backlog", f)
	}
}

// The badge's fragment follows each ingest, and a fresh page agrees with it.
func TestBacklogBadgeFollowsIngest(t *testing.T) {
	srv := mirrorWith(t, backlogSnapshot(fixtureWorkflow, "alpha", "beta", "alpha"))
	if f := httpGetBody(t, srv.URL+"/r/proj/fragment/backlog"); !strings.Contains(f, ">2 backlog<") {
		t.Fatalf("fragment = %q, want 2 backlog", f)
	}
	ingest := func(snap mirror.Snapshot) {
		t.Helper()
		resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(raw(snap)))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("ingest: %d %s", resp.StatusCode, b)
		}
	}
	ingest(backlogSnapshot(fixtureWorkflow, "alpha", "beta", "zed"))
	if f := httpGetBody(t, srv.URL+"/r/proj/fragment/backlog"); !strings.Contains(f, ">1 backlog<") {
		t.Errorf("fragment after ingest = %q, want 1 backlog", f)
	}
	if m := storiesTabBacklogRE.FindStringSubmatch(httpGetBody(t, srv.URL+"/r/proj/")); m == nil || m[1] != "1" {
		t.Errorf("page badge after ingest = %v, want 1", m)
	}
	ingest(backlogSnapshot(fixtureWorkflow, "beta", "zed"))
	if f := strings.TrimSpace(httpGetBody(t, srv.URL+"/r/proj/fragment/backlog")); f != "" {
		t.Errorf("fragment with no backlog = %q, want empty", f)
	}
}

// The live refresh swaps in the server's fragment; no client code counts rows
// by a state name.
func TestBacklogBadgeLiveWiring(t *testing.T) {
	b, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	_, fn, ok := strings.Cut(js, "function refreshBacklogBadge(")
	if !ok {
		t.Fatal("app.js has no refreshBacklogBadge")
	}
	fn, _, _ = strings.Cut(fn, "\n  }\n")
	if !strings.Contains(fn, `fetch("fragment/backlog")`) {
		t.Error("refreshBacklogBadge does not fetch fragment/backlog")
	}
	if strings.Contains(js, `data-status="backlog"`) {
		t.Error(`app.js still matches rows by data-status="backlog"`)
	}
	if !strings.Contains(js, "refreshBacklogBadge();") {
		t.Error("the stories refetch does not refresh the backlog badge")
	}
}
