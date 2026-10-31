package cli

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/verb"
)

type workingJSON struct {
	ID        string `json:"id"`
	WorkingOn *struct {
		Location string `json:"location"`
		Label    string `json:"label"`
		Session  string `json:"session"`
		Worktree string `json:"worktree"`
		State    string `json:"state"`
		At       string `json:"at"`
		Advisory bool   `json:"advisory"`
		Text     string `json:"text"`
	} `json:"working_on"`
}

// `story get` and `story list` mark a story whose latest working_on row names
// another location (sty_40853d02) — in text and as a `working_on` object — and
// leave every other story unmarked: one last worked on here, one nobody worked
// on, and one that finished (a terminal story has no one on it).
func TestStoryGetAndListMarkAStoryAnotherLocationWorks(t *testing.T) {
	fx := newHoldFixture(t, holdNoGit)
	theirs := createStoryID(t, "worked on elsewhere")
	older := createStoryID(t, "worked on elsewhere then here")
	mine := createStoryID(t, "worked on here")
	none := createStoryID(t, "worked on by nobody")
	done := createStoryID(t, "finished elsewhere")
	putInState(t, done, "done", "", "", false)

	now := time.Now().UTC()
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	other := verb.WorkingOnPayload{Location: otherLoc, Label: "desk", Session: "sess_other", Worktree: "/work/desk", State: "in_progress"}
	appendWorkingOn(t, a, theirs, other, now.Add(-90*time.Minute))
	appendWorkingOn(t, a, older, other, now.Add(-3*time.Hour))
	appendWorkingOn(t, a, older, verb.WorkingOnPayload{Location: fx.loc, Session: "sess_here", State: "in_progress"}, now.Add(-time.Hour))
	appendWorkingOn(t, a, mine, verb.WorkingOnPayload{Location: fx.loc, Session: "sess_here", State: "in_progress"}, now.Add(-time.Hour))
	appendWorkingOn(t, a, done, other, now.Add(-time.Hour))
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	get := func(id string) (workingJSON, string) {
		out, err := runRoot(t, "story", "get", id)
		if err != nil {
			t.Fatalf("get %s: %v\n%s", id, err, out)
		}
		var row workingJSON
		if err := json.Unmarshal([]byte(out), &row); err != nil {
			t.Fatalf("get %s is not JSON: %v\n%s", id, err, out)
		}
		return row, out
	}

	row, out := get(theirs)
	w := row.WorkingOn
	if w == nil || w.Location != otherLoc || w.Label != "desk" || w.Session != "sess_other" || w.Worktree != "/work/desk" ||
		w.State != "in_progress" || !w.Advisory {
		t.Fatalf("working_on = %+v, want desk / sess_other / in_progress, marked advisory\n%s", w, out)
	}
	if want := "working: desk (sess_other), 1h30m0s ago (advisory)"; w.Text != want {
		t.Errorf("text = %q, want %q", w.Text, want)
	}
	if !strings.Contains(out, `"title": "worked on elsewhere"`) || !strings.Contains(out, `"status"`) {
		t.Errorf("the story's own fields must survive the mark:\n%s", out)
	}
	if strings.Contains(out, "takeover") || strings.Contains(out, `"held"`) {
		t.Errorf("a story another location works on is information, not a hold:\n%s", out)
	}

	for _, id := range []string{older, mine, none, done} {
		if row, out := get(id); row.WorkingOn != nil {
			t.Errorf("%s names no other worker and must carry no working_on mark:\n%s", id, out)
		}
	}

	out, err = runRoot(t, "story", "list", "--limit", "50")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	var rows []workingJSON
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("list is not JSON: %v\n%s", err, out)
	}
	byID := map[string]workingJSON{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if r := byID[theirs]; r.WorkingOn == nil || r.WorkingOn.Label != "desk" {
		t.Errorf("list row for %s: working_on = %+v", theirs, r.WorkingOn)
	}
	for _, id := range []string{older, mine, none, done} {
		if r, ok := byID[id]; !ok || r.WorkingOn != nil {
			t.Errorf("list row for %s: present=%v working_on=%+v, want present and unmarked", id, ok, r.WorkingOn)
		}
	}
}

