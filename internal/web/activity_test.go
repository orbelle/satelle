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
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

var activityT0 = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

func activityAt(m int) time.Time { return activityT0.Add(time.Duration(m) * time.Minute) }

// activitySnapshot holds every Activity case on the fixture workflow (alpha
// start, beta active with performer builder, held park, zed terminal).
// exeStatus is exe_run's state.
func activitySnapshot(exeStatus string) mirror.Snapshot {
	item := func(id, kind, status string) json.RawMessage {
		return raw(mirror.Item{ID: id, Kind: kind, Title: "T " + id, Status: status, Workflow: "flow", CreatedAt: activityT0, UpdatedAt: activityT0})
	}
	ev := func(id, itemID, kind string, m int, p any) json.RawMessage {
		return raw(mirror.Event{ID: id, Item: itemID, Kind: kind, At: activityAt(m), Payload: raw(p)})
	}
	seat := func(itemID string) json.RawMessage {
		return raw(mirror.Seat{ID: itemID + "/s1", Item: itemID, Session: "s1", Location: "local:/x", BoundAt: activityT0})
	}
	return mirror.Snapshot{
		RepoKey: "rk", Slug: "proj",
		Stories: []json.RawMessage{
			item("sto_done", "story", "zed"), item("sto_back", "story", "alpha"), item("sto_seat", "story", "alpha"),
			item("sto_work", "story", "beta"), item("sto_park", "story", "held"),
			raw(mirror.Item{ID: "sto_lost", Kind: "story", Title: "T sto_lost", Status: "nowhere", Workflow: "unpushed", CreatedAt: activityT0, UpdatedAt: activityT0}),
			raw(mirror.Item{ID: "sto_lost_seat", Kind: "story", Title: "T sto_lost_seat", Status: "nowhere", Workflow: "unpushed", CreatedAt: activityT0, UpdatedAt: activityT0}),
		},
		Executions: []json.RawMessage{item("exe_done", "execution", "zed"), item("exe_run", "execution", exeStatus)},
		LedgerEvents: []json.RawMessage{
			ev("e1", "sto_seat", mirror.EventGateRun, 1, mirror.GateRun{Transition: "alpha->beta", Gate: "g1", Result: "reject"}),
			ev("e2", "sto_seat", mirror.EventGateRun, 2, mirror.GateRun{Transition: "alpha->beta", Gate: "g1", Result: "accept"}),
			ev("e3", "exe_run", mirror.EventTransitioned, 3, map[string]string{"from": "alpha", "to": "beta"}),
		},
		Seats: []json.RawMessage{seat("sto_done"), seat("sto_seat"), seat("sto_lost_seat")},
		Docs: []json.RawMessage{
			raw(mirror.Doc{Kind: mirror.DocKindWorkflows, Name: "flow", Source: ".satelle/workflows/flow.toml", Body: "name = \"flow\"", Workflow: &fixtureWorkflow}),
		},
		Identity: raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p"}),
	}
}

var activityRowRE = regexp.MustCompile(`(?s)<tr class="row".*?</tr>`)

// activityRowsOf maps each Activity row's item id to its markup.
func activityRowsOf(t *testing.T, html string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, row := range activityRowRE.FindAllString(html, -1) {
		m := regexp.MustCompile(`data-id="([^"]+)"`).FindStringSubmatch(row)
		if m == nil {
			t.Fatalf("an Activity row without an id: %s", row)
		}
		out[m[1]] = row
	}
	return out
}

// activityPanel cuts the Activity panel out of the project page.
func activityPanel(t *testing.T, page string) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, `id="panel-activity"`)
	if !ok {
		t.Fatal("no Activity panel")
	}
	panel, _, _ := strings.Cut(rest, "</section>")
	return panel
}

