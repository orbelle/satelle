//go:build integration && !windows

package tests

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// coveredWorkflow has a code-ac judge on work -> review, and gateless rework
// and done moves: done is reached with no judge of its own.
const coveredWorkflow = `name = "covered"
[states.todo]
start = true
[states.work]
[states.review]
[states.done]
terminal = true

[gates.code-ac]
skill = "code-ac"
agent = "reviewer"

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

// coveredSandbox is a repo on main whose items take the covered workflow,
// judged by a fake claude that accepts.
func coveredSandbox(t *testing.T) *sandbox {
	t.Helper()
	s := newSandbox(t)
	fakeClaude(t, s)
	s.git("branch", "-M", "main")
	s.must("init")
	s.write(".satelle/workflows/covered.toml", coveredWorkflow)
	s.write(".satelle/skills/code-ac.md", "Review the change against the criteria.\n")
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

// diffJSON is story diff --json.
type diffJSON struct {
	Item   string   `json:"item"`
	Base   string   `json:"base"`
	Head   string   `json:"head"`
	Paths  []string `json:"paths"`
	Frozen bool     `json:"frozen"`
}

// The satelle-server sequence: code-ac accepts, an unrelated commit lands
// afterwards, and the gateless move to done is refused, naming the gate to
// re-run. Re-running it over the new change lets done through, and the done
// item's diff is frozen at the change it reached done with.
func TestACommitAfterReviewRefusesDone(t *testing.T) {
	t.Parallel()
	s := coveredSandbox(t)
	id := reviewedItem(s)
	s.commitFile(".satelle/skills/fix.md", "an unrelated skill fix\n")

	before := len(s.ledger(id, ""))
	r := s.run("story", "set", id, "--status", "done")
	if r.code != 1 || !strings.Contains(r.err, "code-ac") || !strings.Contains(r.err, "work -> review") || !strings.Contains(r.err, "Re-run code-ac") {
		t.Fatalf("done after a later commit: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if st := s.get(id).Status; st != "review" {
		t.Fatalf("the refused item moved to %s", st)
	}
	if n := len(s.ledger(id, "")); n != before {
		t.Fatalf("the refused move recorded %d event(s)", n-before)
	}

	// AC2: re-run code-ac through the workflow's rework route.
	s.must("story", "set", id, "--status", "work")
	s.must("story", "set", id, "--status", "review")
	if run := latestRun(s, id, "code-ac"); run.Result != "accept" {
		t.Fatalf("code-ac re-run = %+v, want a fresh accept", run)
	}
	s.must("story", "set", id, "--status", "done")
	if st := s.get(id).Status; st != "done" {
		t.Fatalf("status = %s, want done", st)
	}
}

// AC4: a done item's diff is the change it reached done with; a principle
// committed later, or an untracked file, is not attributed to it.
func TestADoneItemsDiffIsFrozen(t *testing.T) {
	t.Parallel()
	s := coveredSandbox(t)
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

// AC5: done presented from main after a merge and a bump is not refused
// while the item's own branch is still at the change its judge accepted; a
// branch moved past it is.
func TestDoneFromMainIsNotRefusedWhileTheBranchIsJudged(t *testing.T) {
	t.Parallel()
	for _, moved := range []bool{false, true} {
		s := coveredSandbox(t)
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
		case moved && (r.code != 1 || !strings.Contains(r.err, "code-ac") || s.get(id).Status != "review"):
			t.Fatalf("done from main with feat/x past its judge: exit %d\n%s%s", r.code, r.out, r.err)
		}
	}
}
