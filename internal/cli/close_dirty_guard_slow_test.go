//go:build clislow

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/lease"
)

// AC1, AC3, AC6: a close with a gated uncommitted file is refused with the file
// named, nothing changes and no gate runs; after the commit the same close is
// accepted and releases the seat. The route declares no gate of its own, so the
// refusal does not lean on one.
func TestSlowTerminalCloseRefusedWhileGatedChangesUncommitted(t *testing.T) {
	e := closeGuardEnv(t)
	id := e.engage()
	e.gater.calls.Store(0)
	// A root-level file: porcelain reports an untracked directory as `dir/`.
	e.write("foo.go", "package foo\n")

	err := e.set(id, "done")
	if err == nil {
		t.Fatal("close with an uncommitted gated file must be refused")
	}
	for _, want := range []string{"foo.go", "commit or discard"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q, got: %v", want, err)
		}
	}
	if got := e.status(id); got != "in_progress" {
		t.Errorf("status after a refused close = %q, want in_progress", got)
	}
	if !e.seatHeld(id) {
		t.Error("a refused close must leave the seat held")
	}
	if n := e.gater.calls.Load(); n != 0 {
		t.Errorf("a refused close must dispatch no gate, ran %d", n)
	}

	e.git("add", "foo.go")
	e.git("commit", "-qm", "slice")
	if err := e.set(id, "done"); err != nil {
		t.Fatalf("close after the commit: %v", err)
	}
	if got := e.status(id); got != "done" {
		t.Errorf("status = %q, want done", got)
	}
	if e.seatHeld(id) {
		t.Error("the accepted close must release the seat")
	}
	if n := e.gater.calls.Load(); n != 1 {
		t.Errorf("the accepted close runs the gate once, ran %d", n)
	}
}

// AC1: a refused close dispatches no gate. The counting gater in the other
// tests cannot tell a skipped gate from an absent one, so this route gates
// the close with a check that writes a marker file; the real command tree runs
// it. The marker is the control: absent after the refusal, present after the
// commit lets the same close through to the gate.
func TestSlowRefusedTerminalCloseRunsNoGate(t *testing.T) {
	repo := tempRepo(t)
	t.Chdir(repo)
	marker := filepath.Join(t.TempDir(), "gate-ran")
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"), closeRouteDone,
		strings.Replace(closeRouteStep, `requires = ["coded"]`, `requires = ["coded"]
reviewers = ["close-marker-check"]`, 1))
	writeFile(t, repo, ".satelle/skills/close-marker-check.md",
		"---\nname: close-marker-check\ntype: skill\ndescription: records that the close gate ran\n---\n\n```check\n"+
			"touch '"+marker+"'\n```\n")
	gitInitCommit(t, repo)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}

	out, err := runRoot(t, "story", "create", "--title", "gated close", "--body", "goal",
		"--acceptance", "1. ok", "--category", "chore")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	id := idOf(t, out)
	if out, err := runRoot(t, "story", "set", id, "--status", "in_progress"); err != nil {
		t.Fatalf("engage: %v\n%s", err, out)
	}
	writeRepoFile(t, repo, "foo.go", "package foo\n")

	if out, err := runRoot(t, "story", "set", id, "--status", "done"); err == nil {
		t.Fatalf("close must be refused:\n%s", out)
	} else if !strings.Contains(err.Error(), "foo.go") {
		t.Errorf("refusal must name foo.go, got: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a refused close must not run the gate, but its check ran")
	}

	gitIn(t, repo, "add", "-A") // the command tree also installs hook files
	gitIn(t, repo, "commit", "-qm", "slice")
	if out, err := runRoot(t, "story", "set", id, "--status", "done"); err != nil {
		t.Fatalf("close after the commit: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the accepted close must run the gate (control), marker: %v", err)
	}
}

