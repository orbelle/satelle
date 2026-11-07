package item

import (
	"slices"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func qev(t *testing.T, kind string, payload any) ledger.Event {
	t.Helper()
	e, err := ledger.NewEventWith("e", time.Unix(1, 0), "que_1", kind, "me", payload, "")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestFoldQueue(t *testing.T) {
	st := FoldQueue([]ledger.Event{
		qev(t, KindQueueAdded, QueueEntry{Item: "a", Cause: "d:x->y"}),
		qev(t, KindQueueAdded, QueueEntry{Item: "b"}),
		qev(t, KindQueueAdded, QueueEntry{Item: "c"}),
		qev(t, KindQueueAdded, QueueEntry{Item: "a", Cause: "d:z->y"}), // re-add: moves to the end
		qev(t, KindQueueRemoved, QueueEntry{Item: "b"}),
	})
	if !slices.Equal(st.Explicit, []string{"c", "a"}) || st.LastFire != nil {
		t.Fatalf("fold %+v", st)
	}
	if st.Causes["a"] != "d:z->y" || st.Causes["c"] != "" || len(st.Causes) != 2 {
		t.Fatalf("causes %v", st.Causes)
	}
	st = FoldQueue([]ledger.Event{
		qev(t, KindQueueAdded, QueueEntry{Item: "a"}),
		qev(t, KindQueueFired, QueueFire{Run: "exe_1", Task: "tas_1", Cause: "by hand"}),
		qev(t, KindQueueAdded, QueueEntry{Item: "d"}),
	})
	if !slices.Equal(st.Explicit, []string{"d"}) || st.LastFire == nil || st.LastFire.Run != "exe_1" || st.LastFire.Actor != "me" {
		t.Fatalf("fold after fire %+v", st)
	}
}

func TestFoldQueueRemembersAdds(t *testing.T) {
	st := FoldQueue([]ledger.Event{
		qev(t, KindQueueAdded, QueueEntry{Item: "a", Cause: "c1"}),
		qev(t, KindQueueFired, QueueFire{Run: "exe_1"}),
		qev(t, KindQueueRemoved, QueueEntry{Item: "a"}),
	})
	if !st.AddedWith("a", "c1") || st.AddedWith("a", "c2") || st.AddedWith("b", "c1") {
		t.Fatalf("added a/c1=%v a/c2=%v b/c1=%v", st.AddedWith("a", "c1"), st.AddedWith("a", "c2"), st.AddedWith("b", "c1"))
	}
	if len(st.Explicit) != 0 {
		t.Fatalf("explicit %v", st.Explicit)
	}
	if (QueueState{}).AddedWith("a", "c1") {
		t.Fatal("zero state reports an add")
	}
}

func TestMembers(t *testing.T) {
	q := Item{ID: "que_1", Inputs: &Selection{Status: "approved"}, Trigger: &Trigger{Task: "tas_1"}}
	cands := []Item{
		{ID: "sto_a", Status: "approved"},
		{ID: "sto_b", Status: "approved"},
		{ID: "sto_c", Status: "backlog"},
		{ID: "que_1", Status: "approved"},                  // the queue
		{ID: "exe_q", Status: "approved", Parent: "que_1"}, // the queue's run
		{ID: "tas_1", Status: "approved"},                  // the trigger task
		{ID: "exe_t", Status: "approved", Parent: "tas_1"}, // the task's run
	}
	got := Members(q, QueueState{Explicit: []string{"sto_c", "sto_b"}}, cands, nil).Members
	if want := []string{"sto_c", "sto_b", "sto_a"}; !slices.Equal(got, want) {
		t.Fatalf("members %v, want %v", got, want)
	}
	q.Inputs = nil
	if got := Members(q, QueueState{Explicit: []string{"sto_c"}}, cands, nil).Members; !slices.Equal(got, []string{"sto_c"}) {
		t.Fatalf("explicit only %v", got)
	}
}

// statusOf is a MemberStatus over fixed statuses; done is the terminal state.
func statusOf(m map[string]string) MemberStatus {
	return func(id string) (string, bool, bool) {
		s, ok := m[id]
		return s, s == "done", ok
	}
}

func TestMembersLapse(t *testing.T) {
	q := Item{ID: "que_1"}
	state := QueueState{Explicit: []string{"sto_a"}, Causes: map[string]string{"sto_a": "d:review->approved"}}

	// A state-enqueued member lapses once its item leaves that state.
	got := Members(q, state, nil, statusOf(map[string]string{"sto_a": "in_progress"}))
	if len(got.Members) != 0 || !slices.Equal(got.Lapsed, []Lapse{{ID: "sto_a", Status: "in_progress"}}) {
		t.Fatalf("sent back: %+v", got)
	}
	got = Members(q, state, nil, statusOf(map[string]string{"sto_a": "approved"}))
	if !slices.Equal(got.Members, []string{"sto_a"}) || len(got.Lapsed) != 0 {
		t.Fatalf("still approved: %+v", got)
	}
	// An unknown item has not lapsed.
	if got = Members(q, state, nil, statusOf(nil)); !slices.Equal(got.Members, []string{"sto_a"}) {
		t.Fatalf("unknown: %+v", got)
	}
	// A filter that selects the lapsed item keeps it a member, not lapsed.
	fq := Item{ID: "que_1", Inputs: &Selection{Status: "in_progress"}}
	cands := []Item{{ID: "sto_a", Status: "in_progress"}}
	got = Members(fq, state, cands, statusOf(map[string]string{"sto_a": "in_progress"}))
	if !slices.Equal(got.Members, []string{"sto_a"}) || len(got.Lapsed) != 0 {
		t.Fatalf("filter member: %+v", got)
	}

	// A hand or external member lapses only at a terminal state.
	for _, c := range []struct {
		cause, status string
		lapse         bool
	}{
		{"", "in_progress", false},
		{"", "done", true},
		{"server:event", "done", true},
		{"server:event", "backlog", false},
	} {
		st := QueueState{Explicit: []string{"sto_h"}, Causes: map[string]string{"sto_h": c.cause}}
		got := Members(q, st, nil, statusOf(map[string]string{"sto_h": c.status}))
		if c.lapse != (len(got.Members) == 0) || c.lapse != slices.Equal(got.Lapsed, []Lapse{{ID: "sto_h", Status: c.status}}) {
			t.Errorf("cause %q status %s: %+v", c.cause, c.status, got)
		}
	}
}

func TestStateCause(t *testing.T) {
	c := StateCause("delivery", "in_progress", "approved")
	if c != "delivery:in_progress->approved" {
		t.Fatalf("cause %q", c)
	}
	if s, ok := EnqueuedState(c); !ok || s != "approved" {
		t.Fatalf("enqueued state %q %v", s, ok)
	}
	for _, c := range []string{"", "by hand", "enqueue", "a->b", "server:event"} {
		if s, ok := EnqueuedState(c); ok {
			t.Errorf("%q read as a state cause (%q)", c, s)
		}
	}
}

func TestTriggerValidate(t *testing.T) {
	for _, c := range []struct {
		t  Trigger
		ok bool
	}{
		{Trigger{Task: "tas_1"}, true},
		{Trigger{Workflow: "w", MinSize: 3}, true},
		{Trigger{}, false},
		{Trigger{Task: "tas_1", Workflow: "w"}, false},
		{Trigger{Task: "tas_1", MinSize: -1}, false},
	} {
		if err := c.t.Validate(); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.t, err)
		}
	}
}

func TestZeroSelectionClearsInputs(t *testing.T) {
	it := Item{Inputs: &Selection{Kind: "story"}}
	Fields{Inputs: &Selection{}}.Apply(&it)
	if it.Inputs != nil {
		t.Fatalf("inputs %+v", it.Inputs)
	}
}
