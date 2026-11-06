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