// Only items being worked are listed: terminal never, active and parked
// always, a start state (or one the workflow does not name) only when seated.
func TestActivityListsOnlyActiveItems(t *testing.T) {
	srv := mirrorWith(t, activitySnapshot("beta"))
	want := []string{"sto_seat", "sto_work", "sto_park", "sto_lost_seat", "exe_run"}
	notWant := []string{"sto_done", "sto_back", "sto_lost", "exe_done"}
	for name, html := range map[string]string{
		"page":     activityPanel(t, httpGetBody(t, srv.URL+"/r/proj/")),
		"fragment": httpGetBody(t, srv.URL+"/r/proj/fragment/activity"),
	} {
		rows := activityRowsOf(t, html)
		for _, id := range want {
			if _, ok := rows[id]; !ok {
				t.Errorf("%s: %s is not listed", name, id)
			}
		}
		for _, id := range notWant {
			if _, ok := rows[id]; ok {
				t.Errorf("%s: %s is listed", name, id)
			}
		}
		if strings.Contains(html, "data-terminal") {
			t.Errorf("%s: a row is marked terminal", name)
		}
	}
}

// Each row is condensed agent activity: kind, state, who or what works it,
// and the latest step with its time.
func TestActivityRowIsCondensedAgentActivity(t *testing.T) {
	srv := mirrorWith(t, activitySnapshot("beta"))
	rows := activityRowsOf(t, httpGetBody(t, srv.URL+"/r/proj/fragment/activity"))
	inOrder(t, rows["sto_seat"], "story", ">alpha<", "s1 · local:/x", "g1 accept", activityAt(2).Local().Format("2006-01-02 15:04"))
	if strings.Contains(rows["sto_seat"], "g1 reject") {
		t.Error("sto_seat shows an older step")
	}
	inOrder(t, rows["exe_run"], "execution", ">beta<", ">builder<", "alpha → beta", activityAt(3).Local().Format("2006-01-02 15:04"))
	inOrder(t, rows["sto_park"], `act-working">—<`, `act-step">—<`)
	// The most recent step comes first.
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/activity")
	if strings.Index(frag, `data-id="exe_run"`) > strings.Index(frag, `data-id="sto_seat"`) {
		t.Error("rows are not ordered by their latest step")
	}
}

func TestLatestStep(t *testing.T) {
	gate := func(id string, m int, g, res string) Entry {
		return Entry{ID: id, Kind: mirror.EventGateRun, At: activityAt(m), Payload: raw(mirror.GateRun{Transition: "a->b", Gate: g, Result: res})}
	}
	move := func(id string, m int) Entry {
		return Entry{ID: id, Kind: mirror.EventTransitioned, At: activityAt(m), Payload: raw(map[string]string{"from": "a", "to": "b"})}
	}
	other := Entry{ID: "z", Kind: "attachment.added", At: activityAt(9)}
	for _, c := range []struct {
		name string
		in   []Entry
		want string
	}{
		{"empty", nil, ""},
		{"gate only", []Entry{gate("1", 1, "g", "accept")}, "g accept"},
		{"transition only", []Entry{move("1", 1), other}, "a → b"},
		{"newest wins", []Entry{move("2", 2), gate("1", 1, "g", "reject")}, "a → b"},
		{"tie broken by id", []Entry{gate("2", 1, "late", "accept"), gate("1", 1, "early", "accept")}, "late accept"},
	} {
		step, _, _, ok := latestStep(c.in)
		if step != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %v, want %q", c.name, step, ok, c.want)
		}
	}
}

