//go:build clislow

package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/verb"
)

// `story get` and `story list` mark a story whose latest working_on row names
// another location (sty_40853d02) — in text and as a `working_on` object — and
// leave every other story unmarked: one last worked on here, one nobody worked
// on, and one that finished (a terminal story has no one on it).
func TestSlowStoryGetAndListMarkAStoryAnotherLocationWorks(t *testing.T) {
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
