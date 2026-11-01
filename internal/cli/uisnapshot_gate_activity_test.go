package cli

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_216d5205 AC1: a gate round's live tally rides the mirror's seat row under
// the keys web.seatPayload decodes (TestSeatPayloadDecodesGateActivity pins the
// other half), so the progress chip can count the round's gates as they return.
func TestListSeatsJSONCarriesGateActivity(t *testing.T) {
	e := closeGuardEnv(t)
	ctx := context.Background()
	sty, err := e.db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "Gating", Body: "goal",
		AcceptanceCriteria: "1. x", Category: "chore", Status: workitem.StatusBacklog,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if ins, err := e.db.Leases.InsertIfAbsent(ctx, lease.AcquireOpts{ItemID: sty.ID, Kind: "story", Owner: lease.ResolveOwner(), State: "backlog", SeatKey: sty.ID}); err != nil || !ins {
		t.Fatalf("insert: %v %v", ins, err)
	}
	if err := e.db.Leases.SetActivity(ctx, sty.ID, lease.Activity{Label: "rev-b", Index: 3, Total: 4, Gates: true, Passed: 1, Failed: 1}); err != nil {
		t.Fatal(err)
	}
	seats, err := listSeatsJSON(ctx, &app.App{Config: config.Config{}, RepoRoot: e.repo, Store: e.db})
	if err != nil {
		t.Fatal(err)
	}
	var pushed map[string]any
	for _, raw := range seats {
		var row map[string]any
		if json.Unmarshal(raw, &row) == nil && row["id"] == sty.ID {
			pushed = row
		}
	}
	if pushed == nil {
		t.Fatalf("no seat row pushed for %s", sty.ID)
	}
	for key, want := range map[string]any{
		"in_flight": true, "activity_gates": true,
		"activity_total": float64(4), "activity_passed": float64(1), "activity_failed": float64(1),
	} {
		if pushed[key] != want {
			t.Errorf("pushed[%q] = %v, want %v (row %v)", key, pushed[key], want, pushed)
		}
	}
}
