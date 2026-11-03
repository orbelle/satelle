//go:build clislow

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// AC1: from the main tree, with the default fence, an Edit into each linked
// worktree is judged against that worktree's seat.
func TestSlowMultiSeatLinkedEditJudgedAgainstWorktreeSeat(t *testing.T) {
	bothOrders(t, func(t *testing.T, xFirst bool) {
		l := multiSeatLinked(t, "plan", "in_progress", xFirst, false, "")
		if out, err := runRootIn(t, multiSeatEdit(filepath.Join(l.wtY, "a.go")), "hook", "gate"); err != nil {
			t.Fatalf("Y is at in_progress and holds the target tree, so the edit is allowed: %v\n%s", err, out)
		}
		out, err := runRootIn(t, multiSeatEdit(filepath.Join(l.wtX, "a.go")), "hook", "gate")
		if err == nil {
			t.Fatalf("X is at plan and holds the target tree, so the edit is refused:\n%s", out)
		}
		if reason := denyReasonOf(t, out); !strings.Contains(reason, l.x.ID) || strings.Contains(reason, l.y.ID) {
			t.Errorf("the refusal must be X's alone: %s", reason)
		}
	})
	bothOrders(t, func(t *testing.T, xFirst bool) {
		l := multiSeatLinked(t, "in_progress", "plan", xFirst, false, "")
		if out, err := runRootIn(t, multiSeatEdit(filepath.Join(l.wtX, "a.go")), "hook", "gate"); err != nil {
			t.Fatalf("X is at in_progress and holds the target tree, so the edit is allowed: %v\n%s", err, out)
		}
		if out, err := runRootIn(t, multiSeatEdit(filepath.Join(l.wtY, "a.go")), "hook", "gate"); err == nil {
			t.Fatalf("Y is at plan and holds the target tree, so the edit is refused:\n%s", out)
		}
	})
}

// AC1: an unmoved push from the main tree is judged against the seat of the
// worktree that has the pushed branch checked out.
func TestSlowMultiSeatLinkedPushAttributedByBranch(t *testing.T) {
	bothOrders(t, func(t *testing.T, xFirst bool) {
		l := multiSeatLinked(t, "plan", "in_progress", xFirst, false, pushInProgressOnly)
		for _, command := range []string{"git push origin feat/y", "git push -u origin feat/y:feat/y", "git push origin +refs/heads/feat/y"} {
			if out, err := runRootIn(t, multiSeatBash(command), "hook", "commitgate"); err != nil {
				t.Fatalf("%q: feat/y is checked out in Y's worktree and Y is at in_progress: %v\n%s", command, err, out)
			}
		}
		for _, command := range []string{"git push origin feat/x", "git push -u origin feat/x:feat/x"} {
			out, err := runRootIn(t, multiSeatBash(command), "hook", "commitgate")
			if err == nil {
				t.Fatalf("%q: feat/x is checked out in X's worktree and X is at plan:\n%s", command, out)
			}
			if reason := denyReasonOf(t, out); !strings.Contains(reason, l.x.ID) || strings.Contains(reason, l.y.ID) {
				t.Errorf("%q: the refusal must be X's alone: %s", command, reason)
			}
		}
	})
}

// AC2: what targets no seat's worktree keeps today's refusal.
func TestSlowMultiSeatLinkedUnattributableStillRefused(t *testing.T) {
	bothOrders(t, func(t *testing.T, xFirst bool) {
		l := multiSeatLinked(t, "in_progress", "in_progress", xFirst, false, pushInProgressOnly)
		type event struct{ label, command, edit string }
		for _, ev := range []event{
			{"unmoved commit", "git commit -m x", ""},
			{"push of a branch no worktree has", "git push origin no-such-branch", ""},
			{"push of the main tree's branch", "git push origin main", ""},
			{"bare push", "git push", ""},
			{"push of HEAD", "git push origin HEAD", ""},
			{"push of two trees' branches", "git push origin feat/x feat/y", ""},
			{"unmoved commit then push of a seat's branch", "git commit -m x && git push origin feat/y", ""},
			{"shell mutation in the main tree", "echo x > " + filepath.Join(l.repo, "internal", "foo.go"), ""},
			{"edit in the main tree", "", filepath.Join(l.repo, "internal", "foo.go")},
		} {
			var out string
			var err error
			if ev.edit != "" {
				out, err = runRootIn(t, multiSeatEdit(ev.edit), "hook", "gate")
			} else {
				out, err = runRootIn(t, multiSeatBash(ev.command), "hook", "commitgate")
			}
			if err == nil {
				t.Fatalf("%s must be refused:\n%s", ev.label, out)
			}
			reason := denyReasonOf(t, out)
			for _, want := range []string{"none is in the tree this event runs in", l.x.ID, l.y.ID} {
				if !strings.Contains(reason, want) {
					t.Errorf("%s: refusal missing %q: %s", ev.label, want, reason)
				}
			}
		}
	})
}
