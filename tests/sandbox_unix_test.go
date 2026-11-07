//go:build integration && !windows

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A command killed by a signal is recorded as signaled, and its failure
// message names the signal and the empty stderr: the shape of the reported
// setup flake, which used to print as `git [commit ...]: `.
func TestSandboxGitFailureReportsSignal(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	r := s.exec("sh", "", "-c", "kill -9 $$")
	if !r.signaled || r.state != "signal: killed" {
		t.Fatalf("want signaled, signal: killed; got %+v", r)
	}
	msg := gitFailure([]string{"commit"}, r)
	for _, want := range []string{"signal: killed", "stderr: (empty)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("gitFailure missing %q:\n%s", want, msg)
		}
	}
}

// killingGit writes a git wrapper that SIGKILLs itself on its first kills
// `commit` runs (counted in a marker dir) and otherwise runs the real git.
func killingGit(t *testing.T, kills int) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marks := filepath.Join(dir, "marks")
	if err := os.Mkdir(marks, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = commit ] && [ $(ls '" + marks + "' | wc -l) -lt " + strconv.Itoa(kills) + " ]; then\n" +
		"  mktemp -p '" + marks + "' >/dev/null\n" +
		"  kill -9 $$\n" +
		"fi\n" +
		"exec '" + real + "' \"$@\"\n"
	p := filepath.Join(dir, "git")
	writeFile(t, p, script, 0o755)
	return p
}

// Repo setup whose commit is killed by a signal on all but the last attempt
// is retried on a fresh .git and ends with a committed repo.
func TestSandboxSetupRetriesKilledGit(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	if err := os.RemoveAll(filepath.Join(s.dir, ".git")); err != nil {
		t.Fatal(err)
	}
	s.initRepo(killingGit(t, setupAttempts-1))
	if got := strings.TrimSpace(s.git("log", "--format=%s")); got != "init" {
		t.Fatalf("log after retried setup = %q, want init", got)
	}
}
