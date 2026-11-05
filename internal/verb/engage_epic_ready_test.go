package verb_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// sty_a476b913: a child of an epic leaves backlog into an engaging status only
// once its epic-parent's readiness round holds. The fixture is the after-children
// route: the epic's readiness edge is raised(backlog) -> ready, and the `fix`
// child's route declares no propose step at all.

// acceptEpicReadiness records an accepted readiness round on the epic's
// from -> to edge, as `story ready` would.
func acceptEpicReadiness(t *testing.T, db *store.DB, epicID, from, to string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"from": from, "to": to, "attempt": "att_test"})
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: epicID, Kind: ledger.KindReadinessAccept, Actor: "executor", Payload: payload,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
}
