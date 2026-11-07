package web

import (
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// Engagement counts each story in a non-terminal state that a seat is bound to
// once; a legacy row counts only as a non-stale story seat. A task's or task
// run's seat, and a seat on a terminal story, is not a story engaged.
func TestEngagedItems(t *testing.T) {
	stories := []Item{
		{ID: "sto_a", Kind: "story", Status: "beta", Workflow: "flow"},
		{ID: "sto_done", Kind: "story", Status: "zed", Workflow: "flow"},
		{ID: "sty_live", Kind: "story", Status: "open"},
	}
	workflows := map[string]*mirror.Workflow{"flow": &fixtureWorkflow}
	got := engagedItems([]mirror.Seat{
		{ID: "sto_a/s1", Item: "sto_a", Session: "s1"},
		{ID: "sto_a/s2", Item: "sto_a", Session: "s2"},
		{ID: "sto_done/s1", Item: "sto_done", Session: "s1"},
		{ID: "exe_1/s1", Item: "exe_1", Session: "s1"},
		{ID: "tas_b/s1", Item: "tas_b", Session: "s1"},
		{ID: "sty_live", StorySeat: true},
		{ID: "sty_stale", StorySeat: true, Stale: true},
		{ID: "sty_other"},
	}, stories, workflows)
	if want := []string{"sto_a", "sty_live"}; !slices.Equal(got, want) {
		t.Fatalf("engaged = %v, want %v", got, want)
	}
	if n := len(engagedItems(nil, nil, nil)); n != 0 {
		t.Fatalf("nil seats = %d", n)
	}
}

// The backlog count is the stories whose status is backlog.
func TestBacklogCount(t *testing.T) {
	if n := backlogCount([]Item{{Status: "backlog"}, {Status: "alpha"}, {Status: "backlog"}}); n != 2 {
		t.Fatalf("backlog = %d, want 2", n)
	}
	if n := backlogCount(nil); n != 0 {
		t.Fatalf("nil stories = %d", n)
	}
}
