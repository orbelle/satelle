package cli

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// pagedChain builds a story's hosted ledger rows in chain order: each row names
// the one before it as its base, the first names none, and created_at rises.
type pagedChain struct {
	t     *testing.T
	story string
	at    time.Time
	prev  string
	n     int
}

func (c *pagedChain) row(id, kind string, payload any) hosted.WorkstateLedgerRow {
	c.t.Helper()
	c.n++
	rec, err := json.Marshal(map[string]any{
		"id": id, "story_id": c.story, "kind": kind, "payload": payload,
		"created_at": c.at.Add(time.Duration(c.n) * time.Second), "base_id": c.prev,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	c.prev = id
	return hosted.WorkstateLedgerRow{ID: id, StoryID: c.story, Kind: kind, Record: rec}
}

func (c *pagedChain) created(id string) hosted.WorkstateLedgerRow {
	return c.row(id, ledger.KindStoryCreated, storyfold.CreatedPayloadOf(workitem.Item{
		Title: "T", Category: "fix", Status: "backlog",
	}, "cli"))
}

func (c *pagedChain) moved(id, from, to string) hosted.WorkstateLedgerRow {
	return c.row(id, ledger.KindStatusTransition, transitionPayload(from, to))
}

// applyPage applies one page of a paged pull the way snapshotPass.apply does.
func applyPage(t *testing.T, a *app.App, items []hosted.WorkstateItem, rows []hosted.WorkstateLedgerRow, out *storyOutcome) {
	t.Helper()
	optIn := map[string]bool{"stories": true, "ledger": true}
	if _, _, _, _, err := upsertWorkstatePage(context.Background(), a, optIn, items, rows, false, time.Now().UTC(), out, nil); err != nil {
		t.Fatal(err)
	}
}

func rowStatus(t *testing.T, a *app.App, id string) workitem.Item {
	t.Helper()
	row, err := a.Store.Stories.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}
