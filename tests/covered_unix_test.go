//go:build integration && !windows

package tests

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// coveredWorkflow has a code-ac judge on work -> review, and a gateless
// rework move: done is reached with no judge of its own. With check, review
// -> done runs the repo's change-judged check; without it, done has no gate.
func coveredWorkflow(check bool) string {
	w := `name = "covered"
[states.todo]
start = true
[states.work]
[states.review]
[states.done]
terminal = true

[gates.code-ac]
skill = "code-ac"
agent = "reviewer"
`
	if check {
		w += `
[gates.change-judged]
skill = "satelle-change-judged-check"
`
	}
	w += `
[[transition]]
from = ["todo"]
to = "work"
[[transition]]
from = ["work"]
to = "review"
groups = [{ gates = ["code-ac"] }]
[[transition]]
from = ["review"]
to = "work"
[[transition]]
from = ["review"]
to = "done"
`
	if check {
		w += "groups = [{ gates = [\"change-judged\"] }]\n"
	}
	return w
}

// coveredSandbox is a repo on main whose items take the covered workflow,
// judged by a fake claude that accepts; with check, done runs this repo's
// satelle-change-judged-check.
func coveredSandbox(t *testing.T, check bool) *sandbox {
	t.Helper()
	if _, err := exec.LookPath("jq"); check && err != nil {
		t.Fatal("jq is required by satelle-change-judged-check and is not on PATH")
	}
	s := newSandbox(t)
	fakeClaude(t, s)
	s.git("branch", "-M", "main")
	s.must("init")
	s.write(".satelle/workflows/covered.toml", coveredWorkflow(check))
	s.write(".satelle/skills/code-ac.md", "Review the change against the criteria.\n")
	if check {
		s.write(".satelle/skills/satelle-change-judged-check.md", repoText(t, ".satelle/skills/satelle-change-judged-check.md"))
	}
	s.write(".satelle/agents.toml", "[agents.reviewer]\ncommand = \"claude\"\nmodel = \"rv-1\"\ntools = [\"Read\"]\n")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"covered\"\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "config")
	s.must("validate")
	return s
}

// commit writes rel and commits it.
func (s *sandbox) commitFile(rel, body string) {
	s.t.Helper()
	s.write(rel, body)
	s.git("add", "--", rel)
	s.git("commit", "-q", "-m", "add "+rel)
}

// reviewedItem is a story whose code-ac accepted web/a.go on work -> review,
// worked on the current branch.
func reviewedItem(s *sandbox) string {
	s.t.Helper()
	id := s.create("story", "--title", "covered").ID
	s.must("story", "set", id, "--status", "work")
	s.must("story", "seat", id, "--session", "s1")
	s.commitFile("web/a.go", "package web\n")
	s.must("story", "set", id, "--status", "review")
	if run := latestRun(s, id, "code-ac"); run.Result != "accept" {
		s.t.Fatalf("code-ac = %+v", run)
	}
	return id
}

// moves are id's item.transitioned payloads, oldest first.
type moveRec struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Change *struct {
		Base string `json:"base"`
		Head string `json:"head"`
	} `json:"change"`
}

