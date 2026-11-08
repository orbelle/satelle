package gitfacts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func TestFacts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "one")
	r := Repo{Dir: dir}
	head, err := r.Head()
	if err != nil || len(head) != 40 {
		t.Fatalf("head %q %v", head, err)
	}
	top, _ := r.TopLevel()
	if want, _ := filepath.EvalSymlinks(dir); top != want {
		t.Fatalf("toplevel %q want %q", top, want)
	}
	if dirty, _ := r.Dirty(); dirty {
		t.Fatal("clean tree reported dirty")
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("2\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "d"), 0o755)
	os.WriteFile(filepath.Join(dir, "d", "b.go"), []byte("x"), 0o644)
	paths, err := r.ChangedPaths(head)
	if err != nil || strings.Join(paths, ",") != "a.txt,d/b.go" {
		t.Fatalf("changed %v %v", paths, err)
	}
	diff, _ := r.Diff(head, []string{"a.txt"})
	if !strings.Contains(diff, "+2") {
		t.Fatalf("diff %q", diff)
	}
	if dirty, _ := r.Dirty(); !dirty {
		t.Fatal("dirty tree reported clean")
	}
}

// IsAncestor: a parent and the commit itself are in its history; a commit a
// rebase rewrote is not; an unknown commit is an error.
func TestIsAncestor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	commit := func(name string) string {
		os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644)
		gittest.Git(t, dir, "add", ".")
		gittest.Git(t, dir, "commit", "-qm", name)
		h, _ := Repo{Dir: dir}.Head()
		return h
	}
	r := Repo{Dir: dir}
	root := commit("a")
	gittest.Git(t, dir, "checkout", "-q", "-b", "side")
	side := commit("s")
	gittest.Git(t, dir, "checkout", "-q", "main")
	gittest.Git(t, dir, "checkout", "-q", "-b", "feat")
	feat := commit("f")
	for _, c := range []struct {
		a, b string
		want bool
	}{{root, feat, true}, {feat, feat, true}, {side, feat, false}} {
		if got, err := r.IsAncestor(c.a, c.b); err != nil || got != c.want {
			t.Errorf("IsAncestor(%.7s, %.7s) = %v, %v; want %v", c.a, c.b, got, err, c.want)
		}
	}
	gittest.Git(t, dir, "rebase", "-q", "side")
	head, _ := r.Head()
	if in, err := r.IsAncestor(feat, head); err != nil || in {
		t.Errorf("the pre-rebase commit is in the rebased history: %v %v", in, err)
	}
	if _, err := r.IsAncestor(strings.Repeat("0", 40), head); err == nil {
		t.Error("an unknown commit is not an error")
	}
}

// Branch is the checkout's current branch, "" on a detached HEAD.
func TestBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "trunk")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "one")
	r := Repo{Dir: dir}
	if b, err := r.Branch(); err != nil || b != "trunk" {
		t.Fatalf("on a branch: %q %v", b, err)
	}
	head, _ := r.Head()
	gittest.Git(t, dir, "checkout", "-q", "--detach", head)
	if b, err := r.Branch(); err != nil || b != "" {
		t.Fatalf("detached: %q %v", b, err)
	}
}

// On a clean tree the working-tree diff from a base is byte-equal to the diff
// between the base and HEAD: a change a judge saw in a tree hashes the same
// when it is later read from the branch's commits.
func TestCleanTreeDiffEqualsDiffBetweenHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "one")
	r := Repo{Dir: dir}
	base, _ := r.Head()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("2\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "d"), 0o755)
	os.WriteFile(filepath.Join(dir, "d", "b.go"), []byte("package d\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "two")
	head, _ := r.Head()
	for _, paths := range [][]string{nil, {"d/b.go"}} {
		tree, err := r.Diff(base, paths)
		if err != nil {
			t.Fatal(err)
		}
		between, err := r.DiffBetween(base, head, paths)
		if err != nil {
			t.Fatal(err)
		}
		if tree == "" || tree != between {
			t.Fatalf("paths %v: tree diff\n%s\n!= diff between base and HEAD\n%s", paths, tree, between)
		}
	}
}
