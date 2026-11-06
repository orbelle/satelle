//go:build !windows

package tests

import (
	"encoding/json"
	"strings"
	"testing"
)

// workConfig sets up a workflow "work" that every execution takes by rule:
// backlog -> doing (performer maker, gate ready) -> done (gate evidence,
// which refuses until an attachment named evidence is on the item).
func workConfig(t *testing.T, s *sandbox) string {
	t.Helper()
	fake := fakeClaude(t, s)
	s.must("init")
	s.write(".satelle/agents.toml", "[agents.maker]\ncommand = \"claude\"\nmodel = \"mk-1\"\n")
	s.write(".satelle/workflows/work.toml", `name = "work"
[states.backlog]
start = true
[states.doing]
performer = { agent = "maker", skill = "make" }
[states.done]
terminal = true

[gates.ready]
skill = "ready"
[gates.evidence]
skill = "evidence"

[[transition]]
from = ["backlog"]
to = "doing"
groups = [{ mode = "sequential", gates = ["ready"] }]
[[transition]]
from = ["doing"]
to = "done"
groups = [{ mode = "sequential", gates = ["evidence"] }]
`)
	s.write(".satelle/skills/ready.md", "---\ntype: check\n---\n```sh\ntrue\n```\n")
	s.write(".satelle/skills/evidence.md", "---\ntype: check\n---\n```sh\nsatelle ledger list \"$SATELLE_ITEM\" --kind attachment.added --json | grep -q '\"evidence\"' || { echo no evidence; exit 1; }\n```\n")
	s.write(".satelle/skills/make.md", "---\nname: make\n---\nMake it.\n")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"work\"\n[[workflows.rule]]\nworkflow = \"work\"\nkind = \"execution\"\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "config")
	s.must("validate")
	return fake
}

// taskRun creates a task and runs it, returning the execution's id.
func taskRun(t *testing.T, s *sandbox) string {
	t.Helper()
	task := s.create("task", "--title", "groom", "--input-kind", "story")
	var exec struct {
		ID       string `json:"id"`
		Workflow string `json:"workflow"`
		Status   string `json:"status"`
	}
	s.mustJSON(&exec, "task", "run", task.ID)
	if exec.ID == "" || exec.Workflow != "work" || exec.Status != "backlog" {
		t.Fatalf("task run made %+v", exec)
	}
	return exec.ID
}

// Every work verb does on an execution exactly what it does on a story under
// the same workflow: the same exit codes and, ids and sessions aside, the same
// output.
func TestExecutionVerbsMatchStory(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	fake := workConfig(t, s)
	story := s.create("story", "--title", "s", "--workflow", "work").ID
	exec := taskRun(t, s)

	walk := func(kind, id string) []result {
		session := "s-" + kind
		steps := []struct {
			stdin string
			args  []string
		}{
			{"", []string{"seat", id, "--session", session}},
			{"", []string{"set", id, "--status", "doing"}},
			{"", []string{"route", id}},
			{"", []string{"perform", id}},
			{"", []string{"set", id, "--status", "done"}},
			{"the proof", []string{"attach", id, "--name", "evidence", "--file", "-"}},
			{"", []string{"attachments", id, "--json"}},
			{"", []string{"diff", id}},
			{"", []string{"set", id, "--status", "done"}},
			{"", []string{"release", id, "--session", session}},
		}
		var out []result
		for _, st := range steps {
			r := s.exec(satelleBin, st.stdin, append([]string{kind}, st.args...)...)
			r.out = strings.NewReplacer(id, "<id>", session, "<session>").Replace(r.out)
			r.err = strings.NewReplacer(id, "<id>", session, "<session>").Replace(r.err)
			if st.args[0] == "attachments" {
				var as []struct {
					Name string `json:"name"`
					Size int64  `json:"size"`
				}
				if err := json.Unmarshal([]byte(r.out), &as); err != nil {
					t.Fatalf("%s attachments --json: %v\n%s", kind, err, r.out)
				}
				b, _ := json.Marshal(as)
				r.out = string(b)
			}
			out = append(out, r)
		}
		return out
	}
	sr, er := walk("story", story), walk("execution", exec)
	for i := range sr {
		if sr[i].code != er[i].code || sr[i].out != er[i].out {
			t.Errorf("step %d differs:\nstory     exit %d\n%s%s\nexecution exit %d\n%s%s", i, sr[i].code, sr[i].out, sr[i].err, er[i].code, er[i].out, er[i].err)
		}
	}
	if sr[4].code != 2 || sr[8].code != 0 {
		t.Errorf("done without evidence exits %d, with it %d", sr[4].code, sr[8].code)
	}
	if n := len(argvs(t, fake, "mk-1")); n != 2 {
		t.Errorf("performer ran %d time(s), want one per kind", n)
	}
	// The kind is still checked.
	if r := s.run("story", "set", exec, "--status", "done"); r.code == 0 || !strings.Contains(r.err, "is a execution, not a story") {
		t.Errorf("story set on an execution: exit %d %s", r.code, r.err)
	}
}

// A task run's execution walks its workflow to a terminal state through its
// gates, carrying an attachment, all on its own ledger.
func TestTaskRunExecutionWalksItsWorkflow(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	workConfig(t, s)
	exec := taskRun(t, s)

	s.must("execution", "set", exec, "--status", "doing")
	if r := s.run("execution", "set", exec, "--status", "done"); r.code != 2 || !strings.Contains(r.err, "gate evidence") || !strings.Contains(r.err, "no evidence") {
		t.Fatalf("done without evidence: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := s.exec(satelleBin, "the proof", "execution", "attach", exec, "--name", "evidence", "--file", "-"); r.code != 0 {
		t.Fatalf("attach: exit %d %s", r.code, r.err)
	}
	s.must("execution", "set", exec, "--status", "done")

	var got struct {
		Status      string `json:"status"`
		Attachments []struct {
			Name string `json:"name"`
		} `json:"attachments"`
	}
	s.mustJSON(&got, "execution", "get", exec)
	if got.Status != "done" {
		t.Errorf("status = %q", got.Status)
	}
	var runs []string
	for _, g := range s.gateRuns(exec) {
		runs = append(runs, g.Gate+":"+g.Result)
	}
	if strings.Join(runs, ",") != "ready:accept,evidence:reject,evidence:accept" {
		t.Errorf("gate runs = %v", runs)
	}
	if n := len(s.ledger(exec, "attachment.added")); n != 1 {
		t.Errorf("attachment.added = %d", n)
	}
	if n := len(s.ledger(exec, "item.transitioned")); n != 2 {
		t.Errorf("item.transitioned = %d", n)
	}
	s.must("ledger", "verify")
}
