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

// The backlog count is the stories in their workflow's start state, whatever
// that state is called; a status named "backlog" that is not the start state,
// and a story whose workflow is unpushed, are not counted.
func TestBacklogCount(t *testing.T) {
	queued := mirror.Workflow{Name: "queue", States: []mirror.WorkflowState{
		{Name: "queued", Start: true}, {Name: "backlog"}, {Name: "fin", Terminal: true},
	}}
	workflows := map[string]*mirror.Workflow{"flow": &fixtureWorkflow, "queue": &queued}
	stories := []Item{
		{ID: "sto_a", Status: "alpha", Workflow: "flow"},
		{ID: "sto_b", Status: "beta", Workflow: "flow"},
		{ID: "sto_q", Status: "queued", Workflow: "queue"},
		{ID: "sto_named", Status: "backlog", Workflow: "queue"},
		{ID: "sto_fin", Status: "fin", Workflow: "queue"},
		{ID: "sto_lost", Status: "alpha", Workflow: "unpushed"},
	}
	if n := backlogCount(stories, workflows); n != 2 {
		t.Fatalf("backlog = %d, want 2 (sto_a, sto_q)", n)
	}
	if n := backlogCount(nil, nil); n != 0 {
		t.Fatalf("nil stories = %d", n)
	}
}
