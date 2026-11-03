package cli

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// seatHeldBySessionA leaves the repo's story seat live and held by sess-A, so a
// Bash event from sess-B is attributed to a foreign seat holder.
func seatHeldBySessionA(t *testing.T) {
	t.Helper()
	repo, storyID := liveSeatRepo(t)
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	_ = db.Leases.ForceRelease(ctx, storyID)
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: storyID, Kind: "story", Owner: lease.ResolveOwner(),
		State: "in_progress", StorySeat: true, SessionID: "sess-A",
		Worktree: repo,
	}); err != nil {
		t.Fatal(err)
	}
}

func sessionBBashEvent(cmd string) string {
	return fmt.Sprintf(`{"session_id":"sess-B","tool_input":{"command":%q}}`, cmd)
}

// performingStorySeatless adds a second performing story with no seat, so the
// repo has two performing stories and a session that holds neither.
func performingStorySeatless(t *testing.T) {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Stories.Create(context.Background(), workitem.CreateInput{
		Kind: workitem.KindStory, Title: "second performing", Body: "goal",
		AcceptanceCriteria: "1. ok", Status: "in_progress", Category: "chore",
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// bashSeatState is one performing-story/seat setup and the deny text a repo-tree
// write gets under it.
type bashSeatState struct {
	name     string
	setup    func(t *testing.T)
	wantDeny string
}

// bashSeatStateSetups are the performing-story/seat counts a Bash write is judged
// under, each producing its own deny text.
func bashSeatStateSetups() []bashSeatState {
	return []bashSeatState{
		{
			name:     "no performing story, no seat",
			setup:    func(t *testing.T) { t.Chdir(tempRepo(t)) },
			wantDeny: noEngagedStoryEditReason,
		},
		{
			name:     "one performing story, live seat held by another session",
			setup:    seatHeldBySessionA,
			wantDeny: "holds the live engagement seat",
		},
		{
			name: "two performing stories, none holds a seat for this session",
			setup: func(t *testing.T) {
				seatHeldBySessionA(t)
				performingStorySeatless(t)
			},
			wantDeny: "more than one performing story, none holds a seat for session sess-B",
		},
	}
}

// checkBashWritesAnySeatState runs each command through both Bash hooks under
// every setup: the allowed ones must pass, the denied ones must be refused with
// that setup's reason.
func checkBashWritesAnySeatState(t *testing.T, setups []bashSeatState, allowed, denied []string) {
	t.Helper()
	for _, s := range setups {
		t.Run(s.name, func(t *testing.T) {
			s.setup(t)
			for _, c := range allowed {
				for _, sub := range []string{"gate", "commitgate"} {
					if out, err := runRootIn(t, sessionBBashEvent(c), "hook", sub); err != nil {
						t.Errorf("hook %s denied scratch write %q: %v\n%s", sub, c, err, out)
					}
				}
			}
			for _, c := range denied {
				for _, sub := range []string{"gate", "commitgate"} {
					out, err := runRootIn(t, sessionBBashEvent(c), "hook", sub)
					if err == nil {
						t.Errorf("hook %s allowed repo write %q:\n%s", sub, c, out)
						continue
					}
					if !denyReasonContains(out, s.wantDeny) {
						t.Errorf("hook %s deny of %q lacks %q:\n%s", sub, c, s.wantDeny, out)
					}
				}
			}
		})
	}
}
