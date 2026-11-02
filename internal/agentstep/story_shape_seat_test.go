package agentstep

import (
	"context"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// TestBaselineStoryPrinciplesReachPlanningSeats (sty_bcd344fd AC1, AC2;
// sty_73a2dc68 AC1; sty_4952b78a AC5): the shipped planner, ready-reviewer,
// coder and reviewer seats are handed satelle-story-shape, satelle-start-small,
// satelle-dispatch-pointers and satelle-local-first. The bindings come from the embedded baseline seat list
// and the documents from the embedded defaults, so the test proves the product
// baseline, not a fixture: each seat's resolved selector, through
// ResidentPrincipleDocs, must deliver every one of them.
func TestBaselineStoryPrinciplesReachPlanningSeats(t *testing.T) {
	agents, err := config.BaselineAgents()
	if err != nil {
		t.Fatal(err)
	}
	docs := embeddedPrincipleDocs()
	for _, seat := range []string{"planner", "ready-reviewer", "coder", "reviewer"} {
		b, ok := agents.RawBinding(seat)
		if !ok {
			t.Fatalf("baseline has no [%s] seat", seat)
		}
		got := map[string]bool{}
		for _, d := range ResidentPrincipleDocs(context.Background(), docs, b.ResolvedPrinciples()) {
			got[d.Name] = true
		}
		for _, want := range []string{"satelle-story-shape", "satelle-start-small", "satelle-dispatch-pointers", "satelle-local-first"} {
			if !got[want] {
				t.Errorf("seat [%s] (principles %q) is not handed %s", seat, b.ResolvedPrinciples(), want)
			}
		}
	}
}
