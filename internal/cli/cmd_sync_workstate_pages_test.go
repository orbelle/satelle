package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/verb"
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

// sty_a28c64dd AC2: a pull delivers a done story's row with its early events on
// one page and the rest of its chain, through release → done, on the next. The
// second page carries no row of the story, yet its listed status ends done: the
// page rebuilds every story it wrote events to, not only the rows it applied.
func TestPagedPullRefoldsAStoryWhoseLaterEventsLandWithoutItsRow(t *testing.T) {
	a := testApp(t)
	const id = "sty_paged01"
	at := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)
	c := &pagedChain{t: t, story: id, at: at}

	applyPage(t, a, []hosted.WorkstateItem{hostedStory(id, workitem.StatusDone, at.Add(time.Hour))},
		[]hosted.WorkstateLedgerRow{c.created("evt_p1"), c.moved("evt_p2", "backlog", "plan")}, nil)
	if got := rowStatus(t, a, id).Status; got != "plan" {
		t.Fatalf("after page 1 the row = %q, want plan (the fold of the chain that has landed)", got)
	}

	applyPage(t, a, nil, []hosted.WorkstateLedgerRow{
		c.moved("evt_p3", "plan", "in_progress"),
		c.moved("evt_p4", "in_progress", "integration"),
		c.moved("evt_p5", "integration", "release"),
		c.moved("evt_p6", "release", "done"),
	}, nil)
	row := rowStatus(t, a, id)
	if row.Status != workitem.StatusDone {
		t.Errorf("after page 2 the listed status = %q, want done (its newest transition)", row.Status)
	}
	if d, err := verb.StoryDrifts(context.Background(), a.Store.Ledger, row); err != nil || len(d) != 0 {
		t.Errorf("row disagrees with its chain after page 2: %+v (err %v)", d, err)
	}
}

// sty_a28c64dd AC1: a page that carries no row of a closed story but a newer
// transition that reopens it moves the row and names the story in the "the pull
// moved it" line.
func TestPagedPullNamesAClosedStoryAPageOfItsEventsMoves(t *testing.T) {
	// The moved line asks the story's route whether done ends its engagement, so
	// the verb layer reads the repo's indexed workflows as a command run does.
	workstateRepo(t, holdOffToml)
	if out, err := runRoot(t, "story", "list"); err != nil {
		t.Fatalf("story list: %v\n%s", err, out)
	}
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	withVerbWiring(t)
	verb.SetDocIndexStore(a.Store.DocIndex)
	const id = "sty_paged02"
	at := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)
	c := &pagedChain{t: t, story: id, at: at}

	applyPage(t, a, []hosted.WorkstateItem{hostedStory(id, workitem.StatusDone, at.Add(time.Hour))},
		[]hosted.WorkstateLedgerRow{
			c.created("evt_q1"),
			c.moved("evt_q2", "backlog", "in_progress"),
			c.moved("evt_q3", "in_progress", "done"),
		}, nil)
	if got := rowStatus(t, a, id).Status; got != workitem.StatusDone {
		t.Fatalf("after page 1 the row = %q, want done", got)
	}

	var out storyOutcome
	applyPage(t, a, nil, []hosted.WorkstateLedgerRow{c.moved("evt_q4", "done", "in_progress")}, &out)
	if got := rowStatus(t, a, id).Status; got != "in_progress" {
		t.Errorf("after page 2 the row = %q, want in_progress (its newest transition)", got)
	}
	moved := strings.Join(out.Moved, "\n")
	for _, want := range []string{id, "was done", "moved it to in_progress", "event evt_q4"} {
		if !strings.Contains(moved, want) {
			t.Errorf("moved lines do not name %q:\n%s", want, moved)
		}
	}
}
