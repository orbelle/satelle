package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// stopFixture is a repo whose default workflow holds in_progress, review
// (waits), approved (enqueues into release), blocked (park) and done, and
// whose release queue fires a run in the default workflow, with min size 0
// and no filter.
func stopFixture(t *testing.T) string {
	t.Helper()
	dir := isolate(t)
	must(t, "init")
	if err := os.WriteFile(dir+"/.satelle/workflows/default.toml", []byte(`name = "default"
[states.backlog]
start = true
[states.in_progress]
[states.review]
waits = "the reviewer's approval"
[states.approved]
enqueue = ["release"]
[states.blocked]
park = true
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "review"
[[transition]]
from = ["in_progress", "review"]
to = "approved"
[[transition]]
from = ["in_progress"]
to = "blocked"
[[transition]]
from = ["approved", "backlog"]
to = "done"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReleaseQueue(t, dir, "")
	return dir
}

func writeReleaseQueue(t *testing.T, dir, extra string) {
	t.Helper()
	if err := os.MkdirAll(dir+"/.satelle/queues", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/.satelle/queues/release.toml", []byte("title = \"release\"\n[trigger]\nworkflow = \"default\"\nmin_size = 0\n"+extra), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stopAt creates a story, moves it through the states given and seats the
// session on it (before the last move, so a terminal state keeps the seat).
func stopAt(t *testing.T, session string, states ...string) string {
	t.Helper()
	id := createID(t, "story", "create", "--title", "drive me")
	if len(states) == 0 {
		must(t, "story", "seat", id, "--session", session)
		return id
	}
	for i, s := range states {
		if i == len(states)-1 {
			must(t, "story", "seat", id, "--session", session)
		}
		must(t, "story", "set", id, "--status", s)
	}
	return id
}

// stopcheck runs the stop hook for session under harness with the claude
// payload shape and returns its trimmed output.
func stopcheck(t *testing.T, dir, session, harness string) string {
	t.Helper()
	return stopWith(t, fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"cwd":%q}`, session, dir), harness)
}

func stopWith(t *testing.T, payload, harness string) string {
	t.Helper()
	out, err := run(t, payload, "hook", "stopcheck", "--harness", harness)
	if err != nil {
		t.Fatalf("hook stopcheck: %v\n%s", err, out)
	}
	return strings.TrimSpace(out)
}

// blockReason decodes a {decision: block, reason} answer, failing on any
// other.
func blockReason(t *testing.T, out string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not a JSON answer %q: %v", out, err)
	}
	reason, _ := doc["reason"].(string)
	if len(doc) != 2 || doc["decision"] != "block" || reason == "" {
		t.Fatalf("not a block answer: %s", out)
	}
	return reason
}

func clearStop(t *testing.T, session string) {
	t.Helper()
	if err := os.Remove(stopMarker(session)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func stopMarker(session string) string {
	return filepath.Join(config.GlobalDir(), seatlife.Dir, session+".stop")
}

func hasAll(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, text)
		}
	}
}

func hasNone(t *testing.T, what, text string, nots ...string) {
	t.Helper()
	for _, w := range nots {
		if strings.Contains(text, w) {
			t.Errorf("%s has %q:\n%s", what, w, text)
		}
	}
}

// 1, 2: a seated approved item whose queue can fire blocks once, naming the
// fire; the stop straight after passes; a third blocks again.
func TestStopcheckBlocksOnceNamingTheFire(t *testing.T) {
	dir := stopFixture(t)
	id := stopAt(t, "s1", "in_progress", "approved")
	reason := blockReason(t, stopcheck(t, dir, "s1", "claude"))
	hasAll(t, "reason", reason, id, "approved", "satelle queue fire release --session s1", "not an external wait",
		"terminal state of its workflow", "park state", "waits on an external party", "A stop straight after this one is let through")
	if out := stopcheck(t, dir, "s1", "claude"); out != "" {
		t.Errorf("the second stop in a row printed %q", out)
	}
	blockReason(t, stopcheck(t, dir, "s1", "claude"))
}

