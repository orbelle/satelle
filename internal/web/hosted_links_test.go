package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// hostedPartition is a mirror partition holding stories, with hosted as its
// identity's hosted block. With no stories given it holds a story this machine
// engaged and a story another machine changed a minute ago.
func hostedPartition(t *testing.T, hosted *mirror.HostedMeta, stories ...Item) string {
	t.Helper()
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	rk, slug := "rk-hosted", "hosted"
	if _, err := s.TouchPartition(ctx, rk, slug, now); err != nil {
		t.Fatal(err)
	}
	story := func(id, title string, at time.Time) Item {
		return Item{ID: id, Kind: "story", Title: title, Status: "in_progress", CreatedAt: at, UpdatedAt: at}
	}
	if len(stories) == 0 {
		stories = []Item{story("sty_mine", "engaged here", now.Add(-72*time.Hour)), story("sty_other", "changed elsewhere", now.Add(-time.Minute))}
	}
	var rows []mirror.ItemRow
	for _, it := range stories {
		b, _ := json.Marshal(it)
		rows = append(rows, mirror.ItemRow{ID: it.ID, Payload: string(b)})
	}
	ident, _ := json.Marshal(mirror.IdentityMeta{ProjectName: slug, RepoRoot: "/repos/hosted", Hosted: hosted})
	for kind, rows := range map[string][]mirror.ItemRow{
		"story":    rows,
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

// hostedProbe is the hosted block of a partition whose checkout engaged here.
func hostedProbe(here ...string) *mirror.HostedMeta {
	return &mirror.HostedMeta{Server: "https://satelle.example/", Project: "probe", MachineStories: here}
}

// itemAt is a story with status, priority and tags, created and changed at at.
func itemAt(id, status, priority string, at time.Time, tags ...string) Item {
	return Item{ID: id, Kind: "story", Title: id + " title", Status: status, Priority: priority, Tags: tags, CreatedAt: at, UpdatedAt: at}
}

// everyStateFixture is a hosted partition over stories in backlog, in progress
// and done, one engaged at this checkout and one at another location — the
// working_on rows of the two locations resolve the here-set through
// verb.MachineSet, as the hosted push does.
func everyStateFixture(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	machine := []string{"sty_mine"}
	return hostedPartition(t, hostedProbe(machine...),
		itemAt("sty_mine", "in_progress", "high", now.Add(-72*time.Hour), "surface:ui"),
		itemAt("sty_other", "in_progress", "medium", now.Add(-time.Minute), "epic:x"),
		itemAt("sty_back", "backlog", "critical", now, "epic:x", "lane:trunk"),
		itemAt("sty_done", "done", "low", now, "surface:ui"))
}

// sty_6ce071dc AC1: in hosted mode the project page and the stories fragment list
// every story in the local store — backlog, in progress, done, and a story
// another location engaged — where sty_0dc600e1 AC5 listed only this machine's.
// sty_9ece9394 AC4: it links only pages the server serves — the project page at
// /app/p/<project> — and nothing for progress, epics or members.
func TestHostedProjectPageListsEveryStory(t *testing.T) {
	page := everyStateFixture(t)
	body := httpGetBody(t, page)
	for _, b := range []string{body, httpGetBody(t, page+"fragment/stories")} {
		for _, id := range []string{"sty_mine", "sty_other", "sty_back", "sty_done"} {
			if rowFor(b, id) == "" {
				t.Errorf("%s is in the local store but not listed:\n%s", id, b)
			}
		}
	}
	if strings.Contains(body, "This machine's stories") {
		t.Errorf("the banner still claims the list is this machine's only:\n%s", body)
	}
	if want := `href="https://satelle.example/app/p/probe"`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %s:\n%s", want, body)
	}
	for _, view := range []string{"/probe/stories", "/progress", "/epics", "/members"} {
		if strings.Contains(body, view+`"`) {
			t.Errorf("the page links %s, which the server does not serve:\n%s", view, body)
		}
	}
}

// sty_6ce071dc AC3: a snapshot ingest carrying a story this checkout has never
// seen rings the stories topic on /events, and the stories fragment the client
// refetches on that trigger (refetchPanel) then lists it — no reload.
func TestHostedLiveFollowAddsUnseenStory(t *testing.T) {
	now := time.Now().UTC()
	mine := itemAt("sty_mine", "in_progress", "high", now)
	page := hostedPartition(t, hostedProbe("sty_mine"), mine)
	base := strings.TrimSuffix(page, "/r/hosted/")
	if rowFor(httpGetBody(t, page+"fragment/stories"), "sty_new") != "" {
		t.Fatal("sty_new is listed before it is ingested")
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitBudget)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && sc.Text() != ": connected" {
	}

	raw := func(it Item) json.RawMessage { b, _ := json.Marshal(it); return b }
	snap, _ := json.Marshal(mirror.Snapshot{
		RepoKey: "rk-hosted", Slug: "hosted", Kinds: []string{"story"},
		Stories: []json.RawMessage{raw(mine), raw(itemAt("sty_new", "backlog", "low", now))},
	})
	post, err := http.Post(base+"/ingest/snapshot", "application/json", bytes.NewReader(snap))
	if err != nil {
		t.Fatal(err)
	}
	_ = post.Body.Close()
	if post.StatusCode != http.StatusOK {
		t.Fatalf("ingest/snapshot = %d", post.StatusCode)
	}

	triggered := false
	for !triggered && sc.Scan() {
		triggered = sc.Text() == "data: stories"
	}
	if !triggered {
		t.Fatalf("no stories trigger on /events after the ingest: %v", sc.Err())
	}
	rows := httpGetBody(t, page+"fragment/stories")
	if rowFor(rows, "sty_new") == "" || rowFor(rows, "sty_mine") == "" {
		t.Errorf("the refetched stories fragment does not list the unseen story beside the held one:\n%s", rows)
	}
	if strings.Contains(rowFor(rows, "sty_new"), "data-here") {
		t.Errorf("a story not engaged here is marked as here: %q", rowFor(rows, "sty_new"))
	}
}

// sty_6ce071dc AC4: the client-side filter bar (applyFilter in app.js) matches
// status:, priority: and tags: against each row's data-status, data-priority and
// data-tags. Every row of the full hosted list — engaged here, elsewhere, or not
// at all — carries those values exactly, and the page renders the filter bar, so
// the filter reaches the whole list.
func TestHostedFullListCarriesFilterAttributes(t *testing.T) {
	page := everyStateFixture(t)
	want := map[string][3]string{
		"sty_mine":  {"in_progress", "high", "surface:ui"},
		"sty_other": {"in_progress", "medium", "epic:x"},
		"sty_back":  {"backlog", "critical", "epic:x,lane:trunk"},
		"sty_done":  {"done", "low", "surface:ui"},
	}
	body := httpGetBody(t, page)
	if !strings.Contains(body, `aria-label="filter stories"`) {
		t.Errorf("the page renders no filter bar:\n%s", body)
	}
	for _, b := range []string{body, httpGetBody(t, page+"fragment/stories")} {
		for id, w := range want {
			row := rowFor(b, id)
			for _, attr := range []string{`data-status="` + w[0] + `"`, `data-priority="` + w[1] + `"`, `data-tags="` + w[2] + `"`} {
				if !strings.Contains(row, attr) {
					t.Errorf("%s's row lacks %s for the filter: %q", id, attr, row)
				}
			}
		}
	}
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, read := range []string{"dataset.status", "dataset.priority", "dataset.tags"} {
		if !strings.Contains(string(js), read) {
			t.Errorf("app.js no longer filters on %s", read)
		}
	}
	if strings.Contains(string(js), "dataset.here") {
		t.Error("app.js reads the here mark: no list may filter by it")
	}
}

// sty_0dc600e1 AC6: a local partition keeps its full story list, links nothing
// to a server and marks no story as here (sty_6ce071dc AC2).
func TestLocalProjectPageListsEveryStoryWithNoServerLinks(t *testing.T) {
	body := httpGetBody(t, hostedPartition(t, nil))
	for _, id := range []string{"sty_mine", "sty_other"} {
		if !strings.Contains(body, "fragment/story/"+id) {
			t.Errorf("the local page does not list %s:\n%s", id, body)
		}
	}
	if strings.Contains(body, "hosted-links") || strings.Contains(body, "here-mark") || strings.Contains(body, "data-here") {
		t.Errorf("a local page links to a server or marks a story:\n%s", body)
	}
}

// titleCellFor is id's title cell in body — from its row's end to the next
// cell — or "" when the row is absent.
func titleCellFor(body, id string) string {
	at := strings.Index(body, `data-expand-url="fragment/story/`+id+`"`)
	if at < 0 {
		return ""
	}
	rest := body[at:]
	start := strings.Index(rest, `<div class="wi-title">`)
	if start < 0 {
		return ""
	}
	rest = rest[start:]
	if end := strings.Index(rest, "</div>"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// rowFor is the opening <tr> tag of id's row in body, or "" when it is absent.
func rowFor(body, id string) string {
	end := strings.Index(body, `data-expand-url="fragment/story/`+id+`"`)
	if end < 0 {
		return ""
	}
	start := strings.LastIndex(body[:end], "<tr")
	if start < 0 {
		return ""
	}
	return body[start:end]
}
