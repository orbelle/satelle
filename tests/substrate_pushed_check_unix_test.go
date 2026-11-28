//go:build integration && unix

package tests

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

const substratePushedSkill = "satelle-substrate-pushed-check"

func substratePushedSandbox(t *testing.T) *sandbox {
	t.Helper()
	s := newSandbox(t)
	remote := realDir(t, t.TempDir())
	if r := s.exec("git", "", "init", "-q", "--bare", remote); r.code != 0 {
		t.Fatalf("%s", gitFailure([]string{"init", "--bare", remote}, r))
	}
	s.git("remote", "add", "origin", remote)
	s.git("checkout", "-q", "-b", "sto_pushed")
	s.write("tracked.txt", "one\n")
	s.git("add", "tracked.txt")
	s.git("commit", "-q", "-m", "tracked")
	s.env["SATELLE_ITEM"] = "sto_pushed"
	return s
}

func TestSubstratePushedCheckRefusesUncommittedTrackedChangesNamingThem(t *testing.T) {
	s := substratePushedSandbox(t)
	s.git("push", "-q", "origin", "HEAD:refs/heads/sto_pushed")
	s.write("tracked.txt", "two\n")
	r := runSkillCheck(t, s, substratePushedSkill)
	if r.code == 0 {
		t.Fatalf("a dirty tracked file was accepted:\n%s", r.out)
	}
	if !strings.Contains(r.out, "uncommitted") || !strings.Contains(r.out, "tracked.txt") {
		t.Fatalf("the refusal does not name the uncommitted path:\n%s", r.out)
	}
}

func TestSubstratePushedCheckIgnoresUntrackedFiles(t *testing.T) {
	s := substratePushedSandbox(t)
	s.git("push", "-q", "origin", "HEAD:refs/heads/sto_pushed")
	s.write("scratch.txt", "untracked\n")
	if r := runSkillCheck(t, s, substratePushedSkill); r.code != 0 {
		t.Fatalf("an untracked file refused the move: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestSubstratePushedCheckRefusesABranchNotOnTheRemote(t *testing.T) {
	s := substratePushedSandbox(t)
	r := runSkillCheck(t, s, substratePushedSkill)
	if r.code == 0 {
		t.Fatalf("an unpushed branch was accepted:\n%s", r.out)
	}
	if !strings.Contains(r.out, "is not on the remote origin") {
		t.Fatalf("the refusal does not say the branch is not pushed:\n%s", r.out)
	}
}

func TestSubstratePushedCheckRefusesAPushedHeadThatIsNotHEAD(t *testing.T) {
	s := substratePushedSandbox(t)
	s.git("push", "-q", "origin", "HEAD:refs/heads/sto_pushed")
	s.write("tracked.txt", "two\n")
	s.git("commit", "-q", "-am", "unpushed")
	r := runSkillCheck(t, s, substratePushedSkill)
	if r.code == 0 {
		t.Fatalf("a branch whose pushed head is behind HEAD was accepted:\n%s", r.out)
	}
	head := strings.TrimSpace(s.git("rev-parse", "--short=12", "HEAD"))
	if !strings.Contains(r.out, "not HEAD "+head) {
		t.Fatalf("the refusal does not name HEAD %s:\n%s", head, r.out)
	}
}

func TestSubstratePushedCheckAcceptsACleanPushedHead(t *testing.T) {
	s := substratePushedSandbox(t)
	s.git("push", "-q", "origin", "HEAD:refs/heads/sto_pushed")
	r := runSkillCheck(t, s, substratePushedSkill)
	if r.code != 0 {
		t.Fatalf("a clean, pushed head was refused: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if !strings.Contains(r.out, "is at HEAD") {
		t.Fatalf("the accept line does not report the pushed head:\n%s", r.out)
	}
}

func TestSubstratePushedGateRunsFirstOnTheMoveToDone(t *testing.T) {
	routes, err := workflow.Load("../.satelle/workflows")
	if err != nil {
		t.Fatal(err)
	}
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	if sk, ok := skills[substratePushedSkill]; !ok || !sk.IsCheck() {
		t.Fatalf("%s is not a check skill of this repo", substratePushedSkill)
	}
	w, ok := routes["substrate"]
	if !ok {
		t.Fatal("no substrate workflow")
	}
	if g, ok := w.Gates["substrate-pushed"]; !ok || g.Skill != substratePushedSkill {
		t.Fatalf("substrate does not declare [gates.substrate-pushed] with skill %s", substratePushedSkill)
	}
	tr, err := w.Find("in_progress", "done", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Groups) == 0 || len(tr.Groups[0].Gates) == 0 || tr.Groups[0].Gates[0] != "substrate-pushed" {
		t.Fatalf("substrate in_progress -> done does not run substrate-pushed first: %v", tr.Groups)
	}
}
