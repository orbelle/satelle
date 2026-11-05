package web

import (
	"testing"
	"time"

)

// TestBuildDispatchIndicatorRunning (sty_752c4ef2 AC6, rendering 1: running):
// an in-flight, non-stale seat with agent/model dispatch detail produces a
// running indicator carrying that detail — not shown when the seat is stale,
// not in flight, or has no dispatch metadata (a gate-only phase stamp).
func TestBuildDispatchIndicatorRunning(t *testing.T) {
	now := time.Now().UTC()
	seat := seatPayload{
		ID: "sty_1", InFlight: true, Stale: false,
		Agent: "coder", Model: "sonnet", LastEvent: "tool: Bash",
		LastEventAt: now.Add(-10 * time.Second), ActivityStartedAt: now.Add(-18 * time.Minute),
		IdleTimeoutNs: int64(5 * time.Minute),
	}
	vm := buildDispatchIndicator(seat, now)
	if vm == nil {
		t.Fatal("expected a running indicator for an in-flight seat with dispatch detail")
	}
	if vm.Agent != "coder" || vm.Model != "sonnet" || vm.LastEvent != "tool: Bash" {
		t.Errorf("indicator = %+v", vm)
	}
	if vm.Warn {
		t.Error("10s idle against a 5m idle_timeout must not warn")
	}

	for _, bad := range []seatPayload{
		{ID: "x", InFlight: false, Agent: "coder"},             // not in flight
		{ID: "x", InFlight: true, Stale: true, Agent: "coder"}, // stale
		{ID: "x", InFlight: true, Stale: false},                // no dispatch detail
	} {
		if buildDispatchIndicator(bad, now) != nil {
			t.Errorf("expected no indicator for %+v", bad)
		}
	}
}

// TestBuildDispatchIndicatorWarnsPastHalfIdleTimeout (sty_752c4ef2 AC6,
// rendering 2: warning): the indicator turns to warn once idle time (since
// the last real event) crosses HALF of the seat's resolved idle_timeout —
// not before.
func TestBuildDispatchIndicatorWarnsPastHalfIdleTimeout(t *testing.T) {
	now := time.Now().UTC()
	base := seatPayload{
		ID: "sty_1", InFlight: true, Stale: false, Agent: "coder",
		ActivityStartedAt: now.Add(-time.Minute), IdleTimeoutNs: int64(5 * time.Minute),
	}
	justUnder := base
	justUnder.LastEventAt = now.Add(-149 * time.Second) // < 2m30s (half of 5m)
	if vm := buildDispatchIndicator(justUnder, now); vm == nil || vm.Warn {
		t.Errorf("idle just under half idle_timeout must not warn: %+v", vm)
	}
	over := base
	over.LastEventAt = now.Add(-3 * time.Minute) // > 2m30s
	if vm := buildDispatchIndicator(over, now); vm == nil || !vm.Warn {
		t.Errorf("idle past half idle_timeout must warn: %+v", vm)
	}
}
