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

// diffRepo is a repo whose config sets cfg (key, value pairs), with two
// commits of a.txt: base, then head, which myers and histogram diff
// differently.
func diffRepo(t *testing.T, cfg ...string) (r Repo, base, head string) {
	t.Helper()
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	for i := 0; i+1 < len(cfg); i += 2 {
		gittest.Git(t, dir, "config", cfg[i], cfg[i+1])
	}
	r = Repo{Dir: dir}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\nb\nc\na\nb\nc\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "base")
	base, _ = r.Head()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("c\nb\na\nb\nc\na\n"), 0o644)
	gittest.Git(t, dir, "commit", "-qam", "head")
	head, _ = r.Head()
	return r, base, head
}

// A change's diff text is the same in its own clean tree and between its
// commits, and the same on every machine, whatever prefix or algorithm the
// user's diff config asks for: a change hashed where it was judged hashes the
// same once it is read as commits, or read elsewhere.
func TestDiffTextIgnoresTheUsersDiffConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	plain, pb, ph := diffRepo(t, "diff.algorithm", "myers")
	want, err := plain.DiffBetween(pb, ph, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(want, "--- a/a.txt\n+++ b/a.txt\n") {
		t.Fatalf("plain diff has no a/ b/ prefixes:\n%s", want)
	}
	for _, cfg := range [][]string{
		{"diff.mnemonicPrefix", "true"},
		{"diff.noprefix", "true"},
		{"diff.srcPrefix", "x/", "diff.dstPrefix", "y/"},
		{"diff.algorithm", "histogram"},
		{"diff.algorithm", "patience", "diff.mnemonicPrefix", "true"},
	} {
		r, base, head := diffRepo(t, cfg...)
		tree, err := r.Diff(base, nil)
		if err != nil {
			t.Fatal(err)
		}
		between, err := r.DiffBetween(base, head, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tree != want || between != want {
			t.Errorf("config %v:\ntree diff\n%s\ndiff between commits\n%s\nwant\n%s", cfg, tree, between, want)
		}
	}
}

// IgnoredBy names the rule hiding an untracked path, and reports neither an
// unignored path, a tracked one, nor one a `!` rule re-includes.
func TestIgnoredBy(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "hidden/\nkept/*\n!kept/back.json\ntracked/\n")
	for _, f := range []string{"hidden/a.json", "plain/a.json", "kept/back.json", "tracked/a.json"} {
		write(f, "{}\n")
	}
	gittest.Git(t, dir, "add", "-f", "tracked/a.json")
	got, err := Repo{Dir: dir}.IgnoredBy("hidden/a.json", "plain/a.json", "kept/back.json", "tracked/a.json", "absent/a.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["hidden/a.json"] != ".gitignore:1: hidden/" {
		t.Fatalf("ignored = %v, want only hidden/a.json by .gitignore:1: hidden/", got)
	}
	if got, err := (Repo{Dir: dir}).IgnoredBy(); err != nil || len(got) != 0 {
		t.Fatalf("no paths: %v %v", got, err)
	}
	if _, err := (Repo{Dir: t.TempDir()}).IgnoredBy("a"); err == nil {
		t.Fatal("outside a repository: want an error")
	}
}
