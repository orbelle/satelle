package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func panelSnapshot(d1Status string) mirror.Snapshot {
	delivery := mirror.Workflow{Name: "delivery", States: []mirror.WorkflowState{{Name: "backlog", Start: true}, {Name: "shipped", Terminal: true}}}
	fix := mirror.Workflow{Name: "fix", Panel: "Drift", States: []mirror.WorkflowState{{Name: "open", Start: true}, {Name: "done", Terminal: true}}}
	story := func(id, wf, st string) json.RawMessage {
		return raw(mirror.Item{ID: id, Kind: "story", Title: "T " + id, Status: st, Workflow: wf, CreatedAt: activityT0, UpdatedAt: activityT0})
	}
	doc := func(w mirror.Workflow) json.RawMessage {
		return raw(mirror.Doc{Kind: mirror.DocKindWorkflows, Name: w.Name, Source: ".satelle/workflows/" + w.Name + ".toml", Body: "name = \"" + w.Name + "\"", Workflow: &w})
	}
	return mirror.Snapshot{
		RepoKey: "rk", Slug: "proj",
		Stories:  []json.RawMessage{story("sto_s1", "delivery", "backlog"), story("sto_d1", "fix", d1Status), story("sto_d2", "fix", "done")},
		Docs:     []json.RawMessage{doc(delivery), doc(fix)},
		Identity: raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p"}),
	}
}

var (
	storiesTabRE = regexp.MustCompile(`data-panel="stories" href="#stories"><span class="tab-label" data-text="Stories">Stories</span> <span class="n">(\d+)</span>`)
	panelTabRE   = regexp.MustCompile(`<a class="tab tab-wf[^"]*" role="tab" data-panel="wf-drift" data-wf-panel href="#wf-drift">.*?</a>`)
	rowIDRE      = regexp.MustCompile(`data-id="(sto_[a-z0-9]+)"`)
)

func rowIDs(html string) []string {
	var out []string
	for _, m := range rowIDRE.FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}

func section(page, topic string) string {
	_, rest, ok := strings.Cut(page, `<section class="panel" data-topic="`+topic+`"`)
	if !ok {
		return ""
	}
	rest, _, _ = strings.Cut(rest, "</section>")
	return rest
}

func TestWorkflowPanelTab(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rk := config.RepoKey(dir)
	snap := panelSnapshot("open")
	snap.RepoKey = rk
	srv := mirrorWith(t, snap)
	addPath(t, dir)
	page := httpGetBody(t, srv.URL+"/r/proj/")

	if m := storiesTabRE.FindStringSubmatch(page); m == nil || m[1] != "1" {
		t.Fatalf("Stories tab count = %v, want 1", m)
	}
	if got := rowIDs(section(page, "stories")); strings.Join(got, ",") != "sto_s1" {
		t.Fatalf("Stories rows %v, want only sto_s1", got)
	}
	if m := storiesTabBacklogRE.FindStringSubmatch(page); m == nil || m[1] != "1" {
		t.Fatalf("backlog badge = %v, want 1 (the drift item in its start state is not counted)", m)
	}
	tab := panelTabRE.FindString(page)
	if tab == "" || !strings.Contains(tab, `class="tab tab-wf tab-alert"`) || !strings.Contains(tab, `>Drift</span> <span class="n">1</span>`) {
		t.Fatalf("Drift tab = %q, want label Drift, count 1 and tab-alert", tab)
	}
	if got := rowIDs(section(page, "wf-drift")); strings.Join(got, ",") != "sto_d1,sto_d2" {
		t.Fatalf("Drift panel rows %v, want sto_d1 and sto_d2", got)
	}
	if f := httpGetBody(t, srv.URL+"/r/proj/fragment/tab/wf-drift"); f != tab {
		t.Fatalf("fragment/tab/wf-drift = %q, want the page's tab %q", f, tab)
	}

	if got := rowIDs(httpGetBody(t, srv.URL+"/r/proj/fragment/stories")); strings.Join(got, ",") != "sto_s1" {
		t.Fatalf("fragment/stories %v, want only sto_s1", got)
	}
	if got := rowIDs(httpGetBody(t, srv.URL+"/r/proj/fragment/wf-drift")); strings.Join(got, ",") != "sto_d1,sto_d2" {
		t.Fatalf("fragment/wf-drift %v, want sto_d1 and sto_d2", got)
	}
	for _, p := range []string{"/r/proj/fragment/tab/wf-nope", "/r/proj/fragment/wf-nope"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", p, resp.StatusCode)
		}
	}
	if d := httpGetBody(t, srv.URL+"/r/proj/story/sto_d1"); !strings.Contains(d, `?expand=sto_d1#wf-drift"`) {
		t.Error("the drift item's back link does not return to its panel")
	}
	if land := httpGetBody(t, srv.URL+"/"); !strings.Contains(land, `<td class="n-stories"><span class="n">1</span>`) {
		t.Error("the projects list does not count Stories as 1")
	}

	done := panelSnapshot("done")
	done.RepoKey = rk
	resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(raw(done)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest: %d %s", resp.StatusCode, b)
	}
	f := httpGetBody(t, srv.URL+"/r/proj/fragment/tab/wf-drift")
	if strings.Contains(f, "tab-alert") || !strings.Contains(f, `<span class="n">0</span>`) {
		t.Fatalf("fragment/tab/wf-drift after d1 is done = %q, want count 0 and no tab-alert", f)
	}
}

func TestWorkflowPanelLiveWiring(t *testing.T) {
	b, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	_, fn, ok := strings.Cut(js, "function refreshPanelTab(")
	if !ok {
		t.Fatal("app.js has no refreshPanelTab")
	}
	fn, _, _ = strings.Cut(fn, "\n  }\n")
	for _, want := range []string{`fetch("fragment/tab/" + topic)`, "cur.className = next.className", `next.querySelector(".n")`, "n.textContent = nn.textContent"} {
		if !strings.Contains(fn, want) {
			t.Errorf("refreshPanelTab lacks %q", want)
		}
	}
	if strings.Contains(fn, ".length") {
		t.Error("refreshPanelTab counts rows")
	}
	_, refetch, ok := strings.Cut(js, "function refetchPanel(")
	if !ok {
		t.Fatal("app.js has no refetchPanel")
	}
	refetch, _, _ = strings.Cut(refetch, "\n  }\n")
	_, other, ok := strings.Cut(refetch, `if (topic.indexOf("wf-") === 0) {`)
	if !ok {
		t.Fatal(`refetchPanel has no wf- test`)
	}
	then, elseBranch, ok := strings.Cut(other, "} else {")
	if !ok || !strings.Contains(then, "refreshPanelTab(topic);") {
		t.Fatal("refetchPanel does not call refreshPanelTab under its wf- test")
	}
	elseBranch, _, _ = strings.Cut(elseBranch, "\n        }\n")
	if !strings.Contains(elseBranch, `querySelectorAll("[data-rows] .row").length`) {
		t.Error("the row count is not inside the else of the wf- test")
	}
	if strings.Count(refetch, `querySelectorAll("[data-rows] .row").length`) != 1 {
		t.Error("refetchPanel counts rows outside the else")
	}
	for _, want := range []string{
		`document.querySelectorAll(".tab[data-wf-panel]")`,
		"PANELS.push(tp)",
		"LIVE_TOPICS.push(tp)",
		"DERIVED_TOPICS.stories.push(tp)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	if i, j := strings.Index(js, "LIVE_TOPICS.push(tp)"), strings.Index(js, "var refetch = {};"); i < 0 || j < i {
		t.Error("the panels join LIVE_TOPICS after the refetch map is built")
	}
}
