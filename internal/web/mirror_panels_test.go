package web

import (
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// engagements marks each story in a non-terminal state by its seats'
// standings: a seat here or unverified marks it engaged here (verified when
// any seat is here), a seat elsewhere adds a mark per place, a gone seat adds
// nothing. A legacy row counts only as a non-stale story seat. A task's or
// task run's seat and a seat on a terminal story give nothing, and the counts
// read the same marks.
func TestEngagements(t *testing.T) {
	stories := []Item{
		{ID: "sto_a", Kind: "story", Status: "beta", Workflow: "flow"},
		{ID: "sto_unv", Kind: "story", Status: "beta", Workflow: "flow"},
		{ID: "sto_gone", Kind: "story", Status: "beta", Workflow: "flow"},
		{ID: "sto_far", Kind: "story", Status: "beta", Workflow: "flow"},
		{ID: "sto_done", Kind: "story", Status: "zed", Workflow: "flow"},
		{ID: "sty_live", Kind: "story", Status: "open"},
	}
	workflows := map[string]*mirror.Workflow{"flow": &fixtureWorkflow}
	standing := func(s mirror.Seat) (seatlife.Standing, seatlife.Evidence) {
		switch s.Session {
		case "s-unv":
			return seatlife.Unverified, "no machine recorded"
		case "s-gone":
			return seatlife.Gone, "pid 1 has exited"
		case "s-far", "s-node":
			return seatlife.Elsewhere, "elsewhere"
		}
		return seatlife.Here, "pid 2"
	}
	eng := engagements([]mirror.Seat{
		{ID: "sto_a/s1", Item: "sto_a", Session: "s1", User: "bob@example.com"},
		{ID: "sto_a/s-unv", Item: "sto_a", Session: "s-unv"},
		{ID: "sto_a/s-far", Item: "sto_a", Session: "s-far", Location: "local:/x", Machine: "m-other", Host: "other-host", User: "ann@example.com"},
		{ID: "sto_a/s-node", Item: "sto_a", Session: "s-node", Location: "remote:nod_7"},
		{ID: "sto_unv/s-unv", Item: "sto_unv", Session: "s-unv"},
		{ID: "sto_gone/s-gone", Item: "sto_gone", Session: "s-gone"},
		{ID: "sto_far/s-far", Item: "sto_far", Session: "s-far", Location: "local:/x", Machine: "m-other", Host: "other-host"},
		{ID: "sto_done/s1", Item: "sto_done", Session: "s1"},
		{ID: "exe_1/s1", Item: "exe_1", Session: "s1"},
		{ID: "tas_b/s1", Item: "tas_b", Session: "s1"},
		{ID: "sty_live", StorySeat: true},
		{ID: "sty_stale", StorySeat: true, Stale: true},
		{ID: "sty_other"},
	}, stories, workflows, standing)

	type want struct {
		here       bool
		unverified bool
		elsewhere  []string
	}
	for id, w := range map[string]want{
		"sto_a":    {here: true, elsewhere: []string{"ann@other-host", "remote:nod_7"}},
		"sto_unv":  {here: true, unverified: true},
		"sto_far":  {elsewhere: []string{"other-host"}},
		"sty_live": {here: true},
	} {
		e := eng[id]
		if e == nil {
			t.Fatalf("%s is not engaged", id)
		}
		if (e.Here != nil) != w.here || (e.Here != nil && e.Here.Unverified != w.unverified) {
			t.Errorf("%s here mark = %+v, want here %v unverified %v", id, e.Here, w.here, w.unverified)
		}
		var labels []string
		for _, m := range e.Elsewhere {
			labels = append(labels, m.Label)
		}
		if !slices.Equal(labels, w.elsewhere) {
			t.Errorf("%s elsewhere = %v, want %v", id, labels, w.elsewhere)
		}
	}
	if a := eng["sto_a"]; a != nil && a.Here != nil {
		for _, part := range []string{"engaged here by bob@example.com, session s1 (pid 2)", "engaged here, unverified: no machine recorded, by an unrecorded user, session s-unv"} {
			if !strings.Contains(a.Here.Title, part) {
				t.Errorf("sto_a here title %q lacks %q", a.Here.Title, part)
			}
		}
		if got := a.Attr(); got != "here,elsewhere" {
			t.Errorf("sto_a data-engaged = %q", got)
		}
	}
	for _, id := range []string{"sto_gone", "sto_done", "exe_1", "tas_b", "sty_stale", "sty_other"} {
		if eng[id] != nil {
			t.Errorf("%s is engaged: %+v", id, eng[id])
		}
	}
	here, elsewhere := engagedCounts(eng, stories)
	if want := []string{"sto_a", "sto_unv", "sty_live"}; !slices.Equal(here, want) {
		t.Errorf("engaged here = %v, want %v", here, want)
	}
	if want := []string{"sto_a", "sto_far"}; !slices.Equal(elsewhere, want) {
		t.Errorf("engaged elsewhere = %v, want %v", elsewhere, want)
	}
	if n := len(engagements(nil, nil, nil, nil)); n != 0 {
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
