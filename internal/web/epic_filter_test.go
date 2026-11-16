package web

import (
	"encoding/json"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

var rowTag = regexp.MustCompile(`<tr class="row"[^>]*>`)
var dataAttr = regexp.MustCompile(`data-([a-z-]+)="([^"]*)"`)

func rowDatasets(t *testing.T, sec string) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for _, tr := range rowTag.FindAllString(sec, -1) {
		ds := map[string]string{}
		for _, m := range dataAttr.FindAllStringSubmatch(tr, -1) {
			parts := strings.Split(m[1], "-")
			for i := 1; i < len(parts); i++ {
				parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
			}
			ds[strings.Join(parts, "")] = html.UnescapeString(m[2])
		}
		id := strings.TrimPrefix(ds["expandUrl"], "fragment/story/")
		out[id] = ds
	}
	return out
}

func jsFunc(t *testing.T, js, name string) string {
	t.Helper()
	i := strings.Index(js, "  function "+name+"(")
	if i < 0 {
		t.Fatalf("app.js has no %s", name)
	}
	j := strings.Index(js[i:], "\n  }\n")
	return js[i : i+j+4]
}

func TestEpicFilterFollowsParentLinks(t *testing.T) {
	w := progressWorkflow(same)
	story := func(id, parent string, tags ...string) Item {
		return Item{ID: id, Kind: "story", Title: id, Status: "s0", Workflow: w.Name, ParentID: parent, Tags: tags}
	}
	items := []Item{
		story("sto_epic", "", "epic", "epic:sat"),
		story("sto_tagged", "sto_epic", "epic:sat", "order:1"),
		story("sto_untagged", "sto_epic", "order:2"),
		story("sto_grand", "sto_untagged"),
		story("sto_other", "", "epic:else"),
		story("sto_orphan", "sto_missing"),
	}
	srv := mirrorWith(t, progressSnapshot([]*mirror.Workflow{w}, items, nil, nil, nil))
	page := httpGetBody(t, srv.URL+"/r/proj/")
	rows := rowDatasets(t, section(page, "stories"))
	if len(rows) != len(items) {
		t.Fatalf("rows %d, want %d", len(rows), len(items))
	}
	lineage := map[string]string{
		"sto_epic":     "",
		"sto_tagged":   "epic",
		"sto_untagged": "epic,epic:sat",
		"sto_grand":    "order:2",
		"sto_other":    "",
		"sto_orphan":   "",
	}
	for id, want := range lineage {
		if got := rows[id]["lineage"]; got != want {
			t.Errorf("%s data-lineage %q, want %q", id, got, want)
		}
	}
	if !strings.Contains(page, `data-filter="tags:epic:sat"`) {
		t.Error("the epic's epic:sat chip does not filter by tags:epic:sat")
	}

	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		`if (t.key === "tags") list = list.concat((row.dataset.lineage || "").toLowerCase().split(","));`,
		"syncFilterToURL(panel);",
		`if (fromURL !== null) input.value = fromURL;`,
		`clr.addEventListener("click", function () { input.value = ""; applyFilter(panel); });`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the filter's row match is checked only by the lines above")
	}
	data, _ := json.Marshal(rows)
	prog := "var FILTER_KEYS = { status: 1, workflow: 1, assignee: 1, category: 1, tags: 1, tag: 1, kind: 1, task: 1 };\n" +
		"var TAG_FACETS = {}; var DEFAULT_ORDER = \"updated\";\n" +
		"function collapseAll() {} function applyOrder() {} function renderChips() {}\n" +
		"var location = { href: \"http://satelled/r/proj/#stories\", search: \"\", hash: \"#stories\" };\n" +
		"var history = { replaceState: function (_, __, url) { var u = new URL(url); location.href = url; location.search = u.search; location.hash = u.hash; } };\n" +
		"var rows = " + string(data) + ";\n" +
		"var panel;\n" +
		"function load() {\n" +
		"  var input = { value: \"\", addEventListener: function () {} }, count = { textContent: \"\" };\n" +
		"  var rs = Object.keys(rows).map(function (id) { return { id: id, dataset: rows[id], style: {}, getAttribute: function () { return null; } }; });\n" +
		"  panel = { dataset: { topic: \"stories\" }, input: input, rows: rs,\n" +
		"    querySelector: function (sel) { return sel === \".filterbar input\" ? input : sel === \".filter-count\" ? count : null; },\n" +
		"    querySelectorAll: function () { return rs; } };\n" +
		"  initFilters();\n" +
		"}\n" +
		"var document = { querySelectorAll: function () { return [panel]; } };\n" +
		jsFunc(t, src, "syncFilterToURL") + jsFunc(t, src, "parseQuery") + jsFunc(t, src, "rowMatches") +
		jsFunc(t, src, "applyFilter") + jsFunc(t, src, "initFilters") +
		"function shown() { return panel.rows.filter(function (r) { return r.style.display !== \"none\"; }).map(function (r) { return r.id; }).sort(); }\n" +
		"var out = {};\n" +
		"load(); out.first = shown();\n" +
		"panel.input.value = \"tags:epic:sat\"; applyFilter(panel); out.filtered = shown(); out.url = location.search;\n" +
		"load(); out.reloaded = shown(); out.restored = panel.input.value;\n" +
		"panel.input.value = \"\"; applyFilter(panel); out.cleared = shown(); out.clearedURL = location.search;\n" +
		"panel.input.value = \"tags:epic:else\"; applyFilter(panel); out.other = shown();\n" +
		"console.log(JSON.stringify(out));\n"
	f := filepath.Join(t.TempDir(), "match.js")
	if err := os.WriteFile(f, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got struct {
		First, Filtered, Reloaded, Cleared, Other []string
		URL, Restored, ClearedURL                 string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node output %q: %v", out, err)
	}
	epic := []string{"sto_epic", "sto_tagged", "sto_untagged"}
	if !slices.Equal(got.Filtered, epic) {
		t.Errorf("tags:epic:sat shows %v, want %v: the epic and both its children, tagged or not, and no other item", got.Filtered, epic)
	}
	if got.URL != "?stories=tags%3Aepic%3Asat" {
		t.Errorf("the epic filter wrote the URL query %q, want ?stories=tags%%3Aepic%%3Asat", got.URL)
	}
	if got.Restored != "tags:epic:sat" || !slices.Equal(got.Reloaded, epic) {
		t.Errorf("after a reload the filter is %q showing %v, want tags:epic:sat showing %v", got.Restored, got.Reloaded, epic)
	}
	hidden := []string{"sto_grand", "sto_orphan", "sto_other"}
	for _, id := range hidden {
		if slices.Contains(got.Filtered, id) || !slices.Contains(got.Cleared, id) {
			t.Errorf("%s: hidden by the epic filter %v and shown once cleared %v, want both", id, !slices.Contains(got.Filtered, id), slices.Contains(got.Cleared, id))
		}
	}
	if !slices.Contains(got.Filtered, "sto_untagged") || !slices.Contains(got.Cleared, "sto_untagged") {
		t.Errorf("the untagged child is shown under the epic filter %v and after clearing it %v, want both", got.Filtered, got.Cleared)
	}
	if len(got.First) != len(items) || !slices.Equal(got.Cleared, got.First) || got.ClearedURL != "" {
		t.Errorf("clearing the epic filter shows %v with URL query %q, want the full list %v and no query", got.Cleared, got.ClearedURL, got.First)
	}
	if !slices.Equal(got.Other, []string{"sto_other"}) {
		t.Errorf("tags:epic:else shows %v, want [sto_other]", got.Other)
	}
}
