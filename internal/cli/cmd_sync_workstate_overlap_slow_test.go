//go:build clislow

package cli

import (
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func TestSlowSnapshotConflictRecordsTheOverlapInSyncStateNotOnTheChain(t *testing.T) {
	fx := newHoldFixture(t, holdNoGit)
	id := createStoryID(t, "diverges")
	fx.push(t)
	exec, _ := executorStateFor(t, id)
	time.Sleep(1100 * time.Millisecond) // time-subject: the local row must be a second newer than the hosted one
	putInState(t, id, exec, "", "", false)
	before := passChain(t, id)

	snapshotIn(t, fx)
	c, ok := fx.conflicts(t)[id]
	if !ok {
		t.Fatalf("precondition: a conflict is recorded, got %+v", fx.conflicts(t))
	}
	if c.Source != "conflict" || c.RemoteHolder != "unknown" || c.Reason != conflictLocalAhead || c.LocalHolder != fx.loc {
		t.Errorf("conflict = %+v, want source conflict, remote unknown (no other location worked it), reason %q, local %s", c, conflictLocalAhead, fx.loc)
	}
	sameEvents(t, "the snapshot", before, passChain(t, id))

	snapshotIn(t, fx) // the conflict is still there: still nothing on the chain
	sameEvents(t, "a repeated snapshot", before, passChain(t, id))
	if again := fx.conflicts(t)[id]; !again.At.Equal(c.At) {
		t.Errorf("a repeated pass moved the conflict's time from %v to %v", c.At, again.At)
	}
	fx.push(t)
	if n := fx.f.ledgerKindCount("probe", id, ledger.KindOverlap); n != 0 {
		t.Errorf("overlap rows on the server = %d, want none", n)
	}
}

func TestSlowPullConflictNamesTheOtherLocationThatWorkedIt(t *testing.T) {
	fx := newHoldFixture(t, holdNoGit)
	id := createStoryID(t, "refused, worked elsewhere")
	fx.push(t)
	exec, _ := executorStateFor(t, id)
	time.Sleep(1100 * time.Millisecond) // time-subject: the local row must be a second newer than the hosted one
	putInState(t, id, exec, "", "", false)
	workedElsewhere(t, id, otherLoc, "desk")
	before := passChain(t, id)

	if _, err := runRoot(t, "sync", "workstate", "pull", "--server", fx.ts.URL); err == nil {
		t.Fatal("precondition: the pull is refused for the differing story")
	}
	if c := fx.conflicts(t)[id]; c.Source != "conflict" || c.RemoteHolder != "desk" || c.LocalHolder != fx.loc {
		t.Errorf("conflict = %+v, want one naming desk, the location that last worked it", c)
	}
	sameEvents(t, "the refused pull", before, passChain(t, id))
}
