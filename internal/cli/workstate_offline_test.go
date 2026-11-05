package cli

import (
	"context"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
)

// trimKeeps asserts that a trim holding nothing still leaves every event of id.
func trimKeeps(t *testing.T, server, repo, id string, want int) {
	t.Helper()
	cur, err := hosted.LoadWorkstateCursor(server, "probe", repo)
	if err != nil {
		t.Fatal(err)
	}
	withRepoStore(t, func(db *store.DB) {
		if _, err := verb.TrimStoryHistory(context.Background(), db.Ledger, map[string]bool{}, cur.StoryHeads); err != nil {
			t.Fatal(err)
		}
	})
	if n := localEvents(t, id); n != want {
		t.Fatalf("the trim left %d event(s) of %s, want its unpushed work kept (%d)", n, id, want)
	}
}