// AC2: a clean tree, and a tree dirty only on edit-exempt paths, close as today.
func TestSlowTerminalCloseUnchangedWhenCleanOrExemptOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty func(e *closeEnv)
	}{
		{"clean", func(*closeEnv) {}},
		{"exempt_only", func(e *closeEnv) { e.write(".satelle/notes.md", "scratch\n") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := closeGuardEnv(t)
			id := e.engage()
			tc.dirty(e)
			if err := e.set(id, "done"); err != nil {
				t.Fatalf("close: %v", err)
			}
			if got := e.status(id); got != "done" {
				t.Errorf("status = %q, want done", got)
			}
			if e.seatHeld(id) {
				t.Error("the close must release the seat")
			}
		})
	}
}

// AC4: a park, a non-exit move and a failing probe are not refused on a dirty
// tree.
func TestSlowDirtyCloseGuardLeavesParkNonExitAndProbeErrorAlone(t *testing.T) {
	t.Run("park", func(t *testing.T) {
		e := closeGuardEnv(t)
		id := e.engage()
		e.write("foo.go", "package foo\n")
		if err := e.set(id, "blocked"); err != nil {
			t.Fatalf("park on a dirty tree: %v", err)
		}
		if got := e.status(id); got != "blocked" {
			t.Errorf("status = %q, want blocked", got)
		}
	})

	// The fixture route has no plan state, so the non-exit move is the engaging
	// backlog → in_progress.
	t.Run("non_exit", func(t *testing.T) {
		e := closeGuardEnv(t)
		id := e.story("non exit")
		e.write("foo.go", "package foo\n")
		if err := e.set(id, "in_progress"); err != nil {
			t.Fatalf("non-exit move on a dirty tree: %v", err)
		}
		if got := e.status(id); got != "in_progress" {
			t.Errorf("status = %q, want in_progress", got)
		}
	})

	t.Run("probe_error", func(t *testing.T) {
		e := closeGuardEnv(t)
		id := e.engage()
		e.write("foo.go", "package foo\n")
		e.probeWith(func(context.Context, string) ([]string, error) {
			return nil, errors.New("git unresolvable")
		})
		if err := e.set(id, "done"); err != nil {
			t.Fatalf("a failing probe must fail open: %v", err)
		}
		if got := e.status(id); got != "done" {
			t.Errorf("status = %q, want done", got)
		}
	})

	t.Run("git_error", func(t *testing.T) {
		e := closeGuardEnv(t)
		id := e.engage()
		e.write("foo.go", "package foo\n")
		if err := os.WriteFile(filepath.Join(e.repo, ".git", "index"), []byte("not an index"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := e.set(id, "done"); err != nil {
			t.Fatalf("an unreadable git index must fail open: %v", err)
		}
	})
}

// AC5: a story with no live seat of its own names no paths, even while another
// story's seat sits on a dirty tree in the same checkout.
func TestSlowSeatlessTerminalMoveIgnoresAnotherStoriesDirtyTree(t *testing.T) {
	e := closeGuardEnv(t)
	// B is engaged and then loses its seat, so the terminal move below is one
	// the guard classifies as a close, made by a story with no seat of its own.
	// A repo-root or other-seat fallback would refuse it on A's dirty file.
	b := e.engage()
	if err := e.db.Leases.ForceRelease(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	a := e.engage()
	e.write("foo.go", "package foo\n")
	before, err := e.db.Leases.Get(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}

	if err := e.set(b, "done"); err != nil {
		t.Fatalf("closing a story with no seat of its own: %v", err)
	}
	if got := e.status(b); got != "done" {
		t.Errorf("status = %q, want done", got)
	}
	after, err := e.db.Leases.Get(context.Background(), a)
	if err != nil {
		t.Fatalf("A's seat must be untouched: %v", err)
	}
	if after.Worktree != before.Worktree || !lease.Alive(after, time.Now()) {
		t.Errorf("A's seat changed: %+v -> %+v", before, after)
	}
}
