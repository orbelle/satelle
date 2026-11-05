package verb_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestGateRejectKeepsPreexistingSeat: a rejected edge on a story that already
// holds its seat clears only the in-flight mark — the seat row is not the
// gate's to delete, only one this call created.
func TestGateRejectKeepsPreexistingSeat(t *testing.T) {
	withWiring(t)
	db := wireWithWorkflowsStore(t, singleStoryWF)

	var a workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "A", "category": "feature"}), &a)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}), &a)
	before, err := db.Leases.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("seat after engage: %v", err)
	}

	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{Gated: true, Accept: false, Skill: "s", Notes: "no"}})
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "in_progress"}); err == nil {
		t.Fatal("expected the gate to reject")
	}

	after, err := db.Leases.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("a gate reject dropped a pre-existing seat: %v", err)
	}
	if !after.AcquiredAt.Equal(before.AcquiredAt) {
		t.Fatalf("seat was re-created on reject: acquired %v -> %v", before.AcquiredAt, after.AcquiredAt)
	}
	if after.InFlight {
		t.Fatal("in_flight must be cleared after a reject so a retry can re-enter")
	}
}
