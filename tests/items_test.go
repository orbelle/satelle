package tests

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// Attachments: attach, list, get; each is an attachment.added on the ledger.
func TestAttachments(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	id := s.create("story", "--title", "documented").ID
	s.write("plan.md", "# the plan\n")
	if out := s.must("story", "attach", id, "--file", "plan.md"); !strings.Contains(out, "attached plan.md") {
		t.Errorf("attach: %s", out)
	}
	if r := s.exec(satelleBin, "evidence from stdin\n", "story", "attach", id, "--name", "evidence", "--file", "-"); r.code != 0 {
		t.Fatalf("attach from stdin: %s%s", r.out, r.err)
	}
	var atts []item.Attachment
	s.mustJSON(&atts, "story", "attachments", id)
	names := []string{}
	for _, a := range atts {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "plan.md,evidence" {
		t.Errorf("attachments = %v", names)
	}
	if out := s.must("story", "get", id, "--attachment", "plan.md"); out != "# the plan\n" {
		t.Errorf("plan.md body = %q", out)
	}
	if out := s.must("story", "get", id, "--attachment", "evidence"); out != "evidence from stdin\n" {
		t.Errorf("evidence body = %q", out)
	}
	added := s.ledger(id, "attachment.added")
	if len(added) != 2 {
		t.Fatalf("attachment.added events = %d", len(added))
	}
	var p struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(added[0].Payload, &p); err != nil || p.Name != "plan.md" || p.SHA256 != atts[0].SHA256 {
		t.Errorf("attachment.added payload = %s (%v)", added[0].Payload, err)
	}
	if r := s.run("story", "get", id, "--attachment", "missing"); r.code == 0 {
		t.Error("a missing attachment was printed")
	}
	s.must("ledger", "verify")
}

// A task names its input selection; a run creates an execution recording the
// items it took.
func TestTaskRunCreatesAnExecution(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	a := s.create("story", "--title", "a", "--tag", "pick").ID
	b := s.create("story", "--title", "b", "--tag", "pick").ID
	s.create("story", "--title", "c")
	done := s.create("story", "--title", "d", "--tag", "pick").ID
	s.must("story", "set", done, "--status", "done")

	task := s.create("task", "--title", "review open picks", "--input-kind", "story", "--input-tag", "pick", "--input-status", "backlog")
	if !strings.HasPrefix(task.ID, "tas_") {
		t.Errorf("task id %q", task.ID)
	}
	var run struct {
		Execution item.Item `json:"execution"`
		Inputs    []string  `json:"inputs"`
	}
	s.mustJSON(&run, "task", "run", task.ID)
	if run.Execution.Kind != "execution" || run.Execution.Parent != task.ID {
		t.Errorf("execution = %+v", run.Execution)
	}
	if strings.Join(run.Inputs, ",") != a+","+b && strings.Join(run.Inputs, ",") != b+","+a {
		t.Errorf("inputs = %v, want %s and %s", run.Inputs, a, b)
	}
	if evs := s.ledger(run.Execution.ID, "task.run"); len(evs) != 1 || !strings.Contains(string(evs[0].Payload), a) {
		t.Errorf("task.run on the execution's ledger = %+v", evs)
	}
	if out := s.must("execution", "list", "--parent", task.ID); !strings.Contains(out, run.Execution.ID) {
		t.Errorf("execution list:\n%s", out)
	}
}
