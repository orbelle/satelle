//go:build integration && unix

package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/cli"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const treeCheck = "satelle-tree-unchanged-check"

// treeSandbox is a repo with an origin remote holding main, a route
// open -> plan -> build -> done whose plan performer produces only
// attachments, whose build performer may change the tree, whose
// build -> plan is gateless, and whose plan -> build runs this repo's copy of
// satelle-tree-unchanged-check.
type treeSandbox struct {
	*sandbox
}

const treeRoute = `name = "route"
[states.open]
start = true
[states.plan]
performer = { agent = "worker", skill = "work" }
edits = "attachments"
[states.build]
performer = { agent = "worker", skill = "work" }
[states.done]
terminal = true

[gates.tree-unchanged]
skill = "satelle-tree-unchanged-check"
timeout = "2m"

[[transition]]
from = ["open"]
to = "plan"
[[transition]]
from = ["plan"]
to = "build"
groups = [{ mode = "sequential", gates = ["tree-unchanged"] }]
[[transition]]
from = ["build"]
to = "plan"
[[transition]]
from = ["build"]
to = "done"
`

// newTreeSandbox builds the fixture; withOrigin false leaves the repo with no
// origin remote.
func newTreeSandbox(t *testing.T, withOrigin bool) *treeSandbox {
	t.Helper()
	s := newSandbox(t)
	s.env["SATELLE_SESSION"] = "tree-it"
	s.git("branch", "-M", "main")
	s.must("init")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"route\"\n")
	s.write(".satelle/agents.toml", "[agents.worker]\ncommand = \"session\"\n")
	s.write(".satelle/workflows/route.toml", treeRoute)
	s.write(".satelle/skills/work.md", "---\nname: work\n---\nDo it.\n")
	s.write(".satelle/skills/"+treeCheck+".md", repoText(t, ".satelle/skills/"+treeCheck+".md"))
	gi, err := os.ReadFile(filepath.Join(s.dir, ".satelle", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(gi), "\n") {
		if strings.TrimSpace(line) != "runs/" {
			kept = append(kept, line)
		}
	}
	s.write(".satelle/.gitignore", strings.Join(kept, "\n"))
	s.mustConfigValid()
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "tree fixture")
	if withOrigin {
		origin := t.TempDir()
		if r := s.exec("git", "", "init", "-q", "--bare", origin); r.code != 0 {
			t.Fatalf("%s", gitFailure([]string{"init", "--bare"}, r))
		}
		s.git("remote", "add", "origin", origin)
		s.git("push", "-q", "origin", "main")
	}
	return &treeSandbox{s}
}

// item creates a story and moves it to plan.
func (s *treeSandbox) item(title string) string {
	s.t.Helper()
	id := s.create("story", "--title", title).ID
	s.must("story", "set", id, "--status", "plan")
	return id
}

// commit writes rel and commits it.
func (s *treeSandbox) commit(rel, body string) {
	s.t.Helper()
	s.write(rel, body)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "change "+rel)
}

// present moves id plan -> build and returns the exit code and the latest
// tree-unchanged gate run.
func (s *treeSandbox) present(id string) (int, gateRun) {
	s.t.Helper()
	r := s.run("story", "set", id, "--status", "build")
	if r.code != 0 && r.code != 2 {
		s.t.Fatalf("present %s: exit %d\n%s%s", id, r.code, r.out, r.err)
	}
	var last gateRun
	ran := false
	for _, g := range s.gateRuns(id) {
		if g.Gate == "tree-unchanged" {
			last, ran = g, true
		}
	}
	if !ran {
		s.t.Fatalf("present %s: no tree-unchanged run recorded\n%s%s", id, r.out, r.err)
	}
	return r.code, last
}

// treeChanges is the latest tree-changes attachment of id, and how many
// versions it has.
func (s *treeSandbox) treeChanges(id string) (string, int) {
	s.t.Helper()
	var atts []struct {
		Name string `json:"name"`
	}
	s.mustJSON(&atts, "story", "attachments", id)
	n := 0
	for _, a := range atts {
		if a.Name == "tree-changes" {
			n++
		}
	}
	if n == 0 {
		return "", 0
	}
	return s.must("story", "attachment", id, "tree-changes"), n
}

