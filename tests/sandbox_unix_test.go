//go:build integration && !windows

package tests

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

// An executable written through writeFile can be exec'd at once while other
// goroutines fork continuously: no forked child inherits its write fd, which
// would make the exec fail with ETXTBSY (golang/go#22315).
func TestWriteFileExecUnderForkLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = exec.Command("true").Run()
			}
		}()
	}
	defer func() {
		close(stop)
		wg.Wait()
	}()
	for i := range 300 {
		p := filepath.Join(dir, fmt.Sprintf("fake%d", i))
		writeFile(t, p, "#!/bin/sh\nexit 0\n", 0o755)
		if err := exec.Command(p).Run(); err != nil {
			if errors.Is(err, syscall.ETXTBSY) {
				t.Fatalf("exec %s: %v (golang/go#22315)", p, err)
			}
			t.Fatalf("exec %s: %v", p, err)
		}
	}
}
