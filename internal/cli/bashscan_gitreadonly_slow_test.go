//go:build clislow

package cli

import (
	"strings"
	"testing"
)

// AC2: a shell mutation of a linked worktree is refused by the seat of THAT
// worktree, in both hooks and whichever order the store lists the seats in. The
// anchor's seat would allow it.
func TestSlowMultiSeatBashMutationDenyNamesTargetWorktreeSeat(t *testing.T) {
	for _, hook := range []string{"gate", "commitgate"} {
		bothOrders(t, func(t *testing.T, aFirst bool) {
			f := twoSeatsInLinkedWorktrees(t, "in_progress", "plan", aFirst)
			out, err := runRootIn(t, multiSeatBash("git -C "+f.wtB+" commit -m x"), "hook", hook)
			if err == nil {
				t.Fatalf("%s: B is at plan and holds the worktree, so the commit must be refused:\n%s", hook, out)
			}
			reason := denyReasonOf(t, out)
			if !strings.Contains(reason, f.b.ID) {
				t.Errorf("%s: the refusal must name B's seat %s: %s", hook, f.b.ID, reason)
			}
			if strings.Contains(reason, f.a.ID) {
				t.Errorf("%s: the refusal must not name the anchor's seat %s: %s", hook, f.a.ID, reason)
			}
		})
	}
}

// AC3 (in-process): with two live seats, both at a step the route allocates to a
// non-executor agent (plan, planner), a read-only git -C into the other worktree
// runs without a deny. This is the observed failure: before the fix the -C operand
// was a mutation target, so the command was judged by a seat that may not edit and
// denied; the same command spelled `git -C <wt> commit` still is, as the control.
func TestSlowMultiSeatGitReadOnlyIntoWorktreeNotDenied(t *testing.T) {
	for _, hook := range []string{"gate", "commitgate"} {
		bothOrders(t, func(t *testing.T, aFirst bool) {
			f := twoSeatsInLinkedWorktrees(t, "plan", "plan", aFirst)
			if out, err := runRootIn(t, multiSeatBash("git -C "+f.wtB+" diff main --stat"), "hook", hook); err != nil {
				t.Fatalf("%s: a read-only git -C into a sibling worktree must not be denied: %v\n%s", hook, err, out)
			}
			if out, err := runRootIn(t, multiSeatBash("git -C "+f.wtB+" commit -m x"), "hook", hook); err == nil {
				t.Fatalf("%s: control — a writing git -C into the same worktree must still be refused:\n%s", hook, out)
			}
		})
	}
}
