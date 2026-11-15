//go:build integration && !windows

package tests

import (
	"encoding/json"
	"strings"
	"testing"
)

const stackWorkflow = `name = "stack"
[states.todo]
start = true
[states.work]
[states.review]
[states.done]
terminal = true

[gates.go-check]
skill = "ok"
paths = ["**/*.go"]
[gates.all-check]
skill = "ok"

[[transition]]
from = ["todo"]
to = "work"
[[transition]]
from = ["work"]
to = "review"
groups = [{ mode = "sequential", gates = ["go-check", "all-check"] }]
[[transition]]
from = ["review"]
to = "work"
[[transition]]
from = ["review"]
to = "done"
`

func stackSandbox(t *testing.T) *sandbox {
	t.Helper()
	s := newSandbox(t)
	s.git("branch", "-M", "main")
	s.must("init")
	s.write(".satelle/workflows/stack.toml", stackWorkflow)
	s.write(".satelle/skills/ok.md", "---\ntype: check\n---\n```sh\ntrue\n```\n")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"stack\"\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "config")
	s.must("validate")
	return s
}

func latestRun(s *sandbox, id, gate string) gateRun {
	s.t.Helper()
	runs := s.gateRuns(id)
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Gate == gate {
			return runs[i]
		}
	}
	s.t.Fatalf("no gate.run of %s on %s", gate, id)
	return gateRun{}
}

