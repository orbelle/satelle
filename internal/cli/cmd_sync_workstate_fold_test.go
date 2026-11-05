package cli

import (
	"context"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// seedHostedFull is seedHostedStory with the definition fields a story carries.
func seedHostedFull(f *fakeWorkstateServer, id, title, body, criteria, status string, tags []string, updated time.Time) {
	seedHostedStory(f, id, title, status, updated)
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.itemsByID["probe"][id].(map[string]any)
	rec["body"], rec["acceptance_criteria"], rec["tags"], rec["category"] = body, criteria, tags, "feature"
}

func localDrift(t *testing.T) []verb.StoryDrift {
	t.Helper()
	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	drifts, err := verb.DetectDrift(context.Background(), db.Stories, db.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	return drifts
}

// passRow is a story's cached row as the last sync pass left it, read without the
// seeding store.Open does.
func passRow(t *testing.T, id string) workitem.Item {
	t.Helper()
	db, err := store.OpenReadOnly(runtimeDBPath(t))
	if err != nil {
		t.Fatalf("open db read-only: %v", err)
	}
	defer func() { _ = db.Close() }()
	row, err := db.Stories.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}
