//go:build clislow

package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/verb"
)

// AC2: a close by id from another tree and session is judged by the edge's gate
// on the story's own change set, accepted, and frees the story's seat.
func TestSlowCloseByIDFromAnotherSessionIsJudged(t *testing.T) {
	e, id, r := otherSessionClose(t, "close-diff-review")
	if err := e.set(id, "done"); err != nil {
		t.Fatalf("close from another session: %v", err)
	}
	if got := e.status(id); got != "done" {
		t.Errorf("status = %q, want done", got)
	}
	seen := r.seen()
	if len(seen) != 1 || !hasString(seen[0].Files, "slice.go") {
		t.Errorf("the gate must judge the story's change set, payload diffs = %+v", seen)
	}
	if e.seatHeld(id) {
		t.Error("the accepted close must release the story's seat")
	}
}

// AC2: every gate on the edge still judges — one rejecting gate refuses the close
// and leaves the story where it was.
func TestSlowCloseByIDFromAnotherSessionRefusedByAnyGate(t *testing.T) {
	e, id, r := otherSessionClose(t, "close-diff-review", "close-reject-review")
	err := e.set(id, "done")
	if err == nil {
		t.Fatal("a close with a rejecting gate must be refused")
	}
	if got := e.status(id); got != "in_progress" {
		t.Errorf("status = %q after a refused close, want in_progress", got)
	}
	if n := len(r.seen()); n != 2 {
		t.Errorf("both gates on the edge must judge, ran %d", n)
	}
}

// AC2: the reviewer payload a gate judges is the story's own change set when the
// close is presented from another tree and another session.
func TestSlowDiffResolverReadsTheStoriesTreeFromAnotherSession(t *testing.T) {
	_, id, _ := otherSessionClose(t)
	d := diffResolver()(context.Background(), id)
	if d == nil || d.NoBaseline {
		t.Fatalf("the gate payload diff must resolve from another tree, got %+v", d)
	}
	if !hasString(d.Files, "slice.go") {
		t.Errorf("files = %v, want the slice committed in the story's own tree", d.Files)
	}
	// Unmarked, interactive enumeration from the same tree keeps the anchor.
	if _, err := verb.StoryDiff(context.Background(), id, verb.StoryDiffOpts{}); err == nil ||
		!strings.Contains(err.Error(), "was engaged from working tree") {
		t.Errorf("a plain story diff from another tree must still be refused: %v", err)
	}
}