// refused presents id and fails unless the move is refused with notes naming
// every want, none of reject, and a tree-changes attachment holding every path.
func (s *treeSandbox) refused(id string, paths, want, reject []string) gateRun {
	s.t.Helper()
	code, g := s.present(id)
	if code != 2 || g.Result != "reject" {
		s.t.Fatalf("present %s: exit %d, gate %+v; want a refusal", id, code, g)
	}
	body, _ := s.treeChanges(id)
	for _, p := range paths {
		if !strings.Contains(g.Notes, p) {
			s.t.Errorf("notes do not name %s:\n%s", p, g.Notes)
		}
		if !strings.Contains("\n"+body, "\n"+p+"\n") {
			s.t.Errorf("tree-changes does not hold %s:\n%s", p, body)
		}
	}
	for _, w := range append([]string{"tree-changes", "A plan is judged before any code"}, want...) {
		if !strings.Contains(g.Notes, w) {
			s.t.Errorf("notes do not contain %q:\n%s", w, g.Notes)
		}
	}
	for _, r := range reject {
		if strings.Contains(g.Notes, r) || strings.Contains(body, r) {
			s.t.Errorf("notes or tree-changes name %q:\n%s\n--\n%s", r, g.Notes, body)
		}
	}
	if st := s.get(id).Status; st != "plan" {
		s.t.Errorf("status %q after a refusal, want plan", st)
	}
	return g
}

// accepted presents id and fails unless the move passes naming base, with no
// tree-changes attachment added.
func (s *treeSandbox) accepted(id, base string) {
	s.t.Helper()
	_, before := s.treeChanges(id)
	code, g := s.present(id)
	if code != 0 || g.Result != "accept" {
		s.t.Fatalf("present %s: exit %d, gate %+v; want accept", id, code, g)
	}
	if !strings.Contains(g.Notes, "unchanged since it left "+base+" at") {
		s.t.Errorf("accept notes do not name %s: %s", base, g.Notes)
	}
	if _, after := s.treeChanges(id); after != before {
		s.t.Errorf("an accept added a tree-changes attachment (%d -> %d)", before, after)
	}
}

// parent makes item P on a branch named by its id cut from main, with code
// committed on it. moves are applied to P in order before the code is
// committed when they are given in first, after it otherwise; push pushes the
// branch to origin. It leaves P's branch checked out.
func (s *treeSandbox) parent(first, after []string, push bool) string {
	s.t.Helper()
	p := s.create("story", "--title", "parent").ID
	s.git("checkout", "-q", "-b", p, "main")
	for _, st := range first {
		s.must("story", "set", p, "--status", st)
	}
	s.commit("parent.go", "package parent\n")
	for _, st := range after {
		s.must("story", "set", p, "--status", st)
	}
	if push {
		s.git("push", "-q", "origin", p)
	}
	return p
}

