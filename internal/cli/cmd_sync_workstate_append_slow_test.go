//go:build clislow

package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
)

// sty_9bdf08c4 AC5: a story held for unpushed code and one withheld for fold drift
// get no Append; a clean story does.
func TestSlowPushAppendsNothingForWithheldStories(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	corruptRow(t, w.plan, "body", "corrupted in the cache")

	out, err := pushRaw(t, w.url)
	if err == nil {
		t.Fatalf("a drift-withheld story must make push exit non-zero:\n%s", out)
	}
	if !strings.Contains(out, "held "+w.dirty+":") || !strings.Contains(out, "withheld "+w.plan+":") {
		t.Errorf("the withholding is reported as before:\n%s", out)
	}
	for _, id := range []string{w.dirty, w.unpushed, w.plan} {
		if n := len(w.f.chain.appendsFor(id)); n != 0 {
			t.Errorf("withheld story %s got %d Appends", id, n)
		}
	}
	if n := len(w.f.chain.appendsFor(w.ok)); n != 1 {
		t.Errorf("the clean story got %d Appends, want 1", n)
	}
}

// sty_aa605c84 AC3: a story pushed mid-flight, moved on and noted here, then pushed
// again keeps the hosted ids equal to the local ones in order, and the second push
// is a pure append of the two new events on the first push's head. A story pushed
// once (the control) stays equal too.
func TestSlowPushedTwiceKeepsHostedIdsEqualLocal(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	ungatedLifecycle(t, repo)
	twice, once := createStoryID(t, "twice"), createStoryID(t, "once")
	if out, err := runRoot(t, "story", "set", twice, "--status", "plan"); err != nil {
		t.Fatalf("move to plan: %v\n%s", err, out)
	}
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("first push: %v\n%s", err, out)
	}
	first := localIDs(t, twice)
	sentFirst := len(f.chain.appendsFor(twice))

	// The status move is its transition event alone (a `story set` also writes its
	// working_on, change record and documents), the row rebuilt from the chain as a
	// verb leaves it; the note goes through `story log`, as the vire correction did.
	withRepoStore(t, func(db *store.DB) {
		ctx := context.Background()
		payload, err := json.Marshal(map[string]string{"from": "plan", "to": "in_progress"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: twice, Kind: ledger.KindStatusTransition, Actor: "bob", Body: "plan → in_progress", Payload: payload}, time.Now()); err != nil {
			t.Fatal(err)
		}
		row, err := db.Stories.Get(ctx, twice)
		if err != nil {
			t.Fatal(err)
		}
		if row, err = verb.RebuildRow(ctx, db.Stories, db.Ledger, row, time.Now()); err != nil || row.Status != "in_progress" {
			t.Fatalf("rebuild after the move: status %q, %v", row.Status, err)
		}
	})
	if out, err := runRoot(t, "story", "log", twice, "--kind", "note", "--data", "text=a correction"); err != nil {
		t.Fatalf("note: %v\n%s", err, out)
	}
	local := localIDs(t, twice)
	if len(local) != len(first)+2 || !slices.Equal(local[:len(first)], first) {
		t.Fatalf("local chain = %v, want the first push's %v and two new events", local, first)
	}
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("second push: %v\n%s", err, out)
	}

	if got := f.chain.stored("probe", twice); !slices.Equal(got, local) {
		t.Errorf("hosted ids = %v, want the local ids %v in order", got, local)
	}
	second := f.chain.appendsFor(twice)[sentFirst:]
	var sent []string
	for i, r := range second {
		wantBase := first[len(first)-1]
		if i > 0 {
			wantBase = sent[len(sent)-1]
		}
		if r.GetBaseId() != wantBase {
			t.Errorf("second push Append %d base = %q, want %q", i, r.GetBaseId(), wantBase)
		}
		sent = append(sent, eventIDs(r.GetEvents())...)
	}
	if len(second) == 0 || second[0].GetBaseId() != first[len(first)-1] || !slices.Equal(sent, local[len(first):]) {
		t.Errorf("second push sent %v on base %v, want exactly %v on the first push's head %q", sent, second, local[len(first):], first[len(first)-1])
	}
	if got := f.chain.stored("probe", once); !slices.Equal(got, localIDs(t, once)) {
		t.Errorf("control: hosted ids = %v, want the local ids %v", got, localIDs(t, once))
	}
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Errorf("conflicts = %v, want none", c)
	}
}
