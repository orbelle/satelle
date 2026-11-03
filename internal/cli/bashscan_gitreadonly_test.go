package cli

// sty_56d50bf6 — `git -C DIR <read-only subcommand>` mutates nothing in DIR, so the
// Bash edit gate must not judge it as a mutation of DIR's tree; a git subcommand
// that writes still is one. A shell mutation of a linked worktree is judged against
// the seat of THAT worktree.

import (
	"os"
	"path/filepath"
	"testing"
)

// AC1: the read-only verbs name no mutation target for their -C dir; commit, add,
// checkout, merge and reset still do, as does a verb that cannot be parsed.
func TestBashMutationTargetsGitReadOnlyDir(t *testing.T) {
	const anchor = "/repo"
	const wt = "/other/wt"
	for _, sub := range []string{"diff main --stat", "status", "log --oneline", "show HEAD", "rev-parse HEAD", "rev-list HEAD", "ls-files"} {
		for _, cmd := range []string{"git -C " + wt + " " + sub, "git --no-pager -C " + wt + " " + sub} {
			home, foreign := bashMutationTargets(cmd, anchor)
			if len(home)+len(foreign) != 0 {
				t.Errorf("%q: a read-only git command named targets %v %v", cmd, home, foreign)
			}
		}
	}
	for _, sub := range []string{"commit -m x", "add -A", "checkout main", "merge main", "reset --hard", "push", "git"} {
		cmd := "git -C " + wt + " " + sub
		if sub == "git" {
			cmd = "git -C " + wt // no subcommand: cannot be shown read-only
		}
		_, foreign := bashMutationTargets(cmd, anchor)
		if len(foreign) != 1 || foreign[0] != wt {
			t.Errorf("%q: foreign targets = %v, want [%s]", cmd, foreign, wt)
		}
	}
	// A read-only verb that writes its operand is not read-only.
	for _, cmd := range []string{"git -C " + wt + " diff --output=/tmp/x", "git -C " + wt + " log --output /tmp/x"} {
		if _, foreign := bashMutationTargets(cmd, anchor); len(foreign) != 1 {
			t.Errorf("%q: --output writes, so -C must stay a target: %v", cmd, foreign)
		}
	}
	// Only git is relaxed: make and tar keep the -C target whatever follows.
	for _, cmd := range []string{"make -C " + wt + " status", "tar -C " + wt + " -xf a.tar"} {
		if _, foreign := bashMutationTargets(cmd, anchor); len(foreign) != 1 {
			t.Errorf("%q: -C must stay a target: %v", cmd, foreign)
		}
	}
}

// AC1: with a real linked worktree of the session repository, the edit gate's
// predicate separates a read-only git -C from a writing one.
func TestBashMutatesTreeGitReadOnlyLinkedWorktree(t *testing.T) {
	fenceTempRootsElsewhere(t)
	// Outside every temp root: a path under one is exempt before it is judged.
	base, err := os.MkdirTemp("/var/tmp", "satelle-readonly-")
	if err != nil {
		t.Skipf("cannot place a tree outside the temp roots: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	main := gitInitRepo(t, filepath.Join(base, "main"))
	wt := filepath.Join(base, "linked")
	gitIn(t, main, "worktree", "add", "-q", "-b", "linked-ro", wt)
	if bashMutatesTreeWith("git -C "+wt+" diff main --stat", main, nil) {
		t.Error("git -C <linked worktree> diff is a read, not a tree mutation")
	}
	for _, sub := range []string{"commit -m x", "add -A", "checkout main", "merge main", "reset --hard"} {
		if !bashMutatesTreeWith("git -C "+wt+" "+sub, main, nil) {
			t.Errorf("git -C <linked worktree> %s must still be a tree mutation", sub)
		}
	}
}

// twoSeatsInLinkedWorktrees is multiSeatRepo with real git: the anchor tree is a
// repository and wtB a linked worktree of it, so the foreign-tree fence lets a
// command at wtB through to the seat gate.
func twoSeatsInLinkedWorktrees(t *testing.T, aStatus, bStatus string, aFirst bool) multiSeat {
	t.Helper()
	f := multiSeatRepo(t, aStatus, "feature", bStatus, aFirst, "")
	// Swap the fake .git markers for a real repository and a real linked worktree.
	if err := os.Remove(filepath.Join(f.repo, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.wtB, ".git")); err != nil {
		t.Fatal(err)
	}
	gitInitCommit(t, f.repo)
	gitIn(t, f.repo, "worktree", "add", "-q", "-b", "seat-b", f.wtB)
	return f
}
