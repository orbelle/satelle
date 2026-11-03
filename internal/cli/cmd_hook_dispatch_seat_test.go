package cli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// inFlightSeat opens a seat mid-transition and leaves it there: the dispatch
// window in which the story's committed status is still the source status.
func inFlightSeat(t *testing.T, db *store.DB, it workitem.Item, target, worktree, session string) {
	t.Helper()
	claimEpicSeat(t, db, it, target, worktree, session)
}

// settledEpicSeat gives a sibling a committed seat co-held under the epic's key.
func settledEpicSeat(t *testing.T, db *store.DB, it workitem.Item, status, worktree, session string) {
	t.Helper()
	claimEpicSeat(t, db, it, status, worktree, session)
	if err := db.Leases.Confirm(context.Background(), it.ID, status); err != nil {
		t.Fatal(err)
	}
}

// claimEpicSeat acquires a seat under one shared arbitration key, the way epic
// seat mode co-holds several siblings in distinct working trees. It opens
// in flight; the caller settles it or leaves it there.
func claimEpicSeat(t *testing.T, db *store.DB, it workitem.Item, state, worktree, session string) {
	t.Helper()
	l, out, _, err := db.Leases.AcquireWith(context.Background(), lease.AcquireOpts{
		ItemID: it.ID, Kind: "story", Owner: "alice", State: state, SeatKey: "epic:shared",
		StorySeat: true, Worktree: worktree, SessionID: session,
	})
	if err != nil || out != lease.OutcomeAcquired {
		t.Fatalf("acquire %s: outcome=%v err=%v lease=%+v", it.ID, out, err, l)
	}
}

func setDispatch(t *testing.T, agent, step, item string) {
	t.Helper()
	t.Setenv(config.DispatchAgentEnv, agent)
	t.Setenv(config.DispatchStepEnv, step)
	t.Setenv(config.DispatchItemEnv, item)
}

// TestDispatchedPerformerRefusedOutsideItsDispatch (sty_8d7d1c45 AC3): selecting
// the performer's seat grants nothing beyond the route's allocation for it.
func TestDispatchedPerformerRefusedOutsideItsDispatch(t *testing.T) {
	db, create := attributionRepo(t)
	stubWorktree(t, "/w/driver")
	epic := create("epic", "ready")
	sibling := create("sibling", "in_progress")
	settledEpicSeat(t, db, sibling, "in_progress", "/w/sib", "sess-driver")
	inFlightSeat(t, db, epic, "in_progress", "/w/epic", "sess-driver")
	now := time.Now().UTC()

	check := func(t *testing.T, agent, step, item string) string {
		t.Helper()
		setDispatch(t, agent, step, item)
		info, _, live, err := resolveSeats(false, "sess-driver")
		if err != nil {
			t.Fatal(err)
		}
		dm := currentDispatchMarker()
		if hookEditPermitted(info, dm, relayMarker{}) {
			t.Fatalf("%s/%s/%s must be refused (seat %q)", agent, step, item, info.ItemID)
		}
		return hookDenyReason(info, live, dm, relayMarker{}, "sess-driver", now)
	}

	t.Run("wrong agent for the step", func(t *testing.T) {
		check(t, "reviewer", "in_progress", epic.ID)
	})
	t.Run("the sibling's dispatch cannot ride the epic's lease", func(t *testing.T) {
		// The marker names the sibling, whose lease is settled: only the route's
		// committed-step allocation could permit it, and the step is not its status.
		check(t, "coder", "ready", sibling.ID)
	})
	t.Run("a dispatch whose item holds no live lease", func(t *testing.T) {
		got := check(t, "coder", "in_progress", "sty_ghost")
		for _, want := range []string{"sty_ghost", "in_progress", "no live in-flight lease"} {
			if !strings.Contains(got, want) {
				t.Errorf("deny must name the dispatch (%q): %s", want, got)
			}
		}
		if strings.Contains(got, epic.ID) || strings.Contains(got, sibling.ID) {
			t.Errorf("deny must not attribute the dispatch to another story: %s", got)
		}
	})
}

// TestDispatchedPerformerStillFencedOutsideTheRepo (sty_8d7d1c45 AC3): the
// foreign-tree fence runs before the seat is consulted, so a performer holding a
// valid in-flight seat is still denied a write into another git working tree.
func TestDispatchedPerformerStillFencedOutsideTheRepo(t *testing.T) {
	fenceTempRootsElsewhere(t)
	db, create := attributionRepo(t)
	epic := create("epic", "ready")
	inFlightSeat(t, db, epic, "in_progress", "", "")
	setDispatch(t, "coder", "in_progress", epic.ID)

	foreign := t.TempDir()
	if o, err := exec.Command("git", "-C", foreign, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, o)
	}
	out, err := runRootIn(t, editEvent(foreign+"/x.go"), "hook", "gate")
	if err == nil {
		t.Fatalf("a write into another working tree must be denied:\n%s", out)
	}
	if !strings.Contains(out, "allow_outside_tree_edits") {
		t.Errorf("the refusal should name the escape hatch: %s", out)
	}
}
