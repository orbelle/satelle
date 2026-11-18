package web

import (
	"encoding/json"
	"math"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func chipsSnapshot(settings ...mirror.SettingRow) mirror.Snapshot {
	delivery := mirror.Workflow{Name: "delivery", States: []mirror.WorkflowState{{Name: "backlog", Start: true}, {Name: "shipped", Terminal: true}}}
	fix := mirror.Workflow{Name: "fix", Panel: "Drift", States: []mirror.WorkflowState{{Name: "open", Start: true}, {Name: "done", Terminal: true}}}
	assignees := map[string][]string{"sto_a": {"ann", "bob"}, "sto_d": {"cy"}}
	story := func(id, wf, cat string, tags ...string) json.RawMessage {
		st := ""
		switch wf {
		case "delivery":
			st = "backlog"
		case "fix":
			st = "open"
		}
		return raw(mirror.Item{ID: id, Kind: "story", Title: "T " + id, Status: st, Workflow: wf, Category: cat, Tags: tags, Assignees: assignees[id], CreatedAt: activityT0, UpdatedAt: activityT0})
	}
	doc := func(w mirror.Workflow) json.RawMessage {
		return raw(mirror.Doc{Kind: mirror.DocKindWorkflows, Name: w.Name, Source: ".satelle/workflows/" + w.Name + ".toml", Body: "name = \"" + w.Name + "\"", Workflow: &w})
	}
	snap := mirror.Snapshot{
		RepoKey: "rk", Slug: "proj",
		Stories: []json.RawMessage{
			story("sto_a", "delivery", "feature", "priority:high", "surface:satelled", "reporter:operator", "source:email"),
			story("sto_b", "delivery", "", "priority:medium"),
			story("sto_c", "delivery", "", "priority:low", "epic:x"),
			story("sto_n", "delivery", "", "ui"),
			story("sto_u", "delivery", "", "priority:urgent"),
			story("sto_e", "", ""),
			story("sto_d", "fix", "", "priority:high"),
		},
		Tasks: []json.RawMessage{
			raw(mirror.Item{ID: "tsk_1", Kind: "task", Title: "T tsk_1", Status: "backlog", Workflow: "delivery", Assignees: []string{"ann"}, CreatedAt: activityT0, UpdatedAt: activityT0}),
		},
		Docs:     []json.RawMessage{doc(delivery), doc(fix)},
		Identity: raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p"}),
	}
	if len(settings) > 0 {
		snap.Settings = raw(mirror.Settings{RepoRoot: "/p", Rows: settings})
	}
	return snap
}

func rowHTML(t *testing.T, html, kind, id string) string {
	t.Helper()
	for _, r := range strings.Split(html, `<tr class="row"`)[1:] {
		if strings.Contains(r, `data-expand-url="fragment/`+kind+`/`+id+`"`) {
			r, _, _ = strings.Cut(r, "</tr>")
			return r
		}
	}
	t.Fatalf("no row for %s/%s", kind, id)
	return ""
}

func workflowStyle(name string) string {
	return `style="--chip-c:hsl(` + strconv.Itoa(chipHue(name)) + ` 45% 55%)"`
}

func chipButton(tag, attrs, token string) string {
	k, v, _ := item.TagKV(tag)
	return `<button type="button" class="tagchip kv clickable"` + attrs + ` data-filter="` + token + `" aria-label="filter by ` + tag + `"><span class="k">` + k + `</span><span class="v">` + v + `</span></button>`
}

func colourAttr(tag string) string {
	return ` data-colour="` + item.ChipColour(tag, nil) + `"`
}

const storyHeader = `<thead><tr><th>ID</th><th>Title</th><th>Status</th><th>Progress</th><th>Updated</th></tr></thead>`

func TestStoriesTableHasNoWorkflowColumn(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	page := httpGetBody(t, srv.URL+"/r/proj/")
	for _, topic := range []string{"stories", "wf-drift"} {
		sec := section(page, topic)
		if !strings.Contains(sec, storyHeader) {
			t.Errorf("%s: header is not ID, Title, Status, Progress, Updated:\n%s", topic, sec)
		}
		if strings.Contains(sec, "<th>Workflow</th>") {
			t.Errorf("%s still has a Workflow column", topic)
		}
		if strings.Contains(sec, "<th>Assignees</th>") {
			t.Errorf("%s still has an Assignees column", topic)
		}
		if n := strings.Count(storyHeader, "<th>"); n != 5 {
			t.Fatalf("storyHeader has %d columns", n)
		}
	}
	for _, path := range []string{"/r/proj/fragment/stories", "/r/proj/fragment/wf-drift"} {
		frag := httpGetBody(t, srv.URL+path)
		rows := strings.Split(frag, `<tr class="row"`)[1:]
		if len(rows) == 0 {
			t.Fatalf("%s: no rows", path)
		}
		for _, r := range rows {
			if n := strings.Count(r, "<td"); n != 5 {
				t.Errorf("%s: a row has %d cells, want the header's 5:\n%s", path, n, r)
			}
		}
	}
}

func TestTasksTableKeepsWorkflowColumn(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	page := httpGetBody(t, srv.URL+"/r/proj/")
	tasks := section(page, "tasks")
	if !strings.Contains(tasks, `<thead><tr><th>ID</th><th>Title</th><th>Status</th><th>Workflow</th><th>Updated</th></tr></thead>`) {
		t.Error("the Tasks table lost its Workflow column, or keeps an Assignees column")
	}
	if strings.Contains(tasks, "<th>Assignees</th>") {
		t.Error("the Tasks table still has an Assignees column")
	}
	row := rowHTML(t, httpGetBody(t, srv.URL+"/r/proj/fragment/tasks"), "task", "tsk_1")
	if !strings.Contains(row, `<td class="meta">delivery</td>`) || strings.Count(row, "<td") != 5 {
		t.Errorf("a task row lost its Workflow cell:\n%s", row)
	}
	if strings.Contains(row, `data-filter="workflow:`) {
		t.Errorf("a task row shows a workflow chip:\n%s", row)
	}
}

func TestStoryRowWorkflowChip(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	row := rowHTML(t, frag, "story", "sto_a")
	cat := `<button type="button" class="tagchip kv cat clickable" data-filter="category:feature" aria-label="filter by category:feature"><span class="k">category</span><span class="v">feature</span></button>`
	flow := chipButton("workflow:delivery", " "+workflowStyle("delivery"), "workflow:delivery")
	if !strings.Contains(row, cat+flow) {
		t.Errorf("the workflow chip does not follow the category chip:\n%s", row)
	}
	if row := rowHTML(t, frag, "story", "sto_b"); !strings.Contains(row, `<div class="wi-tags">`+flow) {
		t.Errorf("a story with no category does not lead with its workflow chip:\n%s", row)
	}
	if row := rowHTML(t, frag, "story", "sto_e"); strings.Contains(row, "wi-tags") {
		t.Errorf("a story with no workflow, category or tags renders tags:\n%s", row)
	}
}

var workflowStyleRE = regexp.MustCompile(`style="([^"]*)" data-filter="workflow:([a-z]+)"`)

func TestWorkflowChipColourSameEverywhere(t *testing.T) {
	if chipHue("delivery") == chipHue("fix") {
		t.Fatal("the fixture workflow names share a hue")
	}
	srv := mirrorWith(t, chipsSnapshot())
	seen := map[string]map[string]bool{}
	for _, path := range []string{"/r/proj/", "/r/proj/fragment/stories", "/r/proj/fragment/wf-drift", "/r/proj/fragment/story/sto_a", "/r/proj/fragment/story/sto_d", "/r/proj/story/sto_a"} {
		body := httpGetBody(t, srv.URL+path)
		ms := workflowStyleRE.FindAllStringSubmatch(body, -1)
		if len(ms) == 0 {
			t.Errorf("%s: no workflow chip", path)
		}
		for _, m := range ms {
			if seen[m[2]] == nil {
				seen[m[2]] = map[string]bool{}
			}
			seen[m[2]][m[1]] = true
		}
	}
	for _, name := range []string{"delivery", "fix"} {
		want := strings.TrimSuffix(strings.TrimPrefix(workflowStyle(name), `style="`), `"`)
		if len(seen[name]) != 1 || !seen[name][want] {
			t.Errorf("workflow %s chip styles %v, want only %q", name, seen[name], want)
		}
	}
}

func TestPriorityChipsColouredByLevel(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	page := httpGetBody(t, srv.URL+"/r/proj/")
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	colours := map[string]bool{}
	for id, tag := range map[string]string{"sto_a": "priority:high", "sto_b": "priority:medium", "sto_c": "priority:low"} {
		chip := chipButton(tag, colourAttr(tag), tag)
		colours[item.ChipColour(tag, nil)] = true
		for name, body := range map[string]string{
			"page":     rowHTML(t, section(page, "stories"), "story", id),
			"fragment": rowHTML(t, frag, "story", id),
			"detail":   httpGetBody(t, srv.URL+"/r/proj/fragment/story/"+id),
		} {
			if !strings.Contains(body, chip) {
				t.Errorf("%s %s: no chip %s", name, id, chip)
			}
		}
	}
	if len(colours) != 3 || colours[""] {
		t.Errorf("the three levels render colours %v, want three", colours)
	}
}

func TestPriorityIsOptional(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	for _, id := range []string{"sto_n", "sto_e"} {
		row := rowHTML(t, frag, "story", id)
		if strings.Contains(row, `<span class="k">priority</span>`) || strings.Contains(row, "data-tag-priority") {
			t.Errorf("%s has no priority tag but renders one:\n%s", id, row)
		}
	}
	if detail := httpGetBody(t, srv.URL+"/r/proj/fragment/story/sto_n"); strings.Contains(detail, `<span class="k">priority</span>`) {
		t.Errorf("sto_n's detail renders a priority chip:\n%s", detail)
	}
}

func TestConventionalChipsDefaultColours(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	row := rowHTML(t, httpGetBody(t, srv.URL+"/r/proj/fragment/stories"), "story", "sto_a")
	detail := httpGetBody(t, srv.URL+"/r/proj/fragment/story/sto_a")
	for _, tag := range []string{"surface:satelled", "reporter:operator", "source:email"} {
		if item.ChipColour(tag, nil) == "" {
			t.Fatalf("%s has no default colour", tag)
		}
		chip := chipButton(tag, colourAttr(tag), "tags:"+tag)
		if !strings.Contains(row, chip) || !strings.Contains(detail, chip) {
			t.Errorf("no default-coloured chip %s:\n%s", chip, row)
		}
	}
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	if c := rowHTML(t, frag, "story", "sto_c"); !strings.Contains(c, chipButton("epic:x", "", "tags:epic:x")) {
		t.Errorf("epic:x is coloured with no override:\n%s", c)
	}
}

func TestTagColoursOverride(t *testing.T) {
	setting := func(tag, colour string) mirror.SettingRow {
		return mirror.SettingRow{FieldID: "ui.tag_colours." + tag, Label: tag, Value: colour, Section: "ui"}
	}
	srv := mirrorWith(t, chipsSnapshot(
		setting("priority:high", "ok"),
		setting("surface", "warn"),
		setting("epic:x", "note"),
		setting("workflow:delivery", "accent"),
		setting("assignee", "note"),
	))
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	a := rowHTML(t, frag, "story", "sto_a")
	c := rowHTML(t, frag, "story", "sto_c")
	b := rowHTML(t, frag, "story", "sto_b")
	for _, w := range []struct{ row, chip string }{
		{a, chipButton("priority:high", ` data-colour="ok"`, "priority:high")},
		{a, chipButton("surface:satelled", ` data-colour="warn"`, "tags:surface:satelled")},
		{a, chipButton("reporter:operator", colourAttr("reporter:operator"), "tags:reporter:operator")},
		{a, chipButton("workflow:delivery", ` data-colour="accent"`, "workflow:delivery")},
		{c, chipButton("epic:x", ` data-colour="note"`, "tags:epic:x")},
		{a, chipButton("assignee:ann", ` data-colour="note"`, "assignee:ann")},
		{b, chipButton("priority:medium", colourAttr("priority:medium"), "priority:medium")},
	} {
		if !strings.Contains(w.row, w.chip) {
			t.Errorf("no chip %s in:\n%s", w.chip, w.row)
		}
	}
	if strings.Contains(a, `style="--chip-c`) {
		t.Errorf("an overridden workflow chip keeps its hue:\n%s", a)
	}
	if detail := httpGetBody(t, srv.URL+"/r/proj/fragment/story/sto_a"); !strings.Contains(detail, chipButton("priority:high", ` data-colour="ok"`, "priority:high")) ||
		!strings.Contains(detail, chipButton("workflow:delivery", ` data-colour="accent"`, "workflow:delivery")) {
		t.Errorf("the detail ignores the override:\n%s", detail)
	}
	if fix := rowHTML(t, httpGetBody(t, srv.URL+"/r/proj/fragment/wf-drift"), "story", "sto_d"); !strings.Contains(fix, chipButton("priority:high", ` data-colour="ok"`, "priority:high")) {
		t.Errorf("the panel ignores the override:\n%s", fix)
	}
}

func TestColourRules(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	css := httpGetBody(t, srv.URL+"/static/app.css")
	root := decls(t, css, ":root")
	for _, n := range item.ChipColours {
		got := decls(t, css, `.tagchip[data-colour="`+n+`"]`)
		if got["--chip-c"] != "var(--"+n+")" {
			t.Errorf(`.tagchip[data-colour="%s"] sets --chip-c %q`, n, got["--chip-c"])
		}
		if root["--"+n] == "" {
			t.Errorf(":root declares no --%s", n)
		}
	}
	if decls(t, css, `[data-theme="dark"]`)["--note"] == "" {
		t.Error("the dark theme declares no --note")
	}
	plain := cssComment.ReplaceAllString(css, "")
	for _, m := range regexp.MustCompile(`([^{}]*)\{`).FindAllStringSubmatch(plain, -1) {
		for _, key := range item.Conventions {
			if strings.Contains(m[1], key.Key) {
				t.Errorf("selector %q names the tag key %s", strings.TrimSpace(m[1]), key.Key)
			}
		}
	}
}

func TestChipFilterTokens(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	page := httpGetBody(t, srv.URL+"/r/proj/")
	if !strings.Contains(page, `<body data-tag-facets="priority">`) {
		t.Error(`the page body does not carry data-tag-facets="priority"`)
	}
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	for name, html := range map[string]string{"page": section(page, "stories"), "fragment": frag} {
		a := rowHTML(t, html, "story", "sto_a")
		if !strings.Contains(a, `data-filter="workflow:delivery"`) || !strings.Contains(a, `data-workflow="delivery"`) {
			t.Errorf("%s: the workflow chip or the row's data-workflow is missing:\n%s", name, a)
		}
		for id, level := range map[string]string{"sto_a": "high", "sto_b": "medium", "sto_c": "low"} {
			row := rowHTML(t, html, "story", id)
			if !strings.Contains(row, `data-filter="priority:`+level+`"`) || !strings.Contains(row, ` data-tag-priority="`+level+`"`) {
				t.Errorf("%s %s: no priority:%s token or data-tag-priority:\n%s", name, id, level, row)
			}
		}
		u := rowHTML(t, html, "story", "sto_u")
		if !strings.Contains(u, chipButton("priority:urgent", "", "priority:urgent")) {
			t.Errorf("%s: priority:urgent does not emit its own token uncoloured:\n%s", name, u)
		}
		if !strings.Contains(a, `data-filter="tags:surface:satelled"`) {
			t.Errorf("%s: surface:satelled does not keep its tags: token", name)
		}
	}
	detail := httpGetBody(t, srv.URL+"/r/proj/fragment/story/sto_a")
	for _, tok := range []string{"workflow:delivery", "priority:high"} {
		if !strings.Contains(detail, `data-filter="`+tok+`"`) {
			t.Errorf("detail: no %s token", tok)
		}
	}
}

func TestFilterPlaceholderNamesWorkflowAndPriority(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	page := httpGetBody(t, srv.URL+"/r/proj/")
	for _, topic := range []string{"stories", "wf-drift"} {
		m := regexp.MustCompile(`placeholder="([^"]*)"`).FindStringSubmatch(section(page, topic))
		if m == nil || !strings.Contains(m[1], "workflow:name") || !strings.Contains(m[1], "priority:high") {
			t.Errorf("%s: placeholder %v does not name workflow:name and priority:high", topic, m)
		}
	}
}

func TestFacetScript(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	js := httpGetBody(t, srv.URL+"/static/app.js")
	if !strings.Contains(js, "document.body.dataset.tagFacets") {
		t.Error("app.js does not read the page's data-tag-facets")
	}
	if !strings.Contains(js, "FILTER_KEYS[key] || TAG_FACETS[key]") {
		t.Error("parseQuery does not accept a tag facet key")
	}
	_, branch, ok := strings.Cut(js, "} else if (TAG_FACETS[t.key]) {")
	if !ok {
		t.Fatal("rowMatches has no TAG_FACETS[t.key] branch")
	}
	branch, _, _ = strings.Cut(branch, "} else")
	if !strings.Contains(branch, `"data-tag-" + t.key`) || strings.Contains(branch, "dataset.search") {
		t.Errorf("the facet branch does not match the row's data-tag-<key>:\n%s", branch)
	}
	for _, key := range item.FacetKeys() {
		if strings.Contains(js, key) {
			t.Errorf("app.js names the tag key %s", key)
		}
	}
	m := regexp.MustCompile(`var FILTER_KEYS = \{([^}]*)\}`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("no FILTER_KEYS literal")
	}
	for _, kv := range strings.Split(m[1], ",") {
		k, _, _ := strings.Cut(strings.TrimSpace(kv), ":")
		if slices.Contains(item.FacetKeys(), strings.TrimSpace(k)) {
			t.Errorf("FILTER_KEYS names the facet key %s", k)
		}
	}
}

const (
	progressWidthBefore = 14.0
	workflowColumnWidth = 6.0
)

func remOf(t *testing.T, sel, v string) float64 {
	t.Helper()
	n, err := strconv.ParseFloat(strings.TrimSuffix(v, "rem"), 64)
	if err != nil || !strings.HasSuffix(v, "rem") || n <= 0 {
		t.Errorf("%s width %q is not a positive rem width", sel, v)
		return 0
	}
	return n
}

func TestStoriesTableLayoutRules(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	css := httpGetBody(t, srv.URL+"/static/app.css")
	plain := cssComment.ReplaceAllString(css, "")
	if got := decls(t, css, ".stories-table")["table-layout"]; got != "fixed" {
		t.Errorf(".stories-table table-layout %q, want fixed", got)
	}
	if n := strings.Count(plain, "table-layout"); n != 1 {
		t.Errorf("the stylesheet declares table-layout %d times, want once", n)
	}
	if strings.Contains(plain, ".col-assignees") {
		t.Error("the stylesheet still sizes an Assignees column")
	}
	for _, sel := range []string{".col-id", ".col-status", ".col-updated"} {
		remOf(t, sel, decls(t, css, sel)["width"])
	}
	if w := remOf(t, ".col-reviews", decls(t, css, ".col-reviews")["width"]); w < progressWidthBefore+workflowColumnWidth {
		t.Errorf(".col-reviews is %vrem, want at least %vrem", w, progressWidthBefore+workflowColumnWidth)
	}
	if strings.Contains(plain, ".col-title") || strings.Contains(plain, "col:nth-child") {
		t.Error("the stylesheet fixes the Title column's width")
	}
	page := httpGetBody(t, srv.URL+"/r/proj/")
	cols := `<table class="panel-table stories-table"><colgroup><col class="col-id"><col class="col-title"><col class="col-status"><col class="col-reviews"><col class="col-updated"></colgroup>`
	for _, topic := range []string{"stories", "wf-drift"} {
		if !strings.Contains(section(page, topic), cols) {
			t.Errorf("%s: the table does not open with the stories-table class and its five columns", topic)
		}
	}
	if strings.Contains(section(page, "tasks"), "stories-table") {
		t.Error("the Tasks table carries the stories-table class")
	}
}

func TestStoriesTableScrollsInItsOwnBox(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	css := httpGetBody(t, srv.URL+"/static/app.css")
	box := decls(t, css, ".table-scroll")
	if box["overflow-x"] != "auto" {
		t.Errorf(".table-scroll overflow-x %q, want auto: the table scrolls only when it is wider than the page", box["overflow-x"])
	}
	if box["max-width"] != "100%" {
		t.Errorf(".table-scroll max-width %q, want 100%%: the box never grows past the page", box["max-width"])
	}
	page := httpGetBody(t, srv.URL+"/r/proj/")
	for _, topic := range []string{"stories", "wf-drift"} {
		sec := section(page, topic)
		if !strings.Contains(sec, `<div class="table-scroll"><table class="panel-table stories-table">`) || !strings.Contains(sec, "</table></div>") {
			t.Errorf("%s: the stories table is not inside a .table-scroll box", topic)
		}
	}
}

func assigneeChip(user string) string {
	return `<button type="button" class="tagchip kv assignee clickable" data-filter="assignee:` + user + `" aria-label="filter by assignee:` + user + `"><span class="k">assignee</span><span class="v">` + user + `</span></button>`
}

func TestStoryRowAssigneeChips(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	page := httpGetBody(t, srv.URL+"/r/proj/")
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	flow := chipButton("workflow:delivery", " "+workflowStyle("delivery"), "workflow:delivery")
	firstTag := chipButton("priority:high", colourAttr("priority:high"), "priority:high")
	for name, html := range map[string]string{"page": section(page, "stories"), "fragment": frag} {
		a := rowHTML(t, html, "story", "sto_a")
		if !strings.Contains(a, flow+assigneeChip("ann")+assigneeChip("bob")+firstTag) {
			t.Errorf("%s: the assignee chips do not follow the workflow chip and lead the tags:\n%s", name, a)
		}
		if strings.Contains(a, `class="w"`) {
			t.Errorf("%s: a non-working assignee is marked working:\n%s", name, a)
		}
		b := rowHTML(t, html, "story", "sto_b")
		if strings.Contains(b, `data-filter="assignee:`) || strings.Contains(b, `<span class="k">assignee</span>`) {
			t.Errorf("%s: a story with no assignee shows an assignee chip:\n%s", name, b)
		}
		outside := strings.Replace(b, `<td class="col-reviews">—</td>`, "", 1)
		if !strings.Contains(b, `data-assignee=""`) || strings.Contains(outside, "—") {
			t.Errorf("%s: a story with no assignee shows something in its place:\n%s", name, b)
		}
	}
	if d := rowHTML(t, httpGetBody(t, srv.URL+"/r/proj/fragment/wf-drift"), "story", "sto_d"); !strings.Contains(d, assigneeChip("cy")) {
		t.Errorf("a kind panel row lacks its assignee chip:\n%s", d)
	}
	if task := rowHTML(t, httpGetBody(t, srv.URL+"/r/proj/fragment/tasks"), "task", "tsk_1"); !strings.Contains(task, assigneeChip("ann")) {
		t.Errorf("a task row lacks its assignee chip:\n%s", task)
	}
}

func hexHue(t *testing.T, v string) float64 {
	t.Helper()
	v = strings.TrimSpace(v)
	if len(v) != 7 || v[0] != '#' {
		t.Fatalf("%q is not a #rrggbb colour", v)
	}
	ch := func(i int) float64 {
		n, err := strconv.ParseUint(v[i:i+2], 16, 8)
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		return float64(n) / 255
	}
	r, g, b := ch(1), ch(3), ch(5)
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	d := mx - mn
	if d == 0 {
		return -1
	}
	var h float64
	switch mx {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h
}

func hueGap(a, b float64) float64 {
	d := math.Abs(a - b)
	return math.Min(d, 360-d)
}

func TestAssigneeChipColour(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	css := httpGetBody(t, srv.URL+"/static/app.css")
	plain := cssComment.ReplaceAllString(css, "")
	v := decls(t, css, ".tagchip.kv.assignee")["--chip-c"]
	if strings.HasPrefix(v, "var(") {
		t.Fatalf(".tagchip.kv.assignee --chip-c %q is a variable, not one fixed colour", v)
	}
	if n := strings.Count(plain, ".tagchip.kv.assignee"); n != 1 {
		t.Errorf(".tagchip.kv.assignee is declared %d times, want once for both themes", n)
	}
	h := hexHue(t, v)
	if h < 0 {
		t.Fatalf("%s is grey", v)
	}
	if h >= 170 && h <= 249 {
		t.Errorf("hue %.0f lies in the workflow chips' range 170–249", h)
	}
	others := map[string]string{
		".tagchip.kv.cat": decls(t, css, ".tagchip.kv.cat")["--chip-c"],
		".tagchip.kv":     decls(t, css, ".tagchip.kv")["--chip-c"],
	}
	for _, st := range []string{"start", "active", "park", "terminal"} {
		others["st-"+st] = decls(t, css, ".badge.st-"+st)["--badge-c"]
	}
	for _, theme := range []string{":root", `[data-theme="dark"]`} {
		vars := decls(t, css, theme)
		for _, n := range []string{"fail", "warn", "ok", "accent", "note"} {
			if c := vars["--"+n]; c != "" {
				others[theme+" --"+n] = c
			}
		}
		if vars["--muted"] == v {
			t.Errorf("the assignee colour equals %s --muted", theme)
		}
	}
	for name, c := range others {
		oh := hexHue(t, c)
		if oh < 0 {
			continue
		}
		if g := hueGap(h, oh); g < 30 {
			t.Errorf("assignee hue %.0f is %.0f° from %s (%s)", h, g, name, c)
		}
	}
	row := rowHTML(t, httpGetBody(t, srv.URL+"/r/proj/fragment/stories"), "story", "sto_a")
	if strings.Contains(row, `data-filter="assignee:ann" style=`) || !strings.Contains(row, `class="tagchip kv assignee clickable" data-filter="assignee:ann"`) {
		t.Errorf("the assignee chip is not coloured by its class alone:\n%s", row)
	}
}

func TestAssigneeChipFilters(t *testing.T) {
	srv := mirrorWith(t, chipsSnapshot())
	page := httpGetBody(t, srv.URL+"/r/proj/")
	sec := section(page, "stories")
	if !strings.Contains(sec, assigneeChip("ann")) {
		t.Fatalf("no assignee:ann button chip:\n%s", sec)
	}
	if strings.Contains(page, `data-filter="tags:assignee:`) {
		t.Error("an assignee chip filters as a tag")
	}
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{`var chip = e.target.closest(".tagchip[data-filter]");`, "addFilterToken(panel, chip.dataset.filter)"} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the chip's filter is checked only by the lines above")
	}
	data, _ := json.Marshal(rowDatasets(t, sec))
	prog := "var FILTER_KEYS = { status: 1, workflow: 1, assignee: 1, category: 1, tags: 1, tag: 1, kind: 1, task: 1, engaged: 1 };\n" +
		"var TAG_FACETS = {};\n" + jsFunc(t, src, "parseQuery") + jsFunc(t, src, "rowMatches") +
		"var applied = 0; function applyFilter() { applied++; }\n" + jsFunc(t, src, "addFilterToken") +
		"var rows = " + string(data) + ";\n" +
		"var match = function (q) { var p = parseQuery(q); return Object.keys(rows).filter(function (id) {\n" +
		"  var r = { dataset: rows[id], getAttribute: function () { return null; } }; return rowMatches(r, p); }).sort(); };\n" +
		"var input = { value: \"status:all\" }; var panel = { querySelector: function () { return input; } };\n" +
		"addFilterToken(panel, \"assignee:ann\");\n" +
		"console.log(JSON.stringify({ input: input.value, applied: applied, ann: match(input.value), typed: match(\"status:all assignee:bob,cy\") }));\n"
	out, err := exec.Command(node, "-e", prog).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got struct {
		Input   string
		Applied int
		Ann     []string
		Typed   []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node output %q: %v", out, err)
	}
	if got.Input != "status:all assignee:ann" || got.Applied != 1 {
		t.Errorf("clicking the chip left the filter %q (applied %d), want %q applied once", got.Input, got.Applied, "status:all assignee:ann")
	}
	if strings.Join(got.Ann, ",") != "sto_a" {
		t.Errorf("assignee:ann matches %v, want [sto_a]", got.Ann)
	}
	if strings.Join(got.Typed, ",") != "sto_a" {
		t.Errorf("typed assignee:bob,cy matches %v, want [sto_a]", got.Typed)
	}
}
