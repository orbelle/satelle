//go:build clislow

package cli

import (
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// sty_199ad82b AC1, AC2: a rehydrate, plain or forced, takes each hosted chain
// event for event, leaves a forked chain as it was and records the fork, leaves a
// story the server holds no events of with only its page row, and makes no event
// of its own — no workflow_version, definition_snapshot or overlap.
func TestSlowRehydrateTakesHostedChainsAndAppendsNothing(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "plain"
		if force {
			name = "force"
		}
		t.Run(name, func(t *testing.T) {
			fx := newPassFixture(t, true)
			fx.fork(t)
			before := fx.chains(t)

			args := []string{"sync", "rehydrate", "--server", fx.ts.URL}
			if force {
				args = append(args, "--force")
			}
			if out, err := runRoot(t, args...); err != nil {
				t.Fatalf("rehydrate: %v\n%s", err, out)
			}
			after := fx.chains(t)
			sameChain(t, chainOf(t, after[fx.taken]), fx.wants[fx.taken])
			sameChain(t, chainOf(t, after[fx.edited]), fx.wants[fx.edited])
			sameEvents(t, "the rehydrate", before[fx.forked], after[fx.forked])
			if got := idsOf(after[pageOnlyID]); len(got) != 1 || got[0] != "led_pageonly01" {
				t.Errorf("page-only chain = %v, want only the page row led_pageonly01", got)
			}
			noPassEvents(t, "the rehydrate", fx.known(before), after)
			if c := storyConflicts(t, fx.repo)[fx.forked]; c.Reason != conflictChainForked {
				t.Errorf("conflict for the forked story = %+v, want %q", c, conflictChainForked)
			}
		})
	}
}

// sty_199ad82b AC1: a pull and a snapshot of a story whose hosted chain the local
// one forks leave its chain as it was and record the fork.
func TestSlowPullAndSnapshotOfAForkedStoryAppendNothing(t *testing.T) {
	for _, verb := range []string{"pull", "snapshot"} {
		t.Run(verb, func(t *testing.T) {
			fx := newPassFixture(t, true)
			fx.fork(t)
			before := fx.chains(t)

			if out, err := runRoot(t, "sync", "workstate", verb, "--server", fx.ts.URL); err != nil {
				t.Fatalf("%s: %v\n%s", verb, err, out)
			}
			after := fx.chains(t)
			sameEvents(t, "the "+verb, before[fx.forked], after[fx.forked])
			noPassEvents(t, "the "+verb, fx.known(before), after)
			c := storyConflicts(t, fx.repo)[fx.forked]
			if c.Reason != conflictChainForked || c.Source != overlapSourceConflict || c.LocalHolder == "" || c.RemoteHolder == "" {
				t.Errorf("conflict = %+v, want %q with source and both holders", c, conflictChainForked)
			}
		})
	}
}

// sty_199ad82b AC1: a forced rehydrate of a story whose linear chain folds to a
// definition the hosted row does not carry leaves the chain as it was, puts the
// hosted copy on the row and records the disagreement.
func TestSlowForcedRehydrateOfALocallyEditedStoryRecordsTheConflict(t *testing.T) {
	fx := newPassFixture(t, true)
	fx.edit(t)
	before := fx.chains(t)

	if out, err := runRoot(t, "sync", "rehydrate", "--server", fx.ts.URL, "--force"); err != nil {
		t.Fatalf("rehydrate --force: %v\n%s", err, out)
	}
	after := fx.chains(t)
	sameEvents(t, "the forced rehydrate", before[fx.edited], after[fx.edited])
	noPassEvents(t, "the forced rehydrate", fx.known(before), after)
	if row := passRow(t, fx.edited); row.Title != "edited" {
		t.Errorf("row title = %q, want the hosted copy's", row.Title)
	}
	if c := storyConflicts(t, fx.repo)[fx.edited]; c.Reason != conflictForcedHostedWins || c.LocalHolder == "" || c.RemoteHolder == "" {
		t.Errorf("conflict = %+v, want %q with both holders named", c, conflictForcedHostedWins)
	}
}

// sty_199ad82b AC3: after a pull that met a fork and a forced rehydrate that met a
// local edit, a push sends no overlap event and no workflow_version or
// definition_snapshot event a pass made: every one it carries was on a chain
// before the passes. The fixture holds no legacy page-only story: store.Open seeds
// one whose chain defines no title (sty_8de247b8 owns that), and the push would
// carry that seed, which no pass made.
func TestSlowPushAfterConflictingPassesSendsNoPassEvent(t *testing.T) {
	fx := newPassFixture(t, false)
	fx.fork(t)
	fx.edit(t)
	before := fx.chains(t)
	known := fx.known(before)
	if out, err := runRoot(t, "sync", "workstate", "pull", "--server", fx.ts.URL); err == nil {
		t.Logf("pull: %s", out) // the edited story may refuse it; either way the pass ran
	}
	if out, err := runRoot(t, "sync", "rehydrate", "--server", fx.ts.URL, "--force"); err != nil {
		t.Fatalf("rehydrate --force: %v\n%s", err, out)
	}
	if c := storyConflicts(t, fx.repo); c[fx.forked].Reason != conflictChainForked || c[fx.edited].Reason != conflictForcedHostedWins {
		t.Fatalf("precondition: conflicts = %+v, want the fork and the forced win recorded", c)
	}
	sent := len(fx.f.chain.appends)

	_, _ = pushRaw(t, fx.ts.URL) // the forked story's push is refused; the payloads are the subject
	fx.f.chain.mu.Lock()
	appends := fx.f.chain.appends[sent:]
	fx.f.chain.mu.Unlock()
	for _, req := range appends {
		for _, e := range req.GetEvents() {
			var rec struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(e.GetPayload(), &rec); err != nil {
				t.Fatalf("event %s payload: %v", e.GetId(), err)
			}
			switch rec.Kind {
			case ledger.KindOverlap:
				t.Errorf("the push sent overlap event %s for %s", e.GetId(), req.GetStoryId())
			case ledger.KindWorkflowVersion, ledger.KindDefinitionSnapshot:
				if !known[e.GetId()] {
					t.Errorf("the push sent %s %s for %s, which a pass made", rec.Kind, e.GetId(), req.GetStoryId())
				}
			}
		}
	}
}