func (s *sandbox) moves(id string) []moveRec {
	s.t.Helper()
	var out []moveRec
	for _, e := range s.ledger(id, "item.transitioned") {
		var m moveRec
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// judgedJSON is one entry of story diff --json's judged.
type judgedJSON struct {
	Gate       string `json:"gate"`
	Transition string `json:"transition"`
	Result     string `json:"result"`
	Attempt    string `json:"attempt"`
	Base       string `json:"base"`
	Branch     string `json:"branch"`
	Judged     string `json:"judged"`
	Now        string `json:"now"`
	NowHead    string `json:"now_head"`
}

// diffJSON is story diff --json.
type diffJSON struct {
	Item   string        `json:"item"`
	Base   string        `json:"base"`
	Head   string        `json:"head"`
	Paths  []string      `json:"paths"`
	Frozen bool          `json:"frozen"`
	Judged *[]judgedJSON `json:"judged"`
}

// The satelle-server sequence: code-ac accepts, an unrelated commit lands
// afterwards, and the change-judged check on done rejects, naming the gate
// to re-run; the item stays in review. Re-running it over the new change
// lets done through.
func TestACommitAfterReviewRefusesDone(t *testing.T) {
	t.Parallel()
	s := coveredSandbox(t, true)
	id := reviewedItem(s)
	s.commitFile(".satelle/skills/fix.md", "an unrelated skill fix\n")

	moved := len(s.moves(id))
	r := s.run("story", "set", id, "--status", "done")
	if r.code != 2 {
		t.Fatalf("done after a later commit: exit %d, want 2\n%s%s", r.code, r.out, r.err)
	}
	if st := s.get(id).Status; st != "review" {
		t.Fatalf("the rejected item moved to %s", st)
	}
	if n := len(s.moves(id)); n != moved {
		t.Fatalf("the rejected move recorded %d move(s)", n-moved)
	}
	if g := latestRun(s, id, "change-judged"); g.Result != "reject" || !strings.Contains(g.Notes, "code-ac") || !strings.Contains(g.Notes, "work -> review") {
		t.Fatalf("change-judged = %+v, want a reject naming code-ac and work -> review", g)
	}

	// Re-run code-ac through the workflow's rework route.
	s.must("story", "set", id, "--status", "work")
	s.must("story", "set", id, "--status", "review")
	if run := latestRun(s, id, "code-ac"); run.Result != "accept" {
		t.Fatalf("code-ac re-run = %+v, want a fresh accept", run)
	}
	if r := s.run("story", "set", id, "--status", "done"); r.code != 0 {
		t.Fatalf("done after the re-run: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if g := latestRun(s, id, "change-judged"); g.Result != "accept" {
		t.Fatalf("change-judged after the re-run = %+v, want accept", g)
	}
	if st := s.get(id).Status; st != "done" {
		t.Fatalf("status = %s, want done", st)
	}
}

// The binary decides nothing about the judged change: without the check, the
// same sequence moves to done.
func TestWithoutTheCheckDoneMovesAfterALaterCommit(t *testing.T) {
	t.Parallel()
	s := coveredSandbox(t, false)
	id := reviewedItem(s)
	s.commitFile(".satelle/skills/fix.md", "an unrelated skill fix\n")
	if r := s.run("story", "set", id, "--status", "done"); r.code != 0 {
		t.Fatalf("done after a later commit, no check: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if st := s.get(id).Status; st != "done" {
		t.Fatalf("status = %s, want done", st)
	}
}

// diff --json reports each judged change and the change now: equal while the
// tree is the reviewed change, different after a later commit.
func TestTheDiffReportsJudgedChanges(t *testing.T) {
	t.Parallel()
	s := coveredSandbox(t, false)
	id := reviewedItem(s)
	one := func() judgedJSON {
		t.Helper()
		var d diffJSON
		s.mustJSON(&d, "story", "diff", id, "--json")
		if d.Judged == nil || len(*d.Judged) != 1 {
			t.Fatalf("diff --json judged = %+v, want one entry", d.Judged)
		}
		j := (*d.Judged)[0]
		if j.Gate != "code-ac" || j.Transition != "work->review" || j.Result != "accept" || j.Judged == "" || j.Base == "" || j.NowHead == "" {
			t.Fatalf("judged entry = %+v, want code-ac's accept on work->review", j)
		}
		return j
	}
	if j := one(); j.Now != j.Judged {
		t.Fatalf("before a later commit: %+v, want now == judged", j)
	}
	s.commitFile("web/b.go", "package web // after review\n")
	if j := one(); j.Now == j.Judged {
		t.Fatalf("after a later commit: %+v, want now != judged", j)
	}
}

// A done item's diff is the change it reached done with; a principle
// committed later, or an untracked file, is not attributed to it.
func TestADoneItemsDiffIsFrozen(t *testing.T) {
	t.Parallel()
	s := coveredSandbox(t, true)
	id := reviewedItem(s)
	s.must("story", "set", id, "--status", "done")

	var first diffJSON
	s.mustJSON(&first, "story", "diff", id, "--json")
	if !first.Frozen || first.Head == "" || strings.Join(first.Paths, ",") != "web/a.go" {
		t.Fatalf("done diff = %+v, want frozen over web/a.go", first)
	}
	mv := s.moves(id)
	last := mv[len(mv)-1]
	if last.To != "done" || last.Change == nil || last.Change.Base != first.Base || last.Change.Head != first.Head {
		t.Fatalf("move to done = %+v, want change %s..%s", last, first.Base, first.Head)
	}

	s.commitFile(".satelle/principles/later.md", "a later principle\n")
	s.write("scratch.txt", "untracked\n")
	var again diffJSON
	s.mustJSON(&again, "story", "diff", id, "--json")
	if again.Base != first.Base || again.Head != first.Head || !slices.Equal(again.Paths, first.Paths) || !again.Frozen {
		t.Fatalf("done diff moved with later work: %+v, was %+v", again, first)
	}
}

// Done presented from main after a merge and a bump is accepted while the
// item's own branch is still at the change its judge accepted; a branch moved
// past it is rejected by the check.
func TestDoneFromMainIsNotRefusedWhileTheBranchIsJudged(t *testing.T) {
	t.Parallel()
	for _, moved := range []bool{false, true} {
		s := coveredSandbox(t, true)
		s.git("checkout", "-q", "-b", "feat/x")
		id := reviewedItem(s)
		if moved {
			s.commitFile("web/b.go", "package web // after review\n")
		}
		s.git("checkout", "-q", "main")
		s.git("merge", "-q", "--no-ff", "--no-edit", "feat/x")
		s.commitFile("other.txt", "another item and the bump\n")

		r := s.run("story", "set", id, "--status", "done")
		switch {
		case !moved && (r.code != 0 || s.get(id).Status != "done"):
			t.Fatalf("done from main with feat/x judged: exit %d\n%s%s", r.code, r.out, r.err)
		case moved && (r.code != 2 || s.get(id).Status != "review"):
			t.Fatalf("done from main with feat/x past its judge: exit %d\n%s%s", r.code, r.out, r.err)
		}
		if moved {
			if g := latestRun(s, id, "change-judged"); g.Result != "reject" || !strings.Contains(g.Notes, "code-ac") {
				t.Fatalf("change-judged = %+v, want a reject naming code-ac", g)
			}
		}
	}
}
