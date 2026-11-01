package verb

import (
	"os"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
)

// TestSeatListExposesActivityDetail (sty_752c4ef2 AC7): seatRowFromLease
// carries the in-flight DISPATCH metadata — agent, model, pid, last event,
// its age, the real-event count, and this repo's resolved idle_timeout for
// that binding — extending the existing activity/activity_age output rather
// than a second record.
func TestSeatListExposesActivityDetail(t *testing.T) {
	withWiring(t)
	SetAgentsConfig(config.AgentsConfig{
		Agents: map[string]config.AgentBinding{
			"coder": {Command: "claude {payload}", IdleTimeout: "7m"},
		},
	}, nil)

	now := time.Now().UTC()
	live := lease.Lease{
		ItemID: "sty_live", Kind: "story", StorySeat: true, Owner: "alice", State: "in_progress",
		AcquiredAt: now, HeartbeatAt: now,
		InFlight: true, InFlightAt: now, InFlightPid: os.Getpid(),
		ActivityLabel: "dispatch:coder", ActivityIndex: 1, ActivityTotal: 1,
		ActivityAt:         now.Add(-10 * time.Second),
		ActivityAgent:      "coder",
		ActivityModel:      "sonnet",
		ActivityPid:        4242,
		ActivityEventLabel: "tool: Bash",
		ActivityEventAt:    now.Add(-3 * time.Second),
		ActivityEventCount: 7,
	}
	row := seatRowFromLease(live, now)
	if row.Agent != "coder" {
		t.Errorf("Agent = %q, want %q", row.Agent, "coder")
	}
	if row.Model != "sonnet" {
		t.Errorf("Model = %q, want %q", row.Model, "sonnet")
	}
	if row.Pid != 4242 {
		t.Errorf("Pid = %d, want 4242", row.Pid)
	}
	if row.LastEvent != "tool: Bash" {
		t.Errorf("LastEvent = %q, want %q", row.LastEvent, "tool: Bash")
	}
	if row.LastEventAge == "" {
		t.Error("LastEventAge must be set for a live in-flight lease")
	}
	if row.EventCount != 7 {
		t.Errorf("EventCount = %d, want 7", row.EventCount)
	}
	if row.IdleTimeout != (7 * time.Minute).String() {
		t.Errorf("IdleTimeout = %q, want %q", row.IdleTimeout, (7 * time.Minute).String())
	}
}

// TestSeatListOmitsActivityDetailWhenNotStamped: a lease with no dispatch
// metadata yet (or not effectively in flight) reports none of the new fields —
// they ride the SAME omitempty contract the existing activity fields use.
func TestSeatListOmitsActivityDetailWhenNotStamped(t *testing.T) {
	now := time.Now().UTC()
	settled := lease.Lease{
		ItemID: "sty_settled", Kind: "story", StorySeat: true, Owner: "alice", State: "plan",
		AcquiredAt: now, HeartbeatAt: now,
	}
	row := seatRowFromLease(settled, now)
	if row.Agent != "" || row.Model != "" || row.Pid != 0 || row.LastEvent != "" || row.EventCount != 0 {
		t.Fatalf("settled (not in-flight) row must omit dispatch detail: %+v", row)
	}
}

// sty_216d5205: a gate round's live tally rides the seat row `satelle story
// seat` prints, beside the activity index and total; a non-gate phase carries none.
func TestSeatRowCarriesGateActivity(t *testing.T) {
	now := time.Now().UTC()
	gating := lease.Lease{
		ItemID: "sty_gate", Kind: "story", StorySeat: true, Owner: "alice", State: "plan",
		AcquiredAt: now, HeartbeatAt: now,
		InFlight: true, InFlightAt: now, InFlightPid: os.Getpid(),
		ActivityLabel: "rev-b", ActivityIndex: 3, ActivityTotal: 4, ActivityAt: now,
		ActivityGates: true, ActivityPassed: 1, ActivityFailed: 1,
	}
	row := seatRowFromLease(gating, now)
	if !row.ActivityGates || row.ActivityPassed != 1 || row.ActivityFailed != 1 || row.ActivityTotal != 4 {
		t.Fatalf("gate row = %+v, want gates, 1 passed, 1 failed of 4", row)
	}
	gating.ActivityGates, gating.ActivityPassed, gating.ActivityFailed = false, 0, 0
	gating.ActivityLabel = "dispatch:in_progress"
	if row := seatRowFromLease(gating, now); row.ActivityGates || row.ActivityPassed+row.ActivityFailed != 0 {
		t.Fatalf("a performer phase must carry no gate tally: %+v", row)
	}
}