// 3, 4, 5, 6: a park state and a waits state let the stop through; an open
// state names its transition; a terminal item and a seatless session pass.
func TestStopcheckReadsTheWorkflow(t *testing.T) {
	dir := stopFixture(t)
	stopAt(t, "s-park", "in_progress", "blocked")
	if out := stopcheck(t, dir, "s-park", "claude"); out != "" {
		t.Errorf("a park state blocked: %s", out)
	}
	stopAt(t, "s-wait", "in_progress", "review")
	if out := stopcheck(t, dir, "s-wait", "claude"); out != "" {
		t.Errorf("a waits state blocked: %s", out)
	}
	id := stopAt(t, "s-open", "in_progress")
	hasAll(t, "reason", blockReason(t, stopcheck(t, dir, "s-open", "claude")), id+" in_progress: next is satelle story set "+id+" --status review --session s-open")
	stopAt(t, "s-done", "done")
	if out := stopcheck(t, dir, "s-done", "claude"); out != "" {
		t.Errorf("a terminal item blocked: %s", out)
	}
	if out := stopcheck(t, dir, "s-none", "claude"); out != "" {
		t.Errorf("a session with no seat blocked: %s", out)
	}
	if route := must(t, "story", "route", id); !strings.Contains(route, "waits the reviewer's approval") {
		t.Errorf("route does not show the wait:\n%s", route)
	}
}

// 7: a dispatched run (its session comes from SATELLE_SESSION) is never
// blocked.
func TestStopcheckLetsADispatchedRunThrough(t *testing.T) {
	dir := stopFixture(t)
	stopAt(t, "s1", "in_progress", "approved")
	t.Setenv(config.SessionEnv, "perform_0001")
	if out := stopcheck(t, dir, "s1", "claude"); out != "" {
		t.Errorf("a dispatched run was blocked: %s", out)
	}
}

// 8: the queue's run took the item and is open: the move is on the run.
func TestStopcheckNamesTheOpenRunThatCarriesTheItem(t *testing.T) {
	dir := stopFixture(t)
	id := stopAt(t, "s1", "in_progress", "approved")
	run := fireRun(t)
	reason := blockReason(t, stopcheck(t, dir, "s1", "claude"))
	hasAll(t, "unseated run", reason, id, "run "+run+" (backlog) carries it", "no session holds it", "satelle execution seat "+run+" --session s1")
	hasNone(t, "unseated run", reason, "queue fire")

	clearStop(t, "s1")
	must(t, "execution", "seat", run, "--session", "s1")
	reason = blockReason(t, stopcheck(t, dir, "s1", "claude"))
	hasAll(t, "seated run", reason, "carries it and this session holds it", "satelle execution set "+run+" --status in_progress --session s1")

	clearStop(t, "s1")
	must(t, "execution", "release", run, "--session", "s1")
	must(t, "execution", "seat", run, "--session", "s2")
	if out := stopcheck(t, dir, "s1", "claude"); out != "" {
		t.Errorf("another session's run blocked: %s", out)
	}
}

// 9: an item approved after the fire is a member the open run does not list.
func TestStopcheckNamesTheRunThenTheFire(t *testing.T) {
	dir := stopFixture(t)
	stopAt(t, "s-first", "in_progress", "approved")
	run := fireRun(t)
	id := stopAt(t, "s1", "in_progress", "approved")
	reason := blockReason(t, stopcheck(t, dir, "s1", "claude"))
	hasAll(t, "unseated run", reason, id, "does not list it", "no session holds that run",
		"satelle execution seat "+run+" --session s1", "then satelle queue fire release --session s1 once "+run+" is done")
	hasNone(t, "unseated run", reason, "carries it")

	clearStop(t, "s1")
	must(t, "execution", "seat", run, "--session", "s1")
	reason = blockReason(t, stopcheck(t, dir, "s1", "claude"))
	hasAll(t, "seated run", reason, "this session holds that run", "satelle execution set "+run+" --status in_progress --session s1, then satelle queue fire release --session s1")
	hasNone(t, "seated run", reason, "carries it")

	clearStop(t, "s1")
	must(t, "execution", "release", run, "--session", "s1")
	must(t, "execution", "seat", run, "--session", "s2")
	if out := stopcheck(t, dir, "s1", "claude"); out != "" {
		t.Errorf("another session's run blocked: %s", out)
	}
}

// 10: a terminal queue still holding the item names nothing, and the fire
// refuses.
func TestStopcheckTerminalQueue(t *testing.T) {
	dir := stopFixture(t)
	stopAt(t, "s1", "in_progress", "approved")
	must(t, "queue", "set", "release", "--status", "done")
	if out := stopcheck(t, dir, "s1", "claude"); out != "" {
		t.Errorf("a terminal queue blocked: %s", out)
	}
	if _, err := run(t, "", "queue", "fire", "release"); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("fire of a terminal queue: %v", err)
	}
}

