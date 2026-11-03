package cli

// sty_7f93ae45 — a task run re-engages its own execution with the command the
// edit gate's dropped-seat advice names, and the commit/push gate then attributes
// a push from that tree to the execution, never to a story the same run seated in
// the same tree earlier.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
)

const execRouteDone = `["*"]
obligations = ["raised", "planned", "coded", "closed"]
park = { state = "blocked" }

[execution]
obligations = ["raised", "run", "run-closed"]
`

const execRouteStep = `[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "executor"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]

[run]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[run-closed]
status = "done"
terminal = true
requires = ["run"]
`

const execRunSession = "release-run"

func execRunBash(command string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": execRunSession, "transcript_path": "/x/transcript.jsonl",
		"tool_name": "Bash", "tool_input": map[string]any{"command": command},
	})
	return string(b)
}

// reseatUnderEpicKey re-acquires id's seat, same session and tree, under an
// epic-shaped seat key. The store lists leases by seat key, and "epic-…" sorts
// ahead of the execution's own "exe_…" key, so a resolver that took the first
// same-tree seat would pick the story.
func reseatUnderEpicKey(t *testing.T, id, state, tree string) {
	t.Helper()
	forceRelease(t, id)
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: id, Kind: "story", Owner: lease.ResolveOwner(), State: state, StorySeat: true,
		SeatKey: "epic-parent", Worktree: tree, SessionID: execRunSession,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Leases.Confirm(ctx, id, state); err != nil {
		t.Fatal(err)
	}
}

// AC3: the dropped-seat advice names `satelle story set <id> --status <s>` for an
// execution too — the command TestSlowReengagedExecutionPushAttributedToItOverASameTreeStory
// proves seats it.
func TestDroppedSeatAdviceNamesStorySetForAnExecution(t *testing.T) {
	reason := droppedSeatEditReason("exe_695726be", "in_progress")
	if !strings.Contains(reason, "satelle story set exe_695726be --status in_progress") {
		t.Fatalf("advice must name the re-engage command for the execution: %s", reason)
	}
}

// AC2: one session holding two seats in one tree attributes to the one acquired
// last; an exact tie on AcquiredAt is no pick, whatever the store order.
func TestPickSessionSeatForSameTreeLatestAcquired(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	story := seatInfo{ItemID: "sty_x", SessionID: "sess", Worktree: "/w/main", AcquiredAt: t0}
	exe := seatInfo{ItemID: "exe_y", SessionID: "sess", Worktree: "/w/main", AcquiredAt: t0.Add(time.Second)}
	stubWorktree(t, "/w/main")
	for _, live := range [][]seatInfo{{story, exe}, {exe, story}} {
		for _, target := range []string{"/w/main", ""} {
			if got, mine := pickSessionSeatFor(live, nil, "sess", target); got.ItemID != "exe_y" || !mine {
				t.Errorf("target %q: got %q mine=%v, want the later-acquired exe_y", target, got.ItemID, mine)
			}
		}
	}
	tie := exe
	tie.AcquiredAt = t0
	for _, live := range [][]seatInfo{{story, tie}, {tie, story}} {
		if got, _ := pickSessionSeatFor(live, nil, "sess", "/w/main"); got.ItemID != "" {
			t.Errorf("an exact AcquiredAt tie must pick none, got %q", got.ItemID)
		}
	}
}