func TestRebaseRefusesUntilSeatedOnTheStackedParent(t *testing.T) {
	t.Parallel()
	s := stackSandbox(t)
	s.git("checkout", "-q", "-b", "chore/parent")
	s.write("docs/chore.md", "chore\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "chore")
	s.git("checkout", "-q", "main")
	s.git("checkout", "-q", "-b", "feat/story")

	id := s.create("story", "--title", "footer").ID
	s.must("story", "set", id, "--status", "work")
	s.must("story", "seat", id, "--session", "s1")
	s.write("web/footer.go", "package web\n")
	s.write("web/web_test.go", "package web\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "footer")
	s.must("story", "set", id, "--status", "review")
	if got := strings.Join(latestRun(s, id, "go-check").Changed, ","); got != "web/footer.go,web/web_test.go" {
		t.Fatalf("the approval judged %q", got)
	}

	s.must("story", "set", id, "--status", "work")
	s.must("story", "seat", id, "--session", "s1")
	oldBase := strings.TrimSpace(s.git("rev-parse", "HEAD"))
	s.git("rebase", "-q", "chore/parent")
	head := strings.TrimSpace(s.git("rev-parse", "HEAD"))
	before := len(s.gateRuns(id))
	r := s.run("story", "set", id, "--status", "review")
	if r.code != 1 || !strings.Contains(r.err, oldBase) || !strings.Contains(r.err, head) || !strings.Contains(r.err, "seat "+id+" --base") {
		t.Fatalf("the stale move: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := len(s.gateRuns(id)); n != before {
		t.Errorf("a gate ran on the stale base (%d gate.run, was %d)", n, before)
	}
	if st := s.get(id).Status; st != "work" {
		t.Errorf("the refused item moved to %s", st)
	}
	if r := s.run("story", "diff", id); !strings.Contains(r.err, "warning:") || !strings.Contains(r.err, oldBase) {
		t.Errorf("story diff did not warn about the stale base:\n%s", r.err)
	}

	parent := strings.TrimSpace(s.git("rev-parse", "chore/parent"))
	if out := s.must("story", "seat", id, "--session", "s1", "--base", "chore/parent"); !strings.Contains(out, "(base "+parent[:12]+")") {
		t.Errorf("seat --base printed %q", out)
	}
	var got struct {
		Seats []struct {
			Base string `json:"base"`
		} `json:"seats"`
	}
	s.mustJSON(&got, "story", "get", id)
	if len(got.Seats) != 1 || got.Seats[0].Base != parent {
		t.Fatalf("seats = %+v, want one on %s", got.Seats, parent)
	}
	s.must("story", "set", id, "--status", "review")
	for _, g := range []string{"go-check", "all-check"} {
		run := latestRun(s, id, g)
		if run.Result != "accept" || strings.Join(run.Changed, ",") != "web/footer.go,web/web_test.go" {
			t.Errorf("%s = %s over %v, want accept over the story's own two files", g, run.Result, run.Changed)
		}
	}
	if r := s.run("story", "diff", id); r.code != 0 || strings.TrimSpace(r.out) != "web/footer.go\nweb/web_test.go" || r.err != "" {
		t.Errorf("story diff: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := s.run("story", "seat", id, "--session", "s1", "--base", oldBase); r.code == 0 || !strings.Contains(r.err, "not in HEAD's history") {
		t.Errorf("seat --base on a commit outside HEAD: exit %d %s", r.code, r.err)
	}
	if r := s.run("story", "seat", id, "--session", "s1", "--base", "no-such-ref"); r.code == 0 || !strings.Contains(r.err, "names no commit") {
		t.Errorf("seat --base on no commit: exit %d %s", r.code, r.err)
	}
}

func TestASkipOnAShrunkDiffIsReported(t *testing.T) {
	t.Parallel()
	s := stackSandbox(t)
	id := s.create("story", "--title", "shrink").ID
	s.must("story", "set", id, "--status", "work")
	s.must("story", "seat", id, "--session", "s1")
	s.write("web/a.go", "package web\n")
	s.write("docs/a.md", "a\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "a")
	s.must("story", "set", id, "--status", "review")

	s.must("story", "set", id, "--status", "work")
	s.must("story", "seat", id, "--session", "s1")
	s.write("docs/b.md", "b\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "b")
	out := s.must("story", "set", id, "--status", "review")
	if !strings.Contains(out, "go-check") || !strings.Contains(out, "skipped") || !strings.Contains(out, "warning:") ||
		!strings.Contains(out, "shrunk") || !strings.Contains(out, "web/a.go") {
		t.Fatalf("the shrunk skip was not reported:\n%s", out)
	}
	if run := latestRun(s, id, "go-check"); run.Result != "skipped" || !strings.Contains(run.Warning, "shrunk") {
		t.Errorf("go-check gate.run = %+v", run)
	}
}

type taskRunJSON struct {
	ID      string `json:"id"`
	Base    string `json:"base"`
	TaskRun struct {
		BaseRef string `json:"base_ref"`
	} `json:"task_run"`
}

func TestTaskRunRecordsAndPrintsItsBase(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	workConfig(t, s)
	s.git("branch", "-M", "main")
	task := s.create("task", "--title", "release", "--input-kind", "story").ID
	s.git("checkout", "-q", "-b", "feat/story")
	s.write("story.txt", "work\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "story")
	story := strings.TrimSpace(s.git("rev-parse", "HEAD"))

	if out := s.must("task", "run", task); !strings.Contains(out, "base "+story[:12]+" (feat/story)") {
		t.Errorf("task run printed %q", out)
	}
	var def taskRunJSON
	s.mustJSON(&def, "task", "run", task)
	if def.Base != story || def.TaskRun.BaseRef != "feat/story" {
		t.Errorf("default run: base %s, base_ref %q", def.Base, def.TaskRun.BaseRef)
	}
	evs := s.ledger(def.ID, "task.run")
	var rec struct {
		BaseRef string `json:"base_ref"`
	}
	if len(evs) != 1 || json.Unmarshal(evs[0].Payload, &rec) != nil || rec.BaseRef != "feat/story" {
		t.Errorf("task.run payload: %+v", evs)
	}
	s.git("checkout", "-q", "--detach", story)
	if out := s.must("task", "run", task); !strings.Contains(out, "(detached HEAD)") {
		t.Errorf("detached task run printed %q", out)
	}
	s.git("checkout", "-q", "feat/story")

	main := strings.TrimSpace(s.git("rev-parse", "main"))
	var rel taskRunJSON
	s.mustJSON(&rel, "task", "run", task, "--base", "main")
	if rel.Base != main || rel.TaskRun.BaseRef != "main" {
		t.Fatalf("--base main: base %s, base_ref %q", rel.Base, rel.TaskRun.BaseRef)
	}
	s.git("checkout", "-q", "main")
	s.must("execution", "set", rel.ID, "--status", "doing")
	if r := s.run("execution", "set", def.ID, "--status", "doing"); r.code != 1 || !strings.Contains(r.err, "not in HEAD's history") {
		t.Errorf("the story-branch run on main: exit %d %s", r.code, r.err)
	}

	s.write("main.txt", "later\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "main moves")
	later := strings.TrimSpace(s.git("rev-parse", "main"))
	s.git("checkout", "-q", "feat/story")
	var moved taskRunJSON
	s.mustJSON(&moved, "task", "run", task, "--base", "main")
	if moved.Base != later {
		t.Fatalf("--base main after main moved: base %s, want %s", moved.Base, later)
	}
	s.git("checkout", "-q", "main")
	s.must("execution", "set", moved.ID, "--status", "doing")

	before := len(s.ledger("", "task.run"))
	if r := s.run("task", "run", task, "--base", "no-such-ref"); r.code == 0 || !strings.Contains(r.err, "names no commit") {
		t.Errorf("--base no-such-ref: exit %d %s", r.code, r.err)
	}
	if n := len(s.ledger("", "task.run")); n != before {
		t.Errorf("a refused run recorded a task.run (%d, was %d)", n, before)
	}
}
