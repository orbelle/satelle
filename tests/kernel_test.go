//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// init in an empty directory writes the two-state workflow; a story walks
// backlog → done and the ledger verifies.
func TestInitWalksAStoryToDone(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	if out := s.must("init"); !strings.Contains(out, "configuration valid") {
		t.Fatalf("init: %s", out)
	}
	wf, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "workflows", "default.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[states.backlog]", "start = true", "[states.done]", "terminal = true", `to = "done"`} {
		if !strings.Contains(string(wf), want) {
			t.Errorf("default workflow lacks %q:\n%s", want, wf)
		}
	}
	s.must("validate")

	it := s.create("story", "--title", "first", "--criteria", "it works")
	if it.Status != "backlog" || it.Workflow != "default" || !strings.HasPrefix(it.ID, "sto_") {
		t.Fatalf("created %+v", it)
	}
	if out := s.must("story", "set", it.ID, "--status", "done"); !strings.Contains(out, "backlog -> done") {
		t.Errorf("set: %s", out)
	}
	if got := s.get(it.ID); got.Status != "done" {
		t.Fatalf("status %q, want done", got.Status)
	}
	if out := s.must("ledger", "verify"); !strings.Contains(out, "1 chains intact") {
		t.Errorf("verify: %s", out)
	}
	kinds := []string{}
	for _, e := range s.ledger(it.ID, "") {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, " ") != "item.created item.transitioned" {
		t.Errorf("ledger kinds = %v", kinds)
	}
	if r := s.run("story", "set", it.ID, "--status", "backlog"); r.code == 0 {
		t.Error("a move out of a terminal state with no transition was accepted")
	}
}

// The reviewer≠performer rule refuses a judge on the performer's model.
func TestReviewerMustDifferFromPerformer(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	s.write(".satelle/agents.toml", `
[agents.maker]
command = "claude"
model = "same-model"
tools = ["Read", "Edit"]
[agents.checker]
command = "claude"
model = "same-model"
tools = ["Read"]
`)
	s.write(".satelle/skills/work.md", "Work.\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.backlog]
start = true
[states.doing]
performer = { agent = "maker", skill = "work" }
[states.done]
terminal = true
[gates.look]
skill = "work"
agent = "checker"
[[transition]]
from = ["backlog"]
to = "doing"
[[transition]]
from = ["doing"]
to = "done"
groups = [{ gates = ["look"] }]
`)
	s.must("validate") // the rule is off by default
	toml, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/satelle.toml", strings.Replace(string(toml), "reviewer_differs_from_performer = false", "reviewer_differs_from_performer = true", 1))
	r := s.run("validate")
	if r.code == 0 || !strings.Contains(r.err, `judge "checker" runs model "same-model"`) {
		t.Fatalf("validate with the rule on: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

// validate refuses a broken configuration and a read-only judge on an adapter
// that cannot enforce grants; a broken configuration refuses every command.
func TestValidateRefusesBrokenConfiguration(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	id := s.create("story", "--title", "before").ID

	s.write(".satelle/agents.toml", "[agents.watcher]\ncommand = \"session\"\ntools = [\"Read\"]\n")
	s.write(".satelle/skills/look.md", "Look.\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.backlog]
start = true
[states.done]
terminal = true
[gates.look]
skill = "look"
agent = "watcher"
[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["look"] }]
`)
	r := s.run("validate")
	if r.code == 0 || !strings.Contains(r.err, `agent "watcher" may not judge`) || !strings.Contains(r.err, "does not enforce tool grants") {
		t.Fatalf("validate with a judge on the session adapter: exit %d\n%s%s", r.code, r.out, r.err)
	}

	s.write(".satelle/workflows/default.toml", "name = \"default\"\n[states.backlog]\nstart = true\n")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[mystery]\nx = 1\n")
	r = s.run("validate")
	if r.code != 1 || !strings.Contains(r.err, "mystery") {
		t.Fatalf("validate with an unknown key: exit %d\n%s%s", r.code, r.out, r.err)
	}
	for _, args := range [][]string{{"story", "list"}, {"story", "get", id}, {"story", "create", "--title", "x"}, {"status"}} {
		if r := s.run(args...); r.code == 0 {
			t.Errorf("satelle %s ran under a broken configuration:\n%s", strings.Join(args, " "), r.out)
		}
	}
}
