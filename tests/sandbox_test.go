//go:build integration

package tests

import (
	"strings"
	"testing"
)

func TestSandboxGitFailureReportsStatusAndStderr(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	args := []string{"rev-parse", "--verify", "refs/heads/no-such-branch"}
	r := s.exec("git", "", args...)
	if r.code == 0 || r.signaled {
		t.Fatalf("want a non-zero exit, not a signal: %+v", r)
	}
	if r.err == "" {
		t.Fatalf("git printed no stderr: %+v", r)
	}
	msg := gitFailure(args, r)
	for _, want := range []string{"exit status ", strings.TrimSpace(r.err)} {
		if !strings.Contains(msg, want) {
			t.Errorf("gitFailure missing %q:\n%s", want, msg)
		}
	}
}

func TestSandboxGitRunsNoAutomaticGc(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	if got := strings.TrimSpace(s.git("config", "--get", "gc.auto")); got != "0" {
		t.Errorf("sandbox gc.auto = %q, want 0", got)
	}
	if got := strings.TrimSpace(s.git("config", "--type=bool", "--get", "maintenance.auto")); got != "false" {
		t.Errorf("sandbox maintenance.auto = %q, want false", got)
	}
}
