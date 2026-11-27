//go:build unix

package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func TestWriteRepoPathMarkerUsesTheGivenEnv(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, repo, "init", "-q")
	gittest.Git(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	if err := os.MkdirAll(filepath.Join(repo, DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gittest.Git(t, repo, "worktree", "add", "-q", linked)

	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "git.env")
	script := "#!/bin/sh\nenv >> '" + log + "'\necho --- >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_PROBE_VALUE", "s3cret")
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GIT_MARKER_PROBE=given"}
	for name, root := range map[string]string{"main checkout": repo, "linked worktree": linked} {
		want, err := filepath.EvalSymlinks(CanonicalRepoRoot(root))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(log); err != nil {
			t.Fatal(err)
		}
		runtime := filepath.Join(t.TempDir(), "repo-0123abcd")
		if err := WriteRepoPathMarker(runtime, root, env); err != nil {
			t.Fatal(err)
		}
		if got := ReadRepoPathMarker(runtime); got != want {
			t.Errorf("%s: repo.path = %q, want %q as CanonicalRepoRoot gives", name, got, want)
		}
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("%s: git did not run with the given env: %v", name, err)
		}
		if strings.Contains(string(b), "s3cret") || !strings.Contains(string(b), "GIT_MARKER_PROBE=given") {
			t.Fatalf("%s: git ran with another env than the one given:\n%s", name, b)
		}
	}
}