// 11: an item removed from its queue names its transition, whether the queue
// is open or terminal.
func TestStopcheckRemovedItemNamesItsTransition(t *testing.T) {
	dir := stopFixture(t)
	id := stopAt(t, "s1", "in_progress", "approved")
	must(t, "queue", "remove", "release", id)
	want := "satelle story set " + id + " --status done --session s1"
	hasAll(t, "open queue", blockReason(t, stopcheck(t, dir, "s1", "claude")), want)
	clearStop(t, "s1")
	must(t, "queue", "set", "release", "--status", "done")
	hasAll(t, "terminal queue", blockReason(t, stopcheck(t, dir, "s1", "claude")), want)
}

// 12: a trigger base that names no commit names nothing, and the fire refuses.
func TestStopcheckBadBase(t *testing.T) {
	dir := stopFixture(t)
	writeReleaseQueue(t, dir, "base = \"refs/heads/nope\"\n")
	stopAt(t, "s1", "in_progress", "approved")
	if out := stopcheck(t, dir, "s1", "claude"); out != "" {
		t.Errorf("an unfireable queue blocked: %s", out)
	}
	if _, err := run(t, "", "queue", "fire", "release"); err == nil || !strings.Contains(err.Error(), "names no commit") {
		t.Errorf("fire with a bad base: %v", err)
	}
}

// 13: cursor's block is a followup_message, its pass an empty object.
func TestStopcheckCursorEnvelope(t *testing.T) {
	dir := stopFixture(t)
	id := stopAt(t, "c1", "in_progress", "approved")
	payload := `{"conversation_id":"c1","session_id":"c1","cursor_version":"2026.11.05-86047df","workspace_roots":[` + jsonString(dir) + `],"hook_event_name":"stop","status":"completed","loop_count":0}`
	out := stopWith(t, payload, "cursor")
	var doc map[string]string
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc) != 1 {
		t.Fatalf("cursor block %q: %v", out, err)
	}
	hasAll(t, "followup_message", doc["followup_message"], id, "satelle queue fire release --session c1",
		"terminal state of its workflow", "park state", "waits on an external party")
	if out := stopWith(t, payload, "cursor"); out != "{}" {
		t.Errorf("cursor pass %q, want {}", out)
	}
}

// 14: grok's Stop payload, as its hook guide documents it.
func TestStopcheckGrok(t *testing.T) {
	raw := grokFixture(t, "grok_stop.json") // read before the fixture changes directory
	dir := stopFixture(t)
	id := stopAt(t, "grok-session-0001", "in_progress", "approved")
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["cwd"], doc["workspaceRoot"] = dir, dir
	payload := func(reason string) string {
		doc["reason"] = reason
		b, _ := json.Marshal(doc)
		return string(b)
	}
	// A session-end Stop names nothing and leaves the marker as it was.
	if out := stopWith(t, payload("shutdown"), "grok"); out != "" {
		t.Errorf("a shutdown Stop printed %q", out)
	}
	if _, err := os.Stat(stopMarker("grok-session-0001")); err == nil {
		t.Error("a shutdown Stop wrote the marker")
	}
	out := stopWith(t, payload("end_turn"), "grok")
	if strings.Count(out, "\n") != 0 {
		t.Errorf("grok block is not one JSON object: %q", out)
	}
	hasAll(t, "grok reason", blockReason(t, out), id, "satelle queue fire release --session grok-session-0001",
		"terminal state of its workflow", "park state", "waits on an external party")
	if out := stopWith(t, payload("shutdown"), "grok"); out != "" {
		t.Errorf("a shutdown Stop printed %q", out)
	}
	if _, err := os.Stat(stopMarker("grok-session-0001")); err != nil {
		t.Errorf("a shutdown Stop cleared the marker: %v", err)
	}
	if out := stopWith(t, payload("end_turn"), "grok"); out != "" {
		t.Errorf("the second stop in a row printed %q", out)
	}
}

// 15: pi's extension sends claude's Stop shape and reads the same block.
func TestStopcheckPi(t *testing.T) {
	dir := stopFixture(t)
	id := stopAt(t, "pi-1", "in_progress", "approved")
	out := stopcheck(t, dir, "pi-1", "pi")
	hasAll(t, "pi reason", blockReason(t, out), id, "satelle queue fire release --session pi-1")
}

// fireRun fires the release queue and returns the run it started.
func fireRun(t *testing.T) string {
	t.Helper()
	var f struct {
		Run string `json:"run"`
	}
	if err := json.Unmarshal([]byte(must(t, "queue", "fire", "release", "--json")), &f); err != nil || f.Run == "" {
		t.Fatalf("fire: %+v %v", f, err)
	}
	return f.Run
}

// grokFixture reads one of internal/hook's grok payloads, as grok's hook
// guide documents them; call it before isolate changes directory.
func grokFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "hook", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
