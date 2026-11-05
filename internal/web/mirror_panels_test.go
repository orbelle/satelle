package web

import (
	"testing"
)

// TestEngagedStorySeatIDsPredicate (sty_01ba9482): engagement counts non-stale
// story_seat only — not task seats, not stale rows, not missing story_seat.
func TestEngagedStorySeatIDsPredicate(t *testing.T) {
	got := engagedStorySeatIDs([]seatPayload{
		{ID: "sty_live", StorySeat: true, Stale: false},
		{ID: "sty_stale", StorySeat: true, Stale: true},
		{ID: "tsk_1", StorySeat: false, Stale: false, InFlight: true},
		{ID: "sty_nofield", StorySeat: false, Stale: false, InFlight: true},
		{ID: "", StorySeat: true, Stale: false}, // empty id skipped
	})
	if len(got) != 1 || got[0] != "sty_live" {
		t.Fatalf("engaged ids = %v, want [sty_live]", got)
	}
	// Settled engaged lease (story_seat, !in_flight, !stale) still counts.
	got2 := engagedStorySeatIDs([]seatPayload{
		{ID: "sty_settled", StorySeat: true, InFlight: false, Stale: false},
	})
	if len(got2) != 1 || got2[0] != "sty_settled" {
		t.Fatalf("settled engaged = %v, want [sty_settled]", got2)
	}
	if n := len(engagedStorySeatIDs(nil)); n != 0 {
		t.Fatalf("nil seats count = %d", n)
	}
}

const costWFDone = `[meta]
name = "done"
type = "workflow"
scope = "project"
description = "fixture"

["*"]
obligations = ["raised", "planned", "parked", "closed"]
`

const costWFStep = `[meta]
name = "step"
type = "workflow"
scope = "project"
description = "fixture"

[raised]
status = "backlog"
start = true

[planned]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[parked]
status = "blocked"
agent = "reviewer"
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["planned"]
`
