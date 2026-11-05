package cli

import (
	"context"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

const ledgerPushToml = "[sync]\n\n[hosted]\nproject = \"probe\"\n"

func (f *fakeWorkstateServer) hasLedger(project, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.ledgerByID[project][id]
	return ok
}

// withRepoStore opens the repo's database between runRoot calls (which close
// theirs), the way the other fixtures here do.
func withRepoStore(t *testing.T, fn func(db *store.DB)) {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	fn(db)
}

func upsertLedgerRow(t *testing.T, db *store.DB, id string, at time.Time) {
	t.Helper()
	if _, err := db.Ledger.Upsert(context.Background(), ledger.Entry{
		ID: id, StoryID: "sty_cursor", Kind: ledger.KindStoryUpdated, Body: id, CreatedAt: at,
	}, time.Now()); err != nil {
		t.Fatalf("upsert %s: %v", id, err)
	}
}

func pushOK(t *testing.T, server string) string {
	t.Helper()
	out, err := runRoot(t, "sync", "workstate", "push", "--server", server)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	return out
}

func createProbeStory(t *testing.T) {
	t.Helper()
	if out, err := runRoot(t, "story", "create", "--title", "Ledger cursor probe",
		"--body", "A story for the ledger cursor tests.", "--acceptance", "1. pushed"); err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
}
