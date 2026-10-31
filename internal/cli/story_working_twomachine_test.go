package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
)

// workingOnRows is the working_on rows `ledger list` shows for id on the current
// machine, oldest first.
func workingOnRows(t *testing.T, id string) []verb.WorkingOnPayload {
	t.Helper()
	out, err := runRoot(t, "ledger", "list", "--story", id, "--kind", ledger.KindWorkingOn)
	if err != nil {
		t.Fatalf("ledger list: %v\n%s", err, out)
	}
	var entries []ledger.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("ledger list is not JSON: %v\n%s", err, out)
	}
	var rows []verb.WorkingOnPayload
	for _, e := range entries {
		var p verb.WorkingOnPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("working_on payload: %v\n%s", err, e.Payload)
		}
		rows = append(rows, p)
	}
	return rows
}

func locations(rows []verb.WorkingOnPayload) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Location)
	}
	return out
}

// Two machines work the same story (sty_40853d02 AC3, AC4). Neither is refused,
// nothing is taken over, and each engagement is one working_on row naming the
// location, session and worktree — one that travels with the story's events, so
// each machine sees both after a pull. The fixture's server fails the test on any
// hold route, so a code path that still checked a story out would be caught.
func TestTwoMachinesEngageTheSameStoryAndEachSeesBothWorkingOnRows(t *testing.T) {
	ts, f := newFakeWorkstateServerWith(t, noHoldRoutes(t))
	a := workingMachine(t, ts, f, "sess_machine_a")
	b := workingMachine(t, ts, f, "sess_machine_b")
	if a.loc == b.loc {
		t.Fatalf("the two machines share a location: %s", a.loc)
	}

	// A creates the story, engages it and pushes.
	a.use(t)
	id := createStoryID(t, "worked from two places")
	if out, err := engageStory(t, id); err != nil {
		t.Fatalf("A engage: %v\n%s", err, out)
	}
	rows := workingOnRows(t, id)
	if len(rows) != 1 || rows[0].Location != a.loc || rows[0].Session != "sess_machine_a" || rows[0].State != "in_progress" ||
		filepath.Base(rows[0].Worktree) != filepath.Base(a.repo) {
		t.Fatalf("A's rows = %+v, want one working_on naming location %s, session sess_machine_a, worktree %s and in_progress", rows, a.loc, a.repo)
	}
	a.push(t)

	// B pulls: A's row arrives with the story, and `story get` shows who is on it.
	b.use(t)
	b.pull(t)
	if got := locations(workingOnRows(t, id)); len(got) != 1 || got[0] != a.loc {
		t.Fatalf("B's rows after the pull = %v, want A's alone (%s)", got, a.loc)
	}
	out, err := runRoot(t, "story", "get", id)
	if err != nil {
		t.Fatalf("B get: %v\n%s", err, out)
	}
	var seen workingJSON
	if err := json.Unmarshal([]byte(out), &seen); err != nil || seen.WorkingOn == nil || seen.WorkingOn.Location != a.loc {
		t.Fatalf("B's story get must show A's working_on (%s): %v\n%s", a.loc, err, out)
	}

	// B engages the story A last worked: no refusal, no takeover, no hold call.
	if out, err := engageStory(t, id); err != nil {
		t.Fatalf("B must be able to engage a story A last worked on: %v\n%s", err, out)
	}
	if got := locations(workingOnRows(t, id)); len(got) != 2 || got[0] != a.loc || got[1] != b.loc {
		t.Fatalf("B's rows after engaging = %v, want A's then B's (%s, %s)", got, a.loc, b.loc)
	}
	b.push(t)

	// A pulls and sees both.
	a.use(t)
	a.pull(t)
	if got := locations(workingOnRows(t, id)); len(got) != 2 || got[0] != a.loc || got[1] != b.loc {
		t.Fatalf("A's rows after the pull = %v, want A's then B's (%s, %s)", got, a.loc, b.loc)
	}
	out, err = runRoot(t, "story", "get", id)
	if err != nil {
		t.Fatalf("A get: %v\n%s", err, out)
	}
	seen = workingJSON{}
	if err := json.Unmarshal([]byte(out), &seen); err != nil || seen.WorkingOn == nil || seen.WorkingOn.Location != b.loc {
		t.Fatalf("A's story get must show B's working_on (%s): %v\n%s", b.loc, err, out)
	}
}

