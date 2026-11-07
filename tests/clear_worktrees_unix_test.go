//go:build integration && unix

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// clearWorkflow has a non-terminal working state between start and the
// terminal one, so a story can sit in in_progress (sent back) or done.
const clearWorkflow = `name = "default"

[states.backlog]
start = true

[states.in_progress]

[states.done]
terminal = true

[[transition]]
from = ["backlog"]
to = "in_progress"

[[transition]]
from = ["in_progress"]
to = "done"
`

// TestClearWorktreesKeepsActiveItems drives scripts/clear-worktrees.sh, the
// cleanup task's ACTION (tas_b3424a1c), over clean, pushed worktrees: the one
// whose branch names an item in in_progress (sent back by a release) is kept
// and reported with the item and its state; the one whose item is done, the
// one whose branch names no item and one named like an id no item has are
// removed with their branches; a dirty one is kept as before; the main
// checkout ends on main at origin/main.
func TestClearWorktreesKeepsActiveItems(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by scripts/clear-worktrees.sh and is not on PATH", tool)
		}
	}
	script, err := filepath.Abs("../scripts/clear-worktrees.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := newSandbox(t)
	s.env["SATELLE_SESSION"] = "clear-it"
	origin := realDir(t, t.TempDir())
	s.exec("git", "", "init", "-q", "--bare", origin)
	s.git("remote", "add", "origin", origin)
	s.git("checkout", "-q", "-B", "main")
	s.must("init")
	s.write(".satelle/workflows/default.toml", clearWorkflow)
	s.must("validate")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "base")
	s.git("push", "-q", "-u", "origin", "main")
	s.git("fetch", "-q", "origin")

	active := s.create("story", "--title", "sent back").ID
	s.must("story", "set", active, "--status", "in_progress")
	done := s.create("story", "--title", "released").ID
	s.must("story", "set", done, "--status", "in_progress")
	s.must("story", "set", done, "--status", "done")
	if st := s.get(active).Status; st != "in_progress" {
		t.Fatalf("setup: %s is %s, want in_progress", active, st)
	}
	if st := s.get(done).Status; st != "done" {
		t.Fatalf("setup: %s is %s, want done", done, st)
	}

	root := realDir(t, t.TempDir())
	// worktree adds a worktree on a new branch from origin/main, commits one
	// file on it and pushes it, so it is clean and its HEAD is on a remote.
	worktree := func(branch string) string {
		dir := filepath.Join(root, branch)
		s.git("worktree", "add", "-q", "-b", branch, dir, "origin/main")
		s.at(dir, func() {
			writeFile(t, filepath.Join(dir, branch+".txt"), branch+"\n", 0o644)
			s.git("add", "-A")
			s.git("commit", "-q", "-m", "work on "+branch)
			s.git("push", "-q", "origin", branch)
		})
		return dir
	}
	activeWt := worktree(active)
	doneWt := worktree(done)
	plainWt := worktree("feature-x")
	ghostWt := worktree("sto_0badc0de")
	dirtyWt := worktree("dirty-work")
	writeFile(t, filepath.Join(dirtyWt, "scratch.txt"), "not committed\n", 0o644)

	r := s.exec("bash", "", script)
	if r.code != 0 {
		t.Fatalf("clear-worktrees.sh: exit %d\n%s%s", r.code, r.out, r.err)
	}
	out := r.out
	for _, want := range []string{
		"worktrees before: 6",
		"kept " + activeWt + " (" + active + "): item " + active + " is in_progress, not terminal",
		"removed " + doneWt + " (" + done + ")",
		"removed " + plainWt + " (feature-x)",
		"removed " + ghostWt + " (sto_0badc0de)",
		"kept " + dirtyWt + " (dirty-work): dirty:",
		"worktrees after: 3",
		"main checkout: main is merged into origin/main",
		"## main...origin/main",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s%s", want, out, r.err)
		}
	}

	for dir, kept := range map[string]bool{activeWt: true, dirtyWt: true, doneWt: false, plainWt: false, ghostWt: false} {
		_, err := os.Stat(dir)
		if kept && err != nil {
			t.Errorf("%s was removed, want kept: %v", dir, err)
		}
		if !kept && err == nil {
			t.Errorf("%s is still there, want removed", dir)
		}
	}
	branches := s.git("branch", "--format=%(refname:short)")
	for b, kept := range map[string]bool{active: true, "dirty-work": true, done: false, "feature-x": false, "sto_0badc0de": false} {
		has := strings.Contains("\n"+branches, "\n"+b+"\n")
		if has != kept {
			t.Errorf("local branch %s present=%v, want %v\n%s", b, has, kept, branches)
		}
	}
	if head, want := s.rev("HEAD"), s.rev("origin/main"); head != want {
		t.Errorf("main checkout HEAD %s, want origin/main %s", head, want)
	}
}
