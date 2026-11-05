package cli

import (
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// trimmedStory is a published old story whose hosted chain moved it to plan under
// a new title, left by a hosted-mode snapshot with no local history.
func trimmedStory(t *testing.T, f *fakeWorkstateServer, server, repo, title string) string {
	t.Helper()
	at := time.Now().Add(-72 * time.Hour).UTC()
	moved := workitem.Item{Kind: workitem.KindStory, Title: title + " (from the chain)", Status: "plan", CreatedAt: at, UpdatedAt: at}
	id := cacheStory(t, f, server, repo, title, "backlog", at, ledger.Entry{
		ID: ledger.NewID(), Kind: ledger.KindDefinitionSnapshot, Actor: "satelle", Body: "definition",
		Payload: storyfold.SnapshotPayloadOf(moved, true),
	})
	return id
}
