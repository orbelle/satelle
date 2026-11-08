//go:build integration && unix

package tests

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// resultSummarySandbox is a repo whose workflow ends the agent's work at
// confirm, the operator's state, through the repo's own result-summary check
// last on integration -> confirm, after a trivial check ok.
func resultSummarySandbox(t *testing.T) *sandbox {
	t.Helper()
	s := newSandbox(t)
	s.must("init")
	skill, err := os.ReadFile("../.satelle/skills/satelle-result-summary.md")
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/skills/satelle-result-summary.md", string(skill))
	s.write(".satelle/skills/ok.md", "---\ntype: check\n---\n```sh\necho fine\n```\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.backlog]
start = true
[states.integration]
[states.confirm]
performer = { operator = "confirmation" }
[states.approved]
[states.done]
terminal = true
[gates.ok]
skill = "ok"
[gates.result-summary]
skill = "satelle-result-summary"
[[transition]]
from = ["backlog"]
to = "integration"
groups = [{ mode = "sequential", gates = ["ok"] }]
[[transition]]
from = ["integration"]
to = "confirm"
groups = [{ mode = "sequential", gates = ["ok", "result-summary"] }]
[[transition]]
from = ["confirm"]
to = "approved"
[[transition]]
from = ["confirm"]
to = "backlog"
[[transition]]
from = ["approved"]
to = "done"
`)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "config")
	s.must("validate")
	return s
}

// attachStdin attaches body to id under name.
func (s *sandbox) attachStdin(id, name, mediaType, body string) {
	s.t.Helper()
	if r := s.exec(satelleBin, body, "story", "attach", id, "--name", name, "--type", mediaType, "--file", "-"); r.code != 0 {
		s.t.Fatalf("attach %s: exit %d\n%s%s", name, r.code, r.out, r.err)
	}
}

// AC2: entering confirm attaches the generated result summary, typed brief,
// with the changes, every gate verdict, the evidence and the coder's how-to;
// without a how-to the move is rejected and nothing is attached.
func TestResultSummaryOnEnteringConfirm(t *testing.T) {
	t.Parallel()
	s := resultSummarySandbox(t)
	id := s.create("story", "--title", "summarised", "--criteria", "it works").ID
	s.write("a.txt", "a\n")
	s.git("add", "a.txt")
	s.git("commit", "-q", "-m", "first change")
	s.write("b.txt", "b\n")
	s.git("add", "b.txt")
	s.git("commit", "-q", "-m", "second change")
	head := strings.TrimSpace(s.git("rev-parse", "HEAD"))
	s.must("story", "set", id, "--status", "integration")

	review, _ := json.Marshal(map[string]any{"event": "APPROVE", "pull_number": 42, "commit_sha": head, "review_id": 7, "html_url": "https://github.com/o/r/pull/42#pullrequestreview-7", "repository": "o/r"})
	s.must("ledger", "append", id, "--kind", "github.review", "--payload", string(review))
	// Without a how-to (and every other section composable): rejected, still at
	// integration, nothing attached.
	s.attachStdin(id, "ac-evidence", "text/markdown", "## Per-AC evidence\n\n| AC | proof |\n|----|-------|\n| 1 | TestX |\n")
	r := s.run("story", "set", id, "--status", "confirm")
	if r.code != 2 || !strings.Contains(r.out+r.err, "ac-evidence carries no '## How to try it' section") {
		t.Fatalf("no how-to: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if st := s.get(id).Status; st != "integration" {
		t.Fatalf("status %s after the reject", st)
	}
	if out := s.must("story", "attachments", id); strings.Contains(out, "result-summary") {
		t.Fatalf("a rejected move attached the summary:\n%s", out)
	}

	s.attachStdin(id, "ac-evidence", "text/markdown", "## Per-AC evidence\n\n| AC | proof |\n|----|-------|\n| 1 | TestX |\n\n## How to try it\n\n1. run make try-it and see the greeting\n")
	if out := s.must("story", "set", id, "--status", "confirm"); !strings.Contains(out, "result summary attached") {
		t.Fatalf("into confirm:\n%s", out)
	}
	if st := s.get(id).Status; st != "confirm" {
		t.Fatalf("status %s", st)
	}
	var atts []struct {
		Name      string `json:"name"`
		MediaType string `json:"media_type"`
	}
	s.mustJSON(&atts, "story", "attachments", id)
	typed := false
	for _, a := range atts {
		typed = typed || (a.Name == "result-summary" && a.MediaType == "text/markdown; type=brief")
	}
	if !typed {
		t.Fatalf("no result-summary typed brief: %+v", atts)
	}
	body := s.must("story", "attachment", id, "result-summary")
	for _, want := range []string{
		"## Changes", "## Gate verdicts", "## Evidence", "## How to try it",
		"first change", "second change",
		"**backlog->integration** (1 round)", "- ok: accept",
		"- ok: accepted in this move",
		"https://github.com/o/r/pull/42", "pullrequestreview-7", head,
		"run make try-it and see the greeting",
		"satelle story attachment " + id + " ac-evidence",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("summary lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "- result-summary: accepted") {
		t.Errorf("the summary lists itself as accepted:\n%s", body)
	}
}
