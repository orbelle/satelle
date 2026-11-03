//go:build clislow

package cli

import (
	"testing"
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