// With nothing to mark the response is returned as it came: no re-encoding, so
// key order and spacing are the verb's own.
func TestMarkWorkingStoriesLeavesAnUnmarkedResponseAlone(t *testing.T) {
	raw := json.RawMessage(`{"id":"sty_a","title":"T"}`)
	if got := markWorkingStories(raw, nil); string(got) != string(raw) {
		t.Errorf("got %s, want it untouched", got)
	}
	got := markWorkingStories(raw, map[string]workingMark{"sty_a": {Location: "loc_x", Text: "working: x"}})
	if !strings.HasPrefix(string(got), `{"id":"sty_a","title":"T","working_on":{`) {
		t.Errorf("working_on must be spliced after the row's own members: %s", got)
	}
}

// A page's ledger rows go in with an event after the one it builds on (sty_40853d02):
// a tie on created_at, or a clock that ran behind within one command, must not put
// an event ahead of its base, or a machine that pulls a story could not continue it.
func TestOrderLedgerRowsPutsABaseBeforeItsEvent(t *testing.T) {
	at := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	row := func(id, base string, offset time.Duration) hosted.WorkstateLedgerRow {
		rec, err := json.Marshal(map[string]any{"id": id, "story_id": "sty_x", "kind": "note", "base_id": base, "created_at": at.Add(offset)})
		if err != nil {
			t.Fatal(err)
		}
		return hosted.WorkstateLedgerRow{ID: id, StoryID: "sty_x", Kind: "note", Record: rec}
	}
	// The chain is root → a → b → c. Their created_at ties (a, b) and runs backwards
	// (root is stamped last); the page lists them by id.
	page := []hosted.WorkstateLedgerRow{
		row("evt_a", "evt_root", 0),
		row("evt_b", "evt_a", 0),
		row("evt_c", "evt_b", time.Second),
		row("evt_root", "", 2*time.Second),
	}
	var got []string
	for _, r := range orderLedgerRows(page) {
		got = append(got, r.ID)
	}
	if want := []string{"evt_root", "evt_a", "evt_b", "evt_c"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	// Independent events keep the order they were made in.
	indep := []hosted.WorkstateLedgerRow{row("evt_late", "", time.Minute), row("evt_early", "", 0)}
	got = nil
	for _, r := range orderLedgerRows(indep) {
		got = append(got, r.ID)
	}
	if want := []string{"evt_early", "evt_late"}; !slices.Equal(got, want) {
		t.Fatalf("independent order = %v, want %v", got, want)
	}

	// A cycle in the data cannot recurse forever, and no row is lost.
	cyc := []hosted.WorkstateLedgerRow{row("evt_p", "evt_q", 0), row("evt_q", "evt_p", 0)}
	if got := orderLedgerRows(cyc); len(got) != 2 {
		t.Fatalf("a cycle lost rows: %v", got)
	}
}

// The working_on kind has one writer (recordWorkingOn in package verb) and its
// readers are display — including the presence a refused push names — and cache
// retention only (sty_40853d02, sty_0dc600e1, sty_9c782f0e): no other code
// references the kind, so nothing can read it to refuse an action.
func TestWorkingOnKindIsReferencedOnlyWhereItIsWrittenAndShown(t *testing.T) {
	allowed := map[string]bool{
		"ledger/ledger.go":     true, // the constant
		"verb/working_on.go":   true, // the one writer
		"cli/story_working.go": true, // `story get|list`, the advisory and a refused push read it, to show it
		"verb/storyhistory.go": true, // hosted-mode cache retention reads it to choose what stays cached
	}
	var found []string
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, "../"))
		if strings.Contains(string(b), "KindWorkingOn") && !allowed[rel] {
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("KindWorkingOn is referenced outside its writer and its display: %v", found)
	}
}
