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
	if out := s.must("story", "attachment", id, "plan.md"); out != "# the plan\n" {
		t.Errorf("plan.md body = %q", out)
	}
	if out := s.must("story", "attachment", id, "evidence"); out != "evidence from stdin\n" {
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
	if r := s.run("story", "attachment", id, "missing"); r.code == 0 {
		t.Error("a missing attachment was printed")
	}

	// Versions: the newest by default, any by its sha256 or a 7+ hex prefix.
	v2 := "v2\x00 with nul\r\nand CRLF, no trailing newline"
	if r := s.exec(satelleBin, v2, "story", "attach", id, "--name", "plan.md", "--file", "-"); r.code != 0 {
		t.Fatalf("attach v2: %s%s", r.out, r.err)
	}
	s.mustJSON(&atts, "story", "attachments", id)
	v1sha, v2sha := atts[0].SHA256, atts[len(atts)-1].SHA256
	if out := s.must("story", "attachment", id, "plan.md"); out != v2 {
		t.Errorf("newest plan.md = %q, want v2 byte for byte", out)
	}
	if out := s.must("story", "attachment", id, "plan.md", "--sha", v1sha); out != "# the plan\n" {
		t.Errorf("plan.md --sha v1 = %q", out)
	}
	if out := s.must("story", "attachment", id, "plan.md", "--sha", v2sha[:7]); out != v2 {
		t.Errorf("plan.md --sha <7-hex v2> = %q", out)
	}
	for _, bad := range []string{v2sha[:6], "zzzzzzz", strings.Repeat("0", 64)} {
		if r := s.run("story", "attachment", id, "plan.md", "--sha", bad); r.code == 0 {
			t.Errorf("--sha %s printed a body", bad)
		}
	}
	for _, sha := range []string{"abcdef01" + strings.Repeat("1", 56), "abcdef02" + strings.Repeat("2", 56)} {
		s.must("ledger", "append", id, "--kind", "attachment.added", "--payload", `{"name":"twin","sha256":"`+sha+`","size":1,"media_type":"text/plain"}`)
	}
	if r := s.run("story", "attachment", id, "twin", "--sha", "abcdef0"); r.code == 0 || !strings.Contains(r.err+r.out, "ambiguous") {
		t.Errorf("an ambiguous prefix: code %d, %s%s", r.code, r.out, r.err)
	}
	if r := s.run("story", "get", id, "--attachment", "plan.md"); r.code == 0 {
		t.Error("story get --attachment still exists")
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