// AC5: every changed tree is refused, whatever base a seat named.
func TestTreeUnchangedRefusesAChangedTree(t *testing.T) {
	t.Parallel()
	notUsed := func(ref string) string { return "The base a seat named, " + ref + ", is not used" }
	for _, c := range []struct {
		name string
		run  func(t *testing.T, s *treeSandbox)
	}{
		{"tracked file edited", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.must("story", "seat", id)
			s.write(".satelle/workflows/route.toml", treeRoute+"# edited\n")
			s.refused(id, []string{".satelle/workflows/route.toml"}, []string{"origin/main"}, nil)
		}},
		{"untracked file", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.must("story", "seat", id)
			s.write("new.go", "package x\n")
			s.refused(id, []string{"new.go"}, nil, nil)
		}},
		{"commit after the seat", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.must("story", "seat", id)
			s.commit("code.go", "package x\n")
			s.refused(id, []string{"code.go"}, nil, nil)
		}},
		{"seated after the code", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.commit("code.go", "package x\n")
			s.must("story", "seat", id)
			s.refused(id, []string{"code.go"}, nil, nil)
		}},
		{"re-seated with --base HEAD", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.must("story", "seat", id)
			s.commit("code.go", "package x\n")
			s.must("story", "seat", id, "--base", "HEAD")
			s.refused(id, []string{"code.go"}, []string{"left origin/main", notUsed("HEAD"), "not the branch of an item"}, nil)
		}},
		{"re-seated at the coded commit by other refs", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.commit("code.go", "package x\n")
			head := strings.TrimSpace(s.git("rev-parse", "HEAD"))
			s.must("story", "seat", id, "--base", head)
			s.refused(id, []string{"code.go"}, []string{"left origin/main", notUsed(head)}, nil)
			s.git("branch", "coded", "HEAD")
			s.must("story", "seat", id, "--base", "coded")
			s.refused(id, []string{"code.go"}, []string{"left origin/main", notUsed("coded")}, nil)
			s.git("branch", "origin/main", "HEAD")
			s.must("story", "seat", id, "--base", "origin/main")
			s.refused(id, []string{"code.go"}, []string{"left origin/main"}, nil)
		}},
		{"code pushed under another branch name", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.commit("code.go", "package x\n")
			s.git("push", "-q", "origin", "HEAD:scratch")
			s.must("story", "seat", id, "--base", "origin/scratch")
			s.refused(id, []string{"code.go"}, []string{"left origin/main", notUsed("origin/scratch"), "not the branch of an item"}, nil)
		}},
		{"the item's own branch as base", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.commit("code.go", "package x\n")
			s.git("push", "-q", "origin", id)
			s.must("story", "seat", id, "--base", "origin/"+id)
			s.refused(id, []string{"code.go"}, []string{"left origin/main", "the item's own branch"}, nil)
		}},
		{"stacked on a parent still in plan", func(t *testing.T, s *treeSandbox) {
			p := s.parent([]string{"plan"}, nil, true)
			id := s.item("a")
			s.git("checkout", "-q", "-b", id, p)
			s.must("story", "seat", id, "--base", "origin/"+p)
			s.refused(id, []string{"parent.go"}, []string{"left origin/main", notUsed("origin/" + p), "has not passed its own plan"}, nil)
		}},
		{"stacked on a parent still in backlog", func(t *testing.T, s *treeSandbox) {
			p := s.parent(nil, nil, true)
			id := s.item("a")
			s.git("checkout", "-q", "-b", id, p)
			s.must("story", "seat", id, "--base", "origin/"+p)
			s.refused(id, []string{"parent.go"}, []string{"left origin/main", notUsed("origin/" + p), "is in open"}, nil)
		}},
		{"stacked on a parent returned to plan", func(t *testing.T, s *treeSandbox) {
			p := s.parent([]string{"plan", "build"}, []string{"plan"}, true)
			id := s.item("a")
			s.git("checkout", "-q", "-b", id, p)
			s.must("story", "seat", id, "--base", "origin/"+p)
			s.refused(id, []string{"parent.go"}, []string{"left origin/main", notUsed("origin/" + p), "has not passed its own plan"}, nil)
		}},
		{"stacked on a parent past plan not on origin", func(t *testing.T, s *treeSandbox) {
			p := s.parent([]string{"plan", "build"}, nil, false)
			id := s.item("a")
			s.git("checkout", "-q", "-b", id, p)
			s.must("story", "seat", id, "--base", p)
			s.refused(id, []string{"parent.go"}, []string{"left origin/main", p + " has no branch on origin"}, nil)
		}},
		{"an older qualifying seat", func(t *testing.T, s *treeSandbox) {
			p := s.parent([]string{"plan", "build"}, nil, true)
			id := s.item("a")
			s.git("checkout", "-q", "-b", id, p)
			s.must("story", "seat", id, "--base", "origin/"+p)
			s.must("story", "release", id)
			s.git("push", "-q", "origin", "HEAD:scratch")
			s.must("story", "seat", id, "--base", "origin/scratch")
			s.commit("own.go", "package own\n")
			s.refused(id, []string{"own.go"}, []string{"left origin/" + p}, []string{"parent.go"})
		}},
		{"re-seated with no --base and after release", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.commit("code.go", "package x\n")
			s.must("story", "seat", id)
			s.refused(id, []string{"code.go"}, nil, nil)
			s.must("story", "seat", id)
			s.refused(id, []string{"code.go"}, nil, nil)
			s.must("story", "release", id)
			s.refused(id, []string{"code.go"}, nil, nil)
		}},
		{"a tracked file under .satelle outside runs", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.write(".satelle/skills/work.md", "---\nname: work\n---\nDo it again.\n")
			s.refused(id, []string{".satelle/skills/work.md"}, nil, nil)
		}},
		{"a return to plan after coding", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.must("story", "seat", id)
			s.accepted(id, "origin/main")
			s.commit("code.go", "package x\n")
			s.must("story", "set", id, "--status", "plan")
			s.refused(id, []string{"code.go"}, nil, nil)
		}},
		{"stacked on a parent past plan, with its own commit", func(t *testing.T, s *treeSandbox) {
			p := s.parent([]string{"plan", "build"}, nil, true)
			for _, base := range []string{"origin/" + p, p} {
				id := s.item("a")
				s.git("checkout", "-q", "-b", id, p)
				s.must("story", "seat", id, "--base", base)
				s.commit("own-"+id+".go", "package own\n")
				s.refused(id, []string{"own-" + id + ".go"}, []string{"left origin/" + p}, []string{"parent.go"})
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t, newTreeSandbox(t, true))
		})
	}
}

