//go:build integration && !windows

package tests

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

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

func (s *sandbox) commitFile(rel, body string) {
	s.t.Helper()
	s.write(rel, body)
	s.git("add", "--", rel)
	s.git("commit", "-q", "-m", "add "+rel)
}

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

type diffJSON struct {
	Item      string        `json:"item"`
	Base      string        `json:"base"`
	Head      string        `json:"head"`
	Paths     []string      `json:"paths"`
	Untracked *[]string     `json:"untracked"`
	Frozen    bool          `json:"frozen"`
	Judged    *[]judgedJSON `json:"judged"`
}

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
	if again.Untracked != nil {
		t.Fatalf("a frozen diff lists untracked files %v; it has no untracked key", *again.Untracked)
	}
}

func TestDiffListsUntrackedApart(t *testing.T) {
	t.Parallel()
	s := coveredSandbox(t, false)
	id := s.create("story", "--title", "untracked").ID
	s.must("story", "set", id, "--status", "work")
	s.must("story", "seat", id, "--session", "s1")
	s.commitFile("web/a.go", "package web\n")

	var clean diffJSON
	s.mustJSON(&clean, "story", "diff", id, "--json")
	if clean.Untracked == nil || len(*clean.Untracked) != 0 {
		t.Fatalf("diff --json untracked = %v, want [] with no untracked file", clean.Untracked)
	}

	s.write(".satelle/skills/code-ac.md", "Review the change against the criteria, closely.\n")
	s.write("scratch.txt", "untracked\n")
	lines := strings.Split(strings.TrimSuffix(s.must("story", "diff", id), "\n"), "\n")
	at := slices.Index(lines, "untracked: scratch.txt")
	if at < 0 || slices.Contains(lines, "scratch.txt") {
		t.Fatalf("diff = %q, want scratch.txt marked untracked and not a bare path", lines)
	}
	for _, p := range []string{".satelle/skills/code-ac.md", "web/a.go"} {
		if i := slices.Index(lines, p); i < 0 || i > at {
			t.Fatalf("diff = %q, want %s as a bare line before the untracked one", lines, p)
		}
	}

	var d diffJSON
	s.mustJSON(&d, "story", "diff", id, "--json")
	if slices.Contains(d.Paths, "scratch.txt") || !slices.Contains(d.Paths, "web/a.go") || !slices.Contains(d.Paths, ".satelle/skills/code-ac.md") {
		t.Fatalf("diff --json paths = %v, want the committed and tracked changes only", d.Paths)
	}
	if d.Untracked == nil || !slices.Equal(*d.Untracked, []string{"scratch.txt"}) {
		t.Fatalf("diff --json untracked = %v, want [scratch.txt]", d.Untracked)
	}
}

func TestDoneFromMainIsNotRefusedWhileTheBranchIsJudged(t *testing.T) {
	t.Parallel()
	for _, moved := range []bool{false, true} {
		s := coveredSandbox(t, true)
		s.git("config", "diff.mnemonicPrefix", "true")
		s.git("config", "diff.algorithm", "histogram")
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

func TestDoneFromMainAfterALandingIsAccepted(t *testing.T) {
	t.Parallel()
	for _, landing := range []string{"--ff-only", "--no-ff"} {
		s := coveredSandbox(t, true)
		s.git("config", "diff.mnemonicPrefix", "true")
		s.git("config", "diff.algorithm", "histogram")
		origin := t.TempDir()
		if r := s.exec("git", "", "init", "-q", "--bare", origin); r.code != 0 {
			t.Fatalf("%s", gitFailure([]string{"init", "--bare"}, r))
		}
		s.git("remote", "add", "origin", origin)
		s.git("push", "-q", "origin", "main")
		s.git("checkout", "-q", "-b", "feat/x")
		id := reviewedItem(s)
		s.git("push", "-q", "origin", "feat/x")
		s.git("checkout", "-q", "main")
		s.git("merge", "-q", landing, "--no-edit", "feat/x")
		s.commitFile("bump.txt", "the release bump\n")
		s.git("push", "-q", "origin", "main")
		s.git("branch", "-q", "-D", "feat/x")

		r := s.run("story", "set", id, "--status", "done")
		if r.code != 0 || s.get(id).Status != "done" {
			t.Fatalf("done from main after a %s landing: exit %d\n%s%s", landing, r.code, r.out, r.err)
		}
		if g := latestRun(s, id, "change-judged"); g.Result != "accept" {
			t.Fatalf("change-judged after a %s landing = %+v, want accept", landing, g)
		}
	}
}
