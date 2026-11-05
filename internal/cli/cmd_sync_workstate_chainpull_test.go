package cli

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

func pullRaw(t *testing.T, server string) (string, error) {
	t.Helper()
	return runRoot(t, "sync", "workstate", "pull", "--server", server)
}

func idsOf(chain []ledger.Entry) []string {
	var ids []string
	for _, e := range chain {
		ids = append(ids, e.ID)
	}
	return ids
}

func sameChain(t *testing.T, got, want []ledger.Entry) {
	t.Helper()
	if !slices.Equal(idsOf(got), idsOf(want)) {
		t.Fatalf("local chain = %v, want the hosted chain %v", idsOf(got), idsOf(want))
	}
	for i := range want {
		if got[i].BaseID != want[i].BaseID || got[i].Kind != want[i].Kind || got[i].Body != want[i].Body || got[i].WorkflowHash != want[i].WorkflowHash {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// seedLocalPrefix gives this checkout the events of prefix, under their hosted ids
// and bases, and saves the last of them as the story's head.
func seedLocalPrefix(t *testing.T, server, repo, id string, prefix []ledger.Entry) {
	t.Helper()
	withRepoStore(t, func(db *store.DB) {
		for _, e := range prefix {
			if _, err := db.Ledger.Upsert(context.Background(), e, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err := hosted.SaveStoryHead(server, "probe", repo, id, prefix[len(prefix)-1].ID); err != nil {
		t.Fatal(err)
	}
}
