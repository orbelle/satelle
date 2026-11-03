//go:build clislow

package cli

import (
	"strings"
	"testing"
	"time"
)

// A local status ahead of hosted is recorded as a conflict and the local copy is
// left as it was; pushing the local copy makes both identical and the next pass
// clears the record (sty_6e5fc41c).
func TestSlowSnapshotRecordsALocalAheadConflictAndClearsItOnceIdentical(t *testing.T) {
	fx := newHoldFixture(t, holdNoGit)
	id := createStoryID(t, "diverges")
	fx.push(t)
	exec, _ := executorStateFor(t, id)
	time.Sleep(1100 * time.Millisecond) // time-subject: the local row must be a second newer than the hosted one
	putInState(t, id, exec, "", "", false)

	snapshotIn(t, fx)

	c, ok := fx.conflicts(t)[id]
	if !ok || c.Reason != conflictLocalAhead {
		t.Fatalf("conflicts = %+v, want %s recorded as %q", fx.conflicts(t), id, conflictLocalAhead)
	}
	if got := fx.status(t, id); got != exec {
		t.Fatalf("recording the conflict changed the local status to %q, want %q", got, exec)
	}

	fx.push(t)
	snapshotIn(t, fx)

	if _, ok := fx.conflicts(t)[id]; ok {
		t.Errorf("a conflict is still recorded once both copies are identical: %+v", fx.conflicts(t))
	}
	if got := fx.status(t, id); got != exec {
		t.Errorf("local status = %q after the clearing pass, want %q", got, exec)
	}
}

// A pull refused with the work-state pull conflict records it too.
func TestSlowPullRefusedForADifferingStoryRecordsAConflict(t *testing.T) {
	fx := newHoldFixture(t, holdNoGit)
	id := createStoryID(t, "refused")
	fx.push(t)
	exec, _ := executorStateFor(t, id)
	time.Sleep(1100 * time.Millisecond) // time-subject: the local row must be a second newer than the hosted one
	putInState(t, id, exec, "", "", false)

	out, err := runRoot(t, "sync", "workstate", "pull", "--server", fx.ts.URL)
	if err == nil || !strings.Contains(err.Error(), "pull conflict") {
		t.Fatalf("pull = %v\n%s, want the work-state pull conflict", err, out)
	}

	if c := fx.conflicts(t)[id]; c.Reason != conflictPullRefused {
		t.Fatalf("conflicts = %+v, want %s recorded as %q", fx.conflicts(t), id, conflictPullRefused)
	}
	if got := fx.status(t, id); got != exec {
		t.Errorf("a refused pull changed the local status to %q", got)
	}
}
