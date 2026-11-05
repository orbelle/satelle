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
