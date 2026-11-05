package cli

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// appliedItemIDs lists, in order, the item ids every Apply the server received carried.
func appliedItemIDs(f *fakeWorkstateServer) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, req := range f.applies {
		for _, it := range req.GetItems() {
			var w struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(it.GetRecord(), &w)
			ids = append(ids, w.ID)
		}
	}
	return ids
}

func resetApplies(f *fakeWorkstateServer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies = nil
}

// appendLocal makes one local event on a story, as a mutating verb would.
func appendLocal(t *testing.T, id, body string) {
	t.Helper()
	withRepoStore(t, func(db *store.DB) {
		if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: id, Kind: ledger.KindComment, Actor: "bob", Body: body}, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
}

func localIDs(t *testing.T, id string) []string {
	t.Helper()
	var ids []string
	for _, e := range localChain(t, id) {
		ids = append(ids, e.ID)
	}
	return ids
}
