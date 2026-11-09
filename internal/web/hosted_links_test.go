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
	return hostedPartitionSeats(t, hosted, nil, stories...)
}

// hostedPartitionSeats is hostedPartition with seats pushed as well.
func hostedPartitionSeats(t *testing.T, hosted *mirror.HostedMeta, seats []mirror.Seat, stories ...Item) string {
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
	var seatRows []mirror.ItemRow
	for _, st := range seats {
		b, _ := json.Marshal(st)
		seatRows = append(seatRows, mirror.ItemRow{ID: st.ID, Payload: string(b)})
	}
	for kind, rows := range map[string][]mirror.ItemRow{
		"story":    rows,
		"identity": {{ID: "meta", Payload: string(ident)}},
		"seat":     seatRows,
	} {
		if err := s.ReplaceKind(ctx, rk, kind, rows, now); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(NewMirrorWithInstance(s, "test").Handler)
	t.Cleanup(ts.Close)
	return ts.URL + "/r/" + slug + "/"
}

// hostedProbe is the hosted block of a partition: the here-set comes from the
// seats classed here, not from the block.
func hostedProbe() *mirror.HostedMeta {
	return &mirror.HostedMeta{Server: "https://satelle.example/", Project: "probe"}
}

// itemAt is a story with status, priority and tags, created and changed at at.
func itemAt(id, status, priority string, at time.Time, tags ...string) Item {
	return Item{ID: id, Kind: "story", Title: id + " title", Status: status, Priority: priority, Tags: tags, CreatedAt: at, UpdatedAt: at}
}

// everyStateFixture is a hosted partition over stories in backlog, in progress
// and done, each seated once: sty_mine here (this machine, session alive),
// sty_other on another machine, sty_back by a session that ended, sty_done
// with no machine recorded (fourSeatHome).
func everyStateFixture(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	seats, _ := fourSeatHome(t, "sty_mine", "sty_other", "sty_back", "sty_done")
	return hostedPartitionSeats(t, hostedProbe(), seats,
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
	// The story seated here is marked ◆ here; the one on another machine
	// carries a mark at that machine; the one whose session ended carries
	// nothing. A seat with no machine recorded is unverified and counts as
	// here (◇ here), and a story with no pushed workflow cannot be proven
	// terminal, so sty_done is marked too (sto_44a18f5b).
	for id, w := range map[string]struct{ engaged, here, where, title string }{
		"sty_mine":  {engaged: "here", here: "◆ here"},
		"sty_other": {engaged: "elsewhere", where: "◆ other-host", title: "on other-host (m-other)"},
		"sty_back":  {},
		"sty_done":  {engaged: "here", here: "◇ here", title: "unverified: no machine recorded"},
	} {
		row, cell := rowFor(body, id), titleCellFor(body, id)
		if w.engaged == "" {
			if strings.Contains(row, "data-engaged") || strings.Contains(cell, "here-mark") || strings.Contains(cell, "where-mark") {
				t.Errorf("%s carries an engaged mark: %q %q", id, row, cell)
			}
			continue
		}
		if !strings.Contains(row, `data-engaged="`+w.engaged+`"`) {
			t.Errorf("%s lacks data-engaged=%q: %q", id, w.engaged, row)
		}
		if w.here != "" {
			if !strings.Contains(cell, ">"+w.here+"<") || strings.Contains(cell, "where-mark") {
				t.Errorf("%s is not marked %s alone: %q", id, w.here, cell)
			}
			if unv := strings.Contains(cell, "here-unverified"); unv != (w.here == "◇ here") {
				t.Errorf("%s here-unverified = %v: %q", id, unv, cell)
			}
		} else if strings.Contains(cell, `class="here-mark`) || strings.Count(cell, `class="where-mark"`) != 1 || !strings.Contains(cell, ">"+w.where+"<") {
			t.Errorf("%s is not marked %s alone: %q", id, w.where, cell)
		}
		if w.title != "" {
			mark := w.here
			if mark == "" {
				mark = w.where
			}
			if got := markTitle(t, cell, mark); !strings.Contains(got, w.title) {
				t.Errorf("%s's %s hover %q lacks %q", id, mark, got, w.title)
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
	page := hostedPartition(t, hostedProbe(), mine)
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
	if strings.Contains(rowFor(rows, "sty_new"), "data-engaged") {
		t.Errorf("a story not engaged here is marked as here: %q", rowFor(rows, "sty_new"))
	}
}

// sty_6ce071dc AC4: the client-side filter bar (applyFilter in app.js) matches
// status: and tags: against each row's data-status and data-tags. Every row of the full hosted list — engaged here, elsewhere, or not
// at all — carries those values exactly, and the page renders the filter bar, so
// the filter reaches the whole list.
func TestHostedFullListCarriesFilterAttributes(t *testing.T) {
	page := everyStateFixture(t)
	want := map[string][2]string{
		"sty_mine":  {"in_progress", "surface:ui"},
		"sty_other": {"in_progress", "epic:x"},
		"sty_back":  {"backlog", "epic:x,lane:trunk"},
		"sty_done":  {"done", "surface:ui"},
	}
	body := httpGetBody(t, page)
	if !strings.Contains(body, `aria-label="filter stories"`) {
		t.Errorf("the page renders no filter bar:\n%s", body)
	}
	for _, b := range []string{body, httpGetBody(t, page+"fragment/stories")} {
		for id, w := range want {
			row := rowFor(b, id)
			for _, attr := range []string{`data-status="` + w[0] + `"`, `data-tags="` + w[1] + `"`} {
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
	for _, read := range []string{"dataset.status", "dataset.terminal", "dataset.tags"} {
		if !strings.Contains(string(js), read) {
			t.Errorf("app.js no longer filters on %s", read)
		}
	}
	if strings.Contains(string(js), "dataset.engaged") {
		t.Error("app.js reads the engaged mark: no list filters by it until sto_18f1e94d's chip does")
	}
}

// sty_0dc600e1 AC6: a local partition keeps its full story list and links
// nothing to a server. Nothing is seated, so no story is marked: the marks
// follow seats in every mode, not whether the page is hosted (sto_44a18f5b).
func TestLocalProjectPageListsEveryStoryWithNoServerLinks(t *testing.T) {
	body := httpGetBody(t, hostedPartition(t, nil))
	for _, id := range []string{"sty_mine", "sty_other"} {
		if !strings.Contains(body, "fragment/story/"+id) {
			t.Errorf("the local page does not list %s:\n%s", id, body)
		}
	}
	if strings.Contains(body, "hosted-links") || strings.Contains(body, "here-mark") || strings.Contains(body, "where-mark") || strings.Contains(body, "data-engaged") {
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
