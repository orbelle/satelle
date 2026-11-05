package cli

import (
	"time"
)

// Work-state pull merges by id (sty_78e20d15): a local row the hosted copy has
// never seen is kept beside the hosted rows, and only a same-id row whose local
// side changed (or a ledger row that differs) refuses without --force.

const mergeToml = "[sync]\n\n[hosted]\nproject = \"probe\"\n"

func seedHostedStory(f *fakeWorkstateServer, id, title, status string, updated time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.itemsByID["probe"] == nil {
		f.itemsByID["probe"] = map[string]any{}
	}
	f.itemsByID["probe"][id] = map[string]any{
		"id": id, "kind": "story", "status": status, "title": title,
		"created_at": updated.Add(-time.Hour).Format(time.RFC3339Nano),
		"updated_at": updated.Format(time.RFC3339Nano),
	}
}

func seedHostedLedger(f *fakeWorkstateServer, id, storyID, kind string, payload any, created time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ledgerByID["probe"] == nil {
		f.ledgerByID["probe"] = map[string]any{}
	}
	f.ledgerByID["probe"][id] = map[string]any{
		"id": id, "story_id": storyID, "kind": kind, "payload": payload,
		"created_at": created.Format(time.RFC3339Nano),
	}
}
