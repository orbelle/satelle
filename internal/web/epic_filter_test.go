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
		"sto_grand":    "order:2,epic,epic:sat",
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
		"var TAG_FACETS = {};\n" + jsFunc(t, src, "parseQuery") + jsFunc(t, src, "rowMatches") +
		"var rows = " + string(data) + ";\n" +
		"var match = function (q) { var p = parseQuery(q); return Object.keys(rows).filter(function (id) {\n" +
		"  var r = { dataset: rows[id], getAttribute: function () { return null; } }; return rowMatches(r, p); }).sort(); };\n" +
		"console.log(JSON.stringify({ epic: match(\"tags:epic:sat\"), cleared: match(\"\"), other: match(\"tags:epic:else\") }));\n"
	f := filepath.Join(t.TempDir(), "match.js")
	if err := os.WriteFile(f, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got struct{ Epic, Cleared, Other []string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node output %q: %v", out, err)
	}
	if want := []string{"sto_epic", "sto_grand", "sto_tagged", "sto_untagged"}; !slices.Equal(got.Epic, want) {
		t.Errorf("tags:epic:sat shows %v, want %v: the epic and every story under it, tagged or not", got.Epic, want)
	}
	if !slices.Equal(got.Other, []string{"sto_other"}) {
		t.Errorf("tags:epic:else shows %v, want [sto_other]", got.Other)
	}
	if len(got.Cleared) != len(items) {
		t.Errorf("a cleared filter shows %v, want all %d", got.Cleared, len(items))
	}
}
