package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// activityPartition is a mirror partition holding a running story, a running
// execution of tsk_x, a backlog story and a done execution.
func activityPartition(t *testing.T) string {
	t.Helper()
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	rk, slug := "rk-activity", "activity"
	if _, err := s.TouchPartition(ctx, rk, slug, now); err != nil {
		t.Fatal(err)
	}
	item := func(id string, kind workitem.Kind, status, parent string) mirror.ItemRow {
		b, _ := json.Marshal(workitem.Item{ID: id, Kind: kind, Title: id + " title", Status: status, Priority: "high", ParentID: parent, CreatedAt: now, UpdatedAt: now})
		return mirror.ItemRow{ID: id, Payload: string(b)}
	}
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: slug, RepoRoot: "/repos/activity"})
	for kind, rows := range map[string][]mirror.ItemRow{
		"story": {
			item("sty_run", workitem.KindStory, "in_progress", ""),
			item("sty_back", workitem.KindStory, "backlog", ""),
		},
		"execution": {
			item("exe_run", workitem.KindExecution, "in_progress", "tsk_x"),
			item("exe_done", workitem.KindExecution, "done", "tsk_x"),
		},
		"identity": {{ID: "meta", Payload: string(ident)}},
	} {
		if err := s.ReplaceKind(ctx, rk, kind, rows, now); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(NewMirrorWithInstance(s, "test").Handler)
	t.Cleanup(ts.Close)
	return ts.URL + "/r/" + slug + "/"
}

// panelOf is the section of a project page holding panel topic; a body with no
// such section (a rows fragment) is returned whole.
func panelOf(body, topic string) string {
	i := strings.Index(body, `id="panel-`+topic+`"`)
	if i < 0 {
		return body
	}
	body = body[i:]
	if end := strings.Index(body, "</section>"); end >= 0 {
		body = body[:end]
	}
	return body
}

// activityRowsIn is the rows of the Activity panel in body, by id.
func activityRowsIn(t *testing.T, body string) map[string]string {
	t.Helper()
	rows := map[string]string{}
	for _, tr := range regexp.MustCompile(`(?s)<tr class="row".*?</tr>`).FindAllString(panelOf(body, "activity"), -1) {
		m := regexp.MustCompile(`data-expand-url="fragment/[a-z]+/([a-z0-9_]+)"`).FindStringSubmatch(tr)
		if m != nil {
			rows[m[1]] = tr
		}
	}
	return rows
}

// sty_c356ed4f AC1: the Activity tab lists every story and task execution under
// way — not backlog, not finished — in the Stories table, each row carrying its
// kind for the kind: filter and an execution naming its task as a task: chip,
// with the count in the tab label.
func TestActivityTabListsStoriesAndExecutionsUnderWay(t *testing.T) {
	page := activityPartition(t)
	body := httpGetBody(t, page)

	tab := regexp.MustCompile(`(?s)<a class="tab" role="tab" data-panel="activity"[^>]*>.*?</a>`).FindString(body)
	if !strings.Contains(tab, "Activity") || !strings.Contains(tab, `<span class="n">2</span>`) {
		t.Errorf("Activity tab missing or its count is not 2: %q", tab)
	}
	for _, order := range []string{`data-panel="stories"`, `data-panel="activity"`, `data-panel="tasks"`, `data-panel="workflow"`, `data-panel="docs"`} {
		if !strings.Contains(body, order) {
			t.Errorf("tab strip lacks %s", order)
		}
	}
	if !strings.Contains(body, `<th>ID</th><th>Title</th><th>Status</th><th>Progress</th><th>Priority</th><th>Updated</th>`) {
		t.Error("Activity panel does not carry the Stories table columns")
	}

	for name, b := range map[string]string{"page": body, "fragment": httpGetBody(t, page+"fragment/activity")} {
		rows := activityRowsIn(t, b)
		if len(rows) != 2 || rows["sty_run"] == "" || rows["exe_run"] == "" {
			t.Fatalf("%s: Activity rows = %v, want exactly sty_run and exe_run", name, keys(rows))
		}
		if !strings.Contains(rows["sty_run"], `data-kind="story"`) {
			t.Errorf("%s: story row lacks data-kind=story: %q", name, rows["sty_run"])
		}
		exe := rows["exe_run"]
		for _, want := range []string{`data-kind="execution"`, `data-task="tsk_x"`, `data-filter="task:tsk_x"`, `<span class="k">task</span><span class="v">tsk_x</span>`} {
			if !strings.Contains(exe, want) {
				t.Errorf("%s: execution row lacks %s: %q", name, want, exe)
			}
		}
		if strings.Contains(rows["sty_run"], `<span class="k">task</span>`) {
			t.Errorf("%s: a story row carries a task chip: %q", name, rows["sty_run"])
		}
	}

	// The execution row expands like any row.
	if d := httpGetBody(t, page+"fragment/execution/exe_run"); !strings.Contains(d, "exe_run") {
		t.Errorf("execution fragment does not render the run: %q", d)
	}

	// kind: filters through the shared filter bar: the key is a filter facet the
	// generic dataset branch reads, and the Activity panel takes the item-panel
	// default chips.
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`kind: 1`, `task: 1`, `row.dataset[t.key]`, `t === "activity"`, `"activity"`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
