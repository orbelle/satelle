package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/mirror"
)

// queueSnapshot: que_1 filters status alpha, holds sto_add explicitly, and
// fired exe_q (status beta) over two items.
func queueSnapshot() mirror.Snapshot {
	it := func(id, kind, status string) json.RawMessage {
		return raw(mirror.Item{ID: id, Kind: kind, Title: "T " + id, Status: status, Workflow: "flow", CreatedAt: activityT0, UpdatedAt: activityT0})
	}
	ev := func(id, itemID, kind string, m int, p any) json.RawMessage {
		return raw(mirror.Event{ID: id, Item: itemID, Kind: kind, At: activityAt(m), Actor: "ann", Payload: raw(p)})
	}
	return mirror.Snapshot{
		RepoKey: "rk", Slug: "proj",
		Stories: []json.RawMessage{it("sto_a", "story", "alpha"), it("sto_b", "story", "beta"), it("sto_add", "story", "zed")},
		Tasks:   []json.RawMessage{it("tas_1", "task", "alpha")},
		Executions: []json.RawMessage{
			raw(mirror.Item{ID: "exe_q", Kind: "execution", Title: "run", Status: "beta", ParentID: "tas_1", Workflow: "flow", CreatedAt: activityT0, UpdatedAt: activityT0}),
		},
		Queues: []json.RawMessage{raw(mirror.Item{ID: "que_1", Kind: "queue", Title: "Release queue", Status: "alpha", Workflow: "flow",
			Inputs: &item.Selection{Kind: "story", Status: "alpha"}, Trigger: &item.Trigger{Task: "tas_1"},
			CreatedAt: activityT0, UpdatedAt: activityT0})},
		LedgerEvents: []json.RawMessage{
			ev("q1", "que_1", item.KindQueueAdded, 1, item.QueueEntry{Item: "sto_old"}),
			ev("q2", "que_1", item.KindQueueFired, 2, item.QueueFire{Run: "exe_q", Task: "tas_1", Cause: "by hand"}),
			ev("x1", "exe_q", "task.run", 2, map[string]any{"task": "tas_1", "inputs": []string{"sto_old", "sto_x"}, "queue": "que_1"}),
			ev("q3", "que_1", item.KindQueueAdded, 3, item.QueueEntry{Item: "sto_add"}),
		},
		Docs: []json.RawMessage{
			raw(mirror.Doc{Kind: mirror.DocKindWorkflows, Name: "flow", Source: ".satelle/workflows/flow.toml", Body: "name = \"flow\"", Workflow: &fixtureWorkflow}),
		},
		Identity: raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p"}),
	}
}

func TestQueuesPanel(t *testing.T) {
	srv := mirrorWith(t, queueSnapshot())
	page := httpGetBody(t, srv.URL+"/r/proj/")
	if !regexp.MustCompile(`data-panel="queues" href="#queues"><span class="tab-label" data-text="Queues">Queues</span> <span class="n">1</span>`).MatchString(page) {
		t.Fatal("no Queues tab with count 1")
	}
	if !strings.Contains(page, `<section class="panel" data-topic="queues" id="panel-queues">`) {
		t.Fatal("no Queues panel")
	}
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/queues")
	inOrder(t, frag, "que_1", "Release queue", "filter kind:story status:alpha",
		`<span class="n">2</span>`, `data-member="sto_add"`, "added", `data-member="sto_a"`,
		"task tas_1, by hand", "by hand", `data-run="exe_q"`, ">beta<", "2 items")
	if strings.Contains(frag, `data-member="sto_b"`) || strings.Contains(frag, `data-member="sto_old"`) {
		t.Fatalf("lists a non-member:\n%s", frag)
	}
	if !strings.Contains(frag, `data-expand-url="fragment/queue/que_1"`) {
		t.Fatal("row does not expand")
	}
	httpGetBody(t, srv.URL+"/r/proj/fragment/queue/que_1")
	httpGetBody(t, srv.URL+"/r/proj/queue/que_1")

	// A delta moves sto_b into the filter's status: the same fragment, the
	// one a queues refetch swaps in, now lists it.
	moved := raw(mirror.Item{ID: "sto_b", Kind: "story", Title: "T sto_b", Status: "alpha", Workflow: "flow", CreatedAt: activityT0, UpdatedAt: activityAt(9)})
	delta := mirror.Snapshot{RepoKey: "rk", Slug: "proj", Stories: []json.RawMessage{moved}, Kinds: []string{"identity"},
		MergeKinds: []string{"story", "task", "execution", "queue", "ledger_event", "story_doc"},
		Identity:   raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p"})}
	resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(raw(delta)))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("delta: %v %v", resp, err)
	}
	resp.Body.Close()
	if frag := httpGetBody(t, srv.URL+"/r/proj/fragment/queues"); !strings.Contains(frag, `data-member="sto_b"`) {
		t.Fatalf("delta not reflected:\n%s", frag)
	}
}

var jsArray = func(js, name string) []string {
	m := regexp.MustCompile(`var ` + name + ` = \[([^\]]*)\]`).FindStringSubmatch(js)
	if m == nil {
		return nil
	}
	var out []string
	for _, s := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, s[1])
	}
	return out
}

// Every tab reaches the client and every panel with a rows endpoint is live:
// the rule app.js states above LIVE_TOPICS, checked against the rendered page.
func TestPanelsReachClient(t *testing.T) {
	srv := mirrorWith(t, queueSnapshot())
	page := httpGetBody(t, srv.URL+"/r/proj/")
	b, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	panels, live := jsArray(js, "PANELS"), jsArray(js, "LIVE_TOPICS")
	has := func(ss []string, s string) bool {
		for _, x := range ss {
			if x == s {
				return true
			}
		}
		return false
	}
	var tabs []string
	for _, m := range regexp.MustCompile(`class="tab" role="tab" data-panel="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		tabs = append(tabs, m[1])
		if !has(panels, m[1]) {
			t.Errorf("tab %s is not in PANELS", m[1])
		}
	}
	answers := func(topic string) bool {
		resp, err := http.Get(srv.URL + "/r/proj/fragment/" + topic)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}
	var topics []string
	for _, m := range regexp.MustCompile(`<section class="panel" data-topic="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		topics = append(topics, m[1])
		if answers(m[1]) && !has(live, m[1]) {
			t.Errorf("panel %s has a rows fragment but is not in LIVE_TOPICS", m[1])
		}
	}
	for _, tp := range live {
		if !has(topics, tp) || !answers(tp) {
			t.Errorf("LIVE_TOPICS %s has no panel or no rows fragment", tp)
		}
	}
	if !has(tabs, "queues") || !has(live, "queues") {
		t.Fatalf("queues: tabs %v live %v", tabs, live)
	}
}
