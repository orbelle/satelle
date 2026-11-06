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
		qev(t, KindQueueAdded, QueueEntry{Item: "a"}),
		qev(t, KindQueueAdded, QueueEntry{Item: "b"}),
		qev(t, KindQueueAdded, QueueEntry{Item: "a"}), // duplicate
		qev(t, KindQueueAdded, QueueEntry{Item: "c"}),
		qev(t, KindQueueRemoved, QueueEntry{Item: "b"}),
	})
	if !slices.Equal(st.Explicit, []string{"a", "c"}) || st.LastFire != nil {
		t.Fatalf("fold %+v", st)
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
	got := Members(q, QueueState{Explicit: []string{"sto_c", "sto_b"}}, cands)
	if want := []string{"sto_c", "sto_b", "sto_a"}; !slices.Equal(got, want) {
		t.Fatalf("members %v, want %v", got, want)
	}
	q.Inputs = nil
	if got := Members(q, QueueState{Explicit: []string{"sto_c"}}, cands); !slices.Equal(got, []string{"sto_c"}) {
		t.Fatalf("explicit only %v", got)
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