// The tab counts the active items, and an ingest that ends a run drops it.
func TestActivityCountFollowsIngest(t *testing.T) {
	srv := mirrorWith(t, activitySnapshot("beta"))
	tab := regexp.MustCompile(`data-panel="activity"[^>]*>.*?<span class="n">(\d+)</span>`)
	if m := tab.FindStringSubmatch(httpGetBody(t, srv.URL+"/r/proj/")); m == nil || m[1] != "5" {
		t.Fatalf("Activity tab count = %v, want 5", m)
	}
	resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(raw(activitySnapshot("zed"))))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second ingest: %d %s", resp.StatusCode, b)
	}
	frag := httpGetBody(t, srv.URL+"/r/proj/fragment/activity")
	if _, ok := activityRowsOf(t, frag)["exe_run"]; ok || strings.Count(frag, `<tr class="row"`) != 4 {
		t.Errorf("after the run ended the fragment holds %d rows:\n%s", strings.Count(frag, `<tr class="row"`), frag)
	}
	if m := tab.FindStringSubmatch(httpGetBody(t, srv.URL+"/r/proj/")); m == nil || m[1] != "4" {
		t.Errorf("Activity tab count after the run ended = %v, want 4", m)
	}
	empty := mirrorWith(t, mirror.Snapshot{RepoKey: "rk", Slug: "proj"})
	if f := httpGetBody(t, empty.URL+"/r/proj/fragment/activity"); strings.Contains(f, `class="row"`) || !strings.Contains(f, "no agent activity") {
		t.Errorf("empty partition's Activity:\n%s", f)
	}
}

// The live stream refetches Activity on the stories and tasks doorbells and
// resets the tab count from the refetched rows.
func TestActivityLiveWiring(t *testing.T) {
	b, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		`var PANELS = ["stories", "activity", "tasks", "queues", "workflow"]`,
		`var LIVE_TOPICS = ["stories", "activity", "tasks", "queues"]`,
		`var DERIVED_TOPICS = { stories: ["activity", "queues"], tasks: ["activity", "queues"] }`,
		`t === "queues"`,
		`if (kind === "queue") return "queues";`,
		`refetch[tp] = debounce(function () { refetchPanel(tp); }, 250)`,
		`(DERIVED_TOPICS[ev.data] || []).forEach(function (tp) { if (refetch[tp]) refetch[tp](); })`,
		`(DERIVED_TOPICS[ev.data] || []).indexOf(detailTopic) !== -1`,
		`tab.textContent = panel.querySelectorAll("[data-rows] .row").length`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
}

var engagedChipRE = regexp.MustCompile(`<span class="n-engaged[^>]*>[^<]*</span>`)

// A task run's seat and a terminal story's seat are not stories engaged: the
// Stories tab's chip counts only the seated non-terminal stories, and the
// run's seat shows on Activity instead.
func TestEngagedBadgeIgnoresTaskRunSeats(t *testing.T) {
	snap := activitySnapshot("beta")
	seats, home := seatsHere(t, append(snap.Seats, raw(mirror.Seat{ID: "exe_run/s1", Item: "exe_run", Session: "s1", Location: "local:/x", BoundAt: activityT0})))
	snap.Seats = seats
	srv := mirrorWith(t, snap)
	t.Setenv("SATELLE_HOME", home)
	page := httpGetBody(t, srv.URL+"/r/proj/")
	for name, html := range map[string]string{
		"page":     page,
		"fragment": httpGetBody(t, srv.URL+"/r/proj/fragment/engagement"),
	} {
		chip := engagedChipRE.FindString(html)
		if chip == "" {
			t.Fatalf("%s: no engaged chip:\n%s", name, html)
		}
		for _, want := range []string{`data-engagement-count="2"`, `title="engaged: sto_lost_seat, sto_seat"`, ">2 engaged<"} {
			if !strings.Contains(chip, want) {
				t.Errorf("%s: chip lacks %s: %s", name, want, chip)
			}
		}
		for _, notWant := range []string{"exe_run", "sto_done"} {
			if strings.Contains(chip, notWant) {
				t.Errorf("%s: chip names %s: %s", name, notWant, chip)
			}
		}
	}
	row, ok := activityRowsOf(t, activityPanel(t, page))["exe_run"]
	if !ok {
		t.Fatal("the seated run is not on Activity")
	}
	if !strings.Contains(row, "s1 · local:/x") {
		t.Errorf("the run's Activity row lacks its seat: %s", row)
	}
}