// AC6: a tree with nothing of its own outside .satelle/runs passes.
func TestTreeUnchangedAcceptsAnUnchangedTree(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		run  func(t *testing.T, s *treeSandbox)
	}{
		{"clean branch at the fork", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.must("story", "seat", id)
			s.exec(satelleBin, "# plan\n", "story", "attach", id, "--name", "plan", "--file", "-")
			s.accepted(id, "origin/main")
		}},
		{"only .satelle/runs differs", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.write(".satelle/runs/r1/record.txt", "a run\n")
			if out := s.git("status", "--porcelain"); !strings.Contains(out, ".satelle/runs") {
				t.Fatalf("git does not list .satelle/runs, so the case proves nothing:\n%s", out)
			}
			s.accepted(id, "origin/main")
		}},
		{"merged origin/main with no change of its own", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.must("story", "seat", id)
			s.git("checkout", "-q", "main")
			s.commit("landed.go", "package landed\n")
			s.git("push", "-q", "origin", "main")
			s.git("checkout", "-q", id)
			s.git("merge", "-q", "--no-ff", "-m", "merge main", "origin/main")
			s.accepted(id, "origin/main")
		}},
		{"fast-forwarded onto a newer origin/main", func(t *testing.T, s *treeSandbox) {
			id := s.item("a")
			s.git("checkout", "-q", "-b", id)
			s.must("story", "seat", id)
			s.git("checkout", "-q", "main")
			s.commit("landed.go", "package landed\n")
			s.git("push", "-q", "origin", "main")
			s.git("checkout", "-q", id)
			s.git("merge", "-q", "--ff-only", "origin/main")
			s.accepted(id, "origin/main")
		}},
		{"stacked on a parent that passed its plan", func(t *testing.T, s *treeSandbox) {
			p := s.parent([]string{"plan", "build"}, nil, true)
			id := s.item("a")
			s.git("checkout", "-q", "-b", id, p)
			s.must("story", "seat", id, "--base", "origin/"+p)
			s.accepted(id, "origin/"+p)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t, newTreeSandbox(t, true))
		})
	}
}

