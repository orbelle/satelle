package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

func TestEngagedFilterListsWorkedStories(t *testing.T) {
	w := progressWorkflow(same)
	var items []Item
	story := func(id, status string) *hist {
		items = append(items, Item{ID: id, Kind: "story", Title: id, Status: status, Workflow: w.Name})
		return newHist(id, same).place("s0")
	}
	mine, far, both, idle, done := story("sto_mine", "s0"), story("sto_far", "s0"), story("sto_both", "s0"), story("sto_idle", "s0"), story("sto_done", "t1")
	_ = idle
	seat := func(h *hist, session string) mirror.Seat {
		return mirror.Seat{ID: h.id + "/" + session, Item: h.id, Session: session, User: "bob", Location: "local:/p", BoundAt: h.last()}
	}
	away := func(s mirror.Seat) mirror.Seat {
		s.Machine, s.Host, s.User = "m-other", "other-host", "ann"
		return s
	}
	here, home := seatsHere(t, []json.RawMessage{raw(seat(mine, "s-mine")), raw(seat(both, "s-both")), raw(seat(done, "s-done"))})
	snap := progressSnapshot([]*mirror.Workflow{w}, items, nil, nil, nil)
	snap.Seats = append(here, raw(away(seat(far, "s-far"))), raw(away(seat(both, "s-both-far"))))
	srv := mirrorWith(t, snap)
	t.Setenv("SATELLE_HOME", home)

	page := httpGetBody(t, srv.URL+"/r/proj/")
	for name, html := range map[string]string{"page": page, "fragment": httpGetBody(t, srv.URL+"/r/proj/fragment/engagement")} {
		for _, want := range []string{
			`<span class="n-engaged has-engaged" data-engagement-count="2"`, `data-filter-token="engaged:here" aria-label="list the stories engaged here">2 engaged</button>`,
			`<span class="n-elsewhere" data-elsewhere-count="2"`, `data-filter-token="engaged:elsewhere" aria-label="list the stories engaged elsewhere">2 elsewhere</button>`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("%s: the header lacks %s", name, want)
			}
		}
	}
	rows := rowDatasets(t, section(page, "stories"))
	for id, want := range map[string]string{"sto_mine": "here", "sto_far": "elsewhere", "sto_both": "here,elsewhere", "sto_idle": "", "sto_done": ""} {
		if got := rows[id]["engaged"]; got != want {
			t.Errorf("%s data-engaged %q, want %q", id, got, want)
		}
	}

	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		"tags: 1, tag: 1, kind: 1, task: 1, engaged: 1 };",
		`t.key === "tags" || t.key === "assignee" || t.key === "engaged"`,
		`document.addEventListener("click", function (e) {`,
		`var b = e.target.closest("[data-filter-token]");`,
		"    initEngagedFilter();\n",
		"        holder.innerHTML = html;\n        applyFilter(panel);",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the filter's row match is checked only by the lines above")
	}
	run := func(rows map[string]map[string]string) map[string][]string {
		t.Helper()
		data, _ := json.Marshal(rows)
		prog := "var FILTER_KEYS = { status: 1, workflow: 1, assignee: 1, category: 1, tags: 1, tag: 1, kind: 1, task: 1, engaged: 1 };\n" +
			"var TAG_FACETS = {};\n" + jsFunc(t, src, "parseQuery") + jsFunc(t, src, "rowMatches") +
			"var applied = 0; function applyFilter() { applied++; }\n" + jsFunc(t, src, "setFilterToken") +
			"var rows = " + string(data) + ";\n" +
			"var match = function (q) { var p = parseQuery(q); return Object.keys(rows).filter(function (id) {\n" +
			"  var r = { dataset: rows[id], getAttribute: function () { return null; } }; return rowMatches(r, p); }).sort(); };\n" +
			"var input = { value: \"order:updated engaged:here\" }; var panel = { querySelector: function () { return input; } };\n" +
			"setFilterToken(panel, \"engaged\", \"engaged:elsewhere\");\n" +
			"console.log(JSON.stringify({ here: match(\"engaged:here\"), elsewhere: match(\"engaged:elsewhere\"), switched: [input.value, String(applied)], all: match(\"status:all engaged:here\") }));\n"
		f := filepath.Join(t.TempDir(), "engaged.js")
		if err := os.WriteFile(f, []byte(prog), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(node, f).CombinedOutput()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, out)
		}
		var got map[string][]string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("node output %q: %v", out, err)
		}
		return got
	}
	got := run(rows)
	if want := []string{"sto_both", "sto_mine"}; !slices.Equal(got["here"], want) {
		t.Errorf("engaged:here lists %v, want %v", got["here"], want)
	}
	if want := []string{"sto_both", "sto_far"}; !slices.Equal(got["elsewhere"], want) {
		t.Errorf("engaged:elsewhere lists %v, want %v", got["elsewhere"], want)
	}
	if !slices.Equal(got["all"], []string{"sto_both", "sto_mine"}) {
		t.Errorf("status:all engaged:here lists %v: a seat on a done story must not match", got["all"])
	}
	if !slices.Equal(got["switched"], []string{"order:updated engaged:elsewhere", "1"}) {
		t.Errorf("clicking elsewhere after here leaves the filter %v, want it switched and applied once", got["switched"])
	}

	snap.Seats = here[:1]
	resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(raw(snap)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	frag := rowDatasets(t, httpGetBody(t, srv.URL+"/r/proj/fragment/stories"))
	if got := run(frag)["here"]; !slices.Equal(got, []string{"sto_mine"}) {
		t.Errorf("after sto_both's seats are released the refetched rows list %v under engaged:here, want [sto_mine]", got)
	}
}