// When two machines both append to the same story, the second push takes the
// other machine's events and re-appends its own after them (sty_0dc600e1 AC4) —
// not a hold refusal (sty_40853d02 AC6). Engaging is not what is refused: the
// second machine engaged without a word, and nothing is refused at append time.
func TestSecondMachinePushRebasesOntoTheOtherMachinesEvents(t *testing.T) {
	ts, f := newFakeWorkstateServerWith(t, noHoldRoutes(t))
	a := workingMachine(t, ts, f, "sess_machine_a")
	b := workingMachine(t, ts, f, "sess_machine_b")

	a.use(t)
	id := createStoryID(t, "both append")
	if out, err := engageStory(t, id); err != nil {
		t.Fatalf("A engage: %v\n%s", err, out)
	}
	a.push(t)

	b.use(t)
	b.pull(t)

	// A moves the story on and pushes; B, which has not pulled, engages and pushes.
	a.use(t)
	if out, err := runRoot(t, "ledger", "append", "--story", id, "--kind", "comment", "--actor", "executor", "--body", "A moves on"); err != nil {
		t.Fatalf("A append: %v\n%s", err, out)
	}
	a.push(t)
	aHead := storyHead(t, a, id)
	if aHead == "" {
		t.Fatal("A's push saved no head for the story")
	}

	b.use(t)
	if out, err := engageStory(t, id); err != nil {
		t.Fatalf("B engage must not be refused: %v\n%s", err, out)
	}
	out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("B's push should rebase onto A's events: %v\n%s", err, out)
	}
	for _, want := range []string{"the story " + id + " moved on the server", "re-appended after " + aHead} {
		if !strings.Contains(out, want) {
			t.Errorf("B's push output lacks %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{"held", "takeover", "hold", "re-read and decide"} {
		if strings.Contains(out, banned) {
			t.Errorf("B's push must rebase, not report a hold or a conflict (%q):\n%s", banned, out)
		}
	}
	stored := f.chain.stored("probe", id)
	if i := slices.Index(stored, aHead); i < 0 || i == len(stored)-1 {
		t.Errorf("the server's chain %v must hold A's head %s with B's events after it", stored, aHead)
	}
	if got := storyHead(t, b, id); got != stored[len(stored)-1] {
		t.Errorf("B saved head %s, want the server's latest %s", got, stored[len(stored)-1])
	}
}

// workingMachine is a machine of the two-machine tests: a git working tree under
// its own session, so a working_on row can name all three of location, session and
// worktree.
func workingMachine(t *testing.T, ts *httptest.Server, f *fakeWorkstateServer, session string) *holdFixture {
	t.Helper()
	fx := newHoldMachine(t, ts, f, holdNoGit)
	gitInitCommit(t, fx.repo)
	fx.session = session
	return fx
}

// storyHead is the last event of id this machine saw the server accept.
func storyHead(t *testing.T, fx *holdFixture, id string) string {
	t.Helper()
	c, err := hosted.LoadWorkstateCursor(fx.ts.URL, "probe", fx.repo)
	if err != nil {
		t.Fatal(err)
	}
	return c.StoryHeads[id]
}

// sty_9c782f0e AC1: two locations drive the same story. Exclusivity is the
// chain's compare-and-append on its base, not a lock: neither engage is refused,
// but the second location's next append builds on a base the first location's
// move has passed, the server's base check refuses it, the push does not splice
// it after that move, and the refusal names the other location's latest
// working_on presence.
func TestTwoCheckoutsSecondAppendRefusedNamesPresence(t *testing.T) {
	ts, f := newFakeWorkstateServerWith(t, noHoldRoutes(t))
	a := workingMachine(t, ts, f, "sess_machine_a")
	b := workingMachine(t, ts, f, "sess_machine_b")

	a.use(t)
	id := createStoryID(t, "driven from two locations")
	a.push(t)
	b.use(t)
	b.pull(t)

	// A drives the story first and publishes.
	a.use(t)
	if out, err := engageStory(t, id); err != nil {
		t.Fatalf("A engage: %v\n%s", err, out)
	}
	a.push(t)
	aHead := storyHead(t, a, id)

	// B drives it too, from the base it pulled: engaging is not refused.
	b.use(t)
	if out, err := engageStory(t, id); err != nil {
		t.Fatalf("B engage must not be refused: %v\n%s", err, out)
	}
	out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err == nil {
		t.Fatalf("B's append on a stale base must be refused:\n%s", out)
	}
	for _, want := range []string{"the story " + id, "moved since", "another location moved its status",
		"working on at " + a.loc, "session sess_machine_a", "[in_progress]"} {
		if !strings.Contains(out, want) {
			t.Errorf("B's refusal lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "re-appended after") {
		t.Errorf("B's events must not be spliced after A's move:\n%s", out)
	}
	if stored := f.chain.stored("probe", id); len(stored) == 0 || stored[len(stored)-1] != aHead {
		t.Errorf("the server's chain %v must still end at A's head %s", stored, aHead)
	}
}

// sty_9c782f0e AC3 (card: "A task can then append a result to a story's chain
// from any session"): a task in a session that never engaged the story appends
// a result to its chain with no seat check — no seat is held for it here — and
// the only refusal is a stale base: once another location has moved the story,
// the next result is refused by the base check, naming that presence.
func TestTaskAppendsAResultFromASessionThatNeverEngaged(t *testing.T) {
	ts, f := newFakeWorkstateServerWith(t, noHoldRoutes(t))
	a := workingMachine(t, ts, f, "sess_driver")
	task := workingMachine(t, ts, f, "sess_task")

	a.use(t)
	id := createStoryID(t, "a task reports on it")
	a.push(t)

	task.use(t)
	task.pull(t)
	appendResult := func(body string) {
		t.Helper()
		if out, err := runRoot(t, "ledger", "append", "--story", id, "--kind", ledger.KindExecutionLink, "--actor", "task", "--body", body); err != nil {
			t.Fatalf("task append: %v\n%s", err, out)
		}
	}
	appendResult("result: pass")
	ap, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	_, lerr := ap.Store.Leases.Get(context.Background(), id)
	ap.Close()
	if !errors.Is(lerr, lease.ErrNotFound) {
		t.Fatalf("the task's session holds a seat for %s (err %v); it must append with none", id, lerr)
	}
	task.push(t)
	if stored := f.chain.stored("probe", id); len(stored) == 0 || stored[len(stored)-1] != storyHead(t, task, id) {
		t.Fatalf("the task's result was not appended: server chain %v, task head %s", stored, storyHead(t, task, id))
	}

	// The driver moves the story on; the task's next result is on a stale base.
	a.use(t)
	a.pull(t)
	if out, err := engageStory(t, id); err != nil {
		t.Fatalf("driver engage: %v\n%s", err, out)
	}
	a.push(t)
	task.use(t)
	appendResult("result: second")
	out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err == nil {
		t.Fatalf("the task's result on a stale base must be refused:\n%s", out)
	}
	for _, want := range []string{"moved since", "working on at " + a.loc, "session sess_driver"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{"engagement seat", "seat held"} {
		if strings.Contains(out, banned) {
			t.Errorf("a task's append is refused by no seat (%q):\n%s", banned, out)
		}
	}
}
