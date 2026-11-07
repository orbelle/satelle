package cli

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func TestQueueCommands(t *testing.T) {
	isolate(t)
	must(t, "init")
	task := createID(t, "task", "create", "--title", "release")
	s1 := createID(t, "story", "create", "--title", "one")
	s2 := createID(t, "story", "create", "--title", "two")
	s3 := createID(t, "story", "create", "--title", "three")
	must(t, "story", "set", s1, "--status", "done")
	que := createID(t, "queue", "create", "--title", "release queue", "--input-kind", "story", "--input-status", "done",
		"--trigger-task", task, "--min-size", "0")
	if !regexp.MustCompile(`^que_[0-9a-f]{8}$`).MatchString(que) {
		t.Fatalf("queue id %q", que)
	}
	if out := must(t, "queue", "add", que, s2, s3); !strings.Contains(out, "queued "+s2) || !strings.Contains(out, "queued "+s3) {
		t.Fatalf("add:\n%s", out)
	}
	if out := must(t, "queue", "remove", que, s3); !strings.Contains(out, "removed "+s3) {
		t.Fatalf("remove:\n%s", out)
	}
	if _, err := run(t, "", "queue", "remove", que, s1); err == nil || !strings.Contains(err.Error(), "filter") {
		t.Fatalf("removing a filter member: %v", err)
	}
	var list []item.Item
	if err := json.Unmarshal([]byte(must(t, "queue", "list", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Inputs == nil || list[0].Inputs.Status != "done" || list[0].Trigger == nil || list[0].Trigger.Task != task {
		t.Fatalf("list %+v", list)
	}
	show := must(t, "queue", "show", que)
	for _, want := range []string{`filter     kind="story" status="done"`, "trigger    task " + task + ", fires by hand only", "members (2)", s1, s2, "last fire  never"} {
		if !strings.Contains(show, want) {
			t.Fatalf("show lacks %q:\n%s", want, show)
		}
	}
	if strings.Contains(show, s3) {
		t.Fatalf("show lists a removed item:\n%s", show)
	}

	out := must(t, "queue", "fire", que)
	m := regexp.MustCompile(`fired (exe_[0-9a-f]{8}) \(backlog\) over 2 items`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("fire:\n%s", out)
	}
	_, err := run(t, "", "queue", "fire", que)
	if ExitCodeOf(err) != ExitBlocked || !strings.Contains(err.Error(), m[1]) {
		t.Fatalf("second fire: %v", err)
	}
	if ledger := must(t, "ledger", "list", que); !strings.Contains(ledger, item.KindQueueAdded) || !strings.Contains(ledger, item.KindQueueRemoved) || !strings.Contains(ledger, item.KindQueueFired) {
		t.Fatalf("ledger:\n%s", ledger)
	}
	// The task's inputs clear; a trigger is refused on a story.
	if out := must(t, "task", "set", task, "--input-kind", "story"); !strings.Contains(out, "updated") {
		t.Fatal(out)
	}
	must(t, "task", "set", task, "--clear-inputs")
	var tk item.Item
	_ = json.Unmarshal([]byte(must(t, "task", "get", task, "--json")), &tk)
	if tk.Inputs != nil {
		t.Fatalf("inputs not cleared: %+v", tk.Inputs)
	}
	if _, err := run(t, "", "story", "set", s2, "--trigger-task", task); err == nil {
		t.Fatal("a story took a trigger flag")
	}
}

func TestQueueFireIntoWorkflow(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	if err := os.WriteFile(dir+"/.satelle/workflows/triage.toml", []byte(`name = "triage"
[states.open]
start = true
[states.closed]
terminal = true
[[transition]]
from = ["open"]
to = "closed"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := createID(t, "story", "create", "--title", "one")
	que := createID(t, "queue", "create", "--title", "triage queue", "--trigger-workflow", "triage")
	must(t, "queue", "add", que, s)
	var f kernel.FireOutcome
	if err := json.Unmarshal([]byte(must(t, "queue", "fire", que, "--json")), &f); err != nil {
		t.Fatal(err)
	}
	var run item.Item
	if err := json.Unmarshal([]byte(must(t, "execution", "get", f.Run, "--json")), &run); err != nil {
		t.Fatal(err)
	}
	if run.Workflow != "triage" || run.Parent != que || run.Status != "open" {
		t.Fatalf("run %+v", run)
	}
}

// A queue file is addressed by its name, shown with its file, refuses
// definition edits through queue set, and satelle validate names a broken one.
func TestQueueFileCommands(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	task := createID(t, "task", "create", "--title", "release")
	other := createID(t, "task", "create", "--title", "other")
	if err := os.MkdirAll(dir+"/.satelle/queues", 0o755); err != nil {
		t.Fatal(err)
	}
	file := dir + "/.satelle/queues/release.toml"
	if err := os.WriteFile(file, []byte("title = \"Release\"\n[filter]\nkind = \"story\"\n[trigger]\ntask = \""+task+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := createID(t, "story", "create", "--title", "s")
	show := must(t, "queue", "show", "release")
	for _, want := range []string{"que_release  Release", "defined by .satelle/queues/release.toml", "members (1)", s} {
		if !strings.Contains(show, want) {
			t.Fatalf("show lacks %q:\n%s", want, show)
		}
	}
	var shown struct {
		ID        string `json:"id"`
		DefinedBy string `json:"defined_by"`
	}
	if err := json.Unmarshal([]byte(must(t, "queue", "show", "release", "--json")), &shown); err != nil || shown.ID != "que_release" || shown.DefinedBy != ".satelle/queues/release.toml" {
		t.Fatalf("show --json %+v %v", shown, err)
	}
	for _, args := range [][]string{{"--trigger-task", other}, {"--input-status", "x"}, {"--title", "x"}, {"--clear-inputs"}} {
		if _, err := run(t, "", append([]string{"queue", "set", "release"}, args...)...); err == nil || !strings.Contains(err.Error(), ".satelle/queues/release.toml") {
			t.Fatalf("queue set %v: %v", args, err)
		}
	}
	if after := must(t, "queue", "show", "release"); !strings.Contains(after, "task "+task) || !strings.Contains(after, "Release") {
		t.Fatalf("show after refused sets:\n%s", after)
	}
	out := must(t, "queue", "fire", "release")
	if !strings.Contains(out, "release (que_release) fired exe_") {
		t.Fatalf("fire:\n%s", out)
	}

	if err := os.WriteFile(file, []byte("title = \"Release\"\n[trigger]\ntask = \""+task+"\"\nworkflow = \"default\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "", "validate"); err == nil || !strings.Contains(err.Error(), "queues/release.toml") {
		t.Fatalf("validate a broken queue file: %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	wf, err := os.ReadFile(dir + "/.satelle/workflows/default.toml")
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(wf), "[states.done]\n", "[states.done]\nenqueue = [\"nope\"]\n", 1)
	if broken == string(wf) {
		t.Fatalf("default workflow has no [states.done]:\n%s", wf)
	}
	if err := os.WriteFile(dir+"/.satelle/workflows/default.toml", []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "", "validate"); err == nil || !strings.Contains(err.Error(), `enqueues into "nope", which has no .satelle/queues/nope.toml`) {
		t.Fatalf("validate an enqueue naming no file: %v", err)
	}
}

// queue show names an explicit member whose item left the state that enqueued
// it as lapsed, 'left: <status>', and not as a member.
func TestQueueShowLapsed(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	if err := os.WriteFile(dir+"/.satelle/workflows/default.toml", []byte(`name = "default"
[states.backlog]
start = true
[states.in_progress]
[states.approved]
enqueue = ["release"]
[states.done]
terminal = true
[[transition]]
from = ["backlog", "approved"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "approved"
[[transition]]
from = ["approved"]
to = "done"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir+"/.satelle/queues", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/.satelle/queues/release.toml", []byte("title = \"release\"\n[trigger]\nworkflow = \"default\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := createID(t, "story", "create", "--title", "sent back")
	must(t, "story", "set", s, "--status", "in_progress")
	must(t, "story", "set", s, "--status", "approved")
	if show := must(t, "queue", "show", "release"); !strings.Contains(show, "members (1)") || strings.Contains(show, "lapsed") {
		t.Fatalf("approved:\n%s", show)
	}
	must(t, "story", "set", s, "--status", "in_progress")
	show := must(t, "queue", "show", "release")
	if !regexp.MustCompile(`members \(0\)\nlapsed \(1\)\n  ` + s + ` +left: in_progress +sent back`).MatchString(show) {
		t.Fatalf("show after the send-back:\n%s", show)
	}
	var shown struct {
		Members []struct{ ID string } `json:"members"`
		Lapsed  []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"lapsed"`
	}
	if err := json.Unmarshal([]byte(must(t, "queue", "show", "release", "--json")), &shown); err != nil {
		t.Fatal(err)
	}
	if len(shown.Members) != 0 || len(shown.Lapsed) != 1 || shown.Lapsed[0].ID != s || shown.Lapsed[0].Status != "in_progress" {
		t.Fatalf("show --json %+v", shown)
	}
}