// Every path is named in the attachment, which is not recorded twice for the
// same list; the notes name the attachment and the count.
func TestTreeUnchangedNamesEveryPath(t *testing.T) {
	t.Parallel()
	s := newTreeSandbox(t, true)
	id := s.item("a")
	s.git("checkout", "-q", "-b", id)
	total := 0
	for i := range 300 {
		name := fmt.Sprintf("many/a-rather-long-file-name-to-pass-the-bound-%03d.txt", i)
		total += len(name) + 1
		s.write(name, "x\n")
	}
	if total <= 8192 {
		t.Fatalf("the names total %d bytes, not more than 8 KB", total)
	}
	g := s.refused(id, nil, []string{"300 path(s)"}, nil)
	body, n := s.treeChanges(id)
	if lines := strings.Split(strings.TrimRight(body, "\n"), "\n"); len(lines) != 300 || n != 1 {
		t.Fatalf("tree-changes has %d lines in %d versions, want 300 in 1", len(lines), n)
	}
	s.refused(id, nil, nil, nil)
	if _, n := s.treeChanges(id); n != 1 {
		t.Errorf("a second refusal on the same tree recorded tree-changes again (%d versions)", n)
	}
	re := regexp.MustCompile(`satelle [^.]*`)
	for _, m := range re.FindAllString(g.Notes, -1) {
		if !strings.Contains(m, "--session <session>") {
			t.Errorf("a shown command does not name the session: %q", m)
		}
	}
}

// With no origin/main the check fails closed, whatever the seat names.
func TestTreeUnchangedFailsClosedWithoutOrigin(t *testing.T) {
	t.Parallel()
	s := newTreeSandbox(t, false)
	id := s.item("a")
	for _, seat := range [][]string{nil, {"--base", "HEAD"}} {
		s.must(append([]string{"story", "seat", id}, seat...)...)
		code, g := s.present(id)
		if code != 2 || g.Result != "reject" || !strings.Contains(g.Notes, "origin/main") {
			t.Fatalf("seat %v: exit %d, gate %+v; want a refusal naming origin/main", seat, code, g)
		}
	}
}

// satelle init ships the check, byte for byte this repo's copy.
func TestTreeUnchangedCheckShipsWithInit(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	got, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "skills", treeCheck+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != repoText(t, ".satelle/skills/"+treeCheck+".md") {
		t.Error("init's satelle-tree-unchanged-check differs from this repo's copy")
	}
	if string(got) != repoText(t, "internal/kernel/template/skills/"+treeCheck+".md") {
		t.Error("init's satelle-tree-unchanged-check differs from the template")
	}
}

// AC2 and the wiring: this repo's delivery plan allows attachments only, the
// help documents the key, and the check runs first on plan -> in_progress,
// with the rule in the skill and not in the binary.
func TestDeliveryPlanAllowsAttachmentsOnly(t *testing.T) {
	t.Parallel()
	routes, err := workflow.Load("../.satelle/workflows")
	if err != nil {
		t.Fatal(err)
	}
	d := routes["delivery"]
	if d == nil {
		t.Fatal("no delivery workflow")
	}
	if got := d.States["plan"].EditScope(); got != workflow.EditsAttachments {
		t.Errorf("delivery plan edit scope %q, want attachments", got)
	}
	if got := d.States["in_progress"].EditScope(); got != workflow.EditsTree {
		t.Errorf("delivery in_progress edit scope %q, want tree", got)
	}
	tr, err := d.Find("plan", "in_progress", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Groups) == 0 || len(tr.Groups[0].Gates) == 0 || tr.Groups[0].Gates[0] != "tree-unchanged" {
		t.Errorf("plan -> in_progress groups %+v, want tree-unchanged first", tr.Groups)
	}
	g := d.Gates["tree-unchanged"]
	if g.Skill != treeCheck || g.Agent != "" || len(g.Panel) != 0 || g.Carry {
		t.Errorf("gate tree-unchanged %+v, want the check with no agent, panel or carry", g)
	}
	s := newSandbox(t)
	if h := s.must("help", "workflows"); !strings.Contains(h, `edits = "attachments"`) || !strings.Contains(h, treeCheck) {
		t.Errorf("help workflows does not document edits and the check")
	}
	root := cli.NewRootCmd()
	diff, _, err := root.Find([]string{"story", "diff"})
	if err != nil {
		t.Fatal(err)
	}
	if diff.Flags().Lookup("fork") != nil {
		t.Error("story diff has a --fork flag: the plan-entry rule has a second home in the binary")
	}
}
