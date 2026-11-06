package item

import (
	"errors"
	"slices"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// QueueKind is the kind of a queue item. A queue gathers items by its filter
// (Item.Inputs) and by explicit adds, and its trigger runs a task or a
// workflow over them when it fills or when fired by hand.
const QueueKind = "queue"

// Event kinds on a queue's chain.
const (
	KindQueueAdded   = "queue.added"
	KindQueueRemoved = "queue.removed"
	KindQueueFired   = "queue.fired"
)

// Trigger is what a queue runs and when. Exactly one of Task and Workflow is
// set. MinSize 0 fires only by hand; N > 0 fires when an enqueue leaves the
// queue holding N or more members. Base is the default ref a fired run's
// base comes from ("" means HEAD).
type Trigger struct {
	Task     string `json:"task,omitempty" toml:"task"`
	Workflow string `json:"workflow,omitempty" toml:"workflow"`
	Base     string `json:"base,omitempty" toml:"base"`
	MinSize  int    `json:"min_size" toml:"min_size"`
}

// Validate checks the trigger's shape (not that its target exists).
func (t Trigger) Validate() error {
	if (t.Task == "") == (t.Workflow == "") {
		return errors.New("a trigger names exactly one of a task and a workflow")
	}
	if t.MinSize < 0 {
		return errors.New("a trigger's minimum size is 0 (fire by hand) or more")
	}
	return nil
}

// QueueEntry is the payload of queue.added and queue.removed.
type QueueEntry struct {
	Item  string `json:"item"`
	Cause string `json:"cause,omitempty"`
}

// QueueFire is the payload of queue.fired: the run it started and its target.
// The run's own task.run record holds the items it took.
type QueueFire struct {
	Run      string `json:"run"`
	Task     string `json:"task,omitempty"`
	Workflow string `json:"workflow,omitempty"`
	Cause    string `json:"cause,omitempty"`
}

// Fired is the last fire of a queue.
type Fired struct {
	QueueFire
	At    time.Time `json:"at"`
	Actor string    `json:"actor,omitempty"`
}

// QueueState is a queue's chain folded: its explicit members in add order and
// its last fire.
type QueueState struct {
	Explicit []string `json:"explicit"`
	LastFire *Fired   `json:"last_fire,omitempty"`
}

// FoldQueue folds a queue's chain. An add appends an item not already
// explicit, a remove deletes it, and a fire takes every member, so it empties
// the explicit list.
func FoldQueue(events []ledger.Event) QueueState {
	st := QueueState{Explicit: []string{}}
	for _, e := range events {
		switch e.Kind {
		case KindQueueAdded:
			var q QueueEntry
			if e.Decode(&q) == nil && q.Item != "" && !slices.Contains(st.Explicit, q.Item) {
				st.Explicit = append(st.Explicit, q.Item)
			}
		case KindQueueRemoved:
			var q QueueEntry
			if e.Decode(&q) == nil {
				st.Explicit = slices.DeleteFunc(st.Explicit, func(id string) bool { return id == q.Item })
			}
		case KindQueueFired:
			var f QueueFire
			if e.Decode(&f) == nil {
				st.Explicit = []string{}
				st.LastFire = &Fired{QueueFire: f, At: e.At, Actor: e.Actor}
			}
		}
	}
	return st
}

// Members returns the queue's members: its explicit items first, then the
// candidates its filter selects, without duplicates. The queue itself, its
// children, the trigger's task and that task's children are never members
// (the exclusion a task run applies to its own inputs).
func Members(q Item, st QueueState, candidates []Item) []string {
	excluded := func(id, parent string) bool {
		if id == q.ID || parent == q.ID {
			return true
		}
		if t := q.Trigger; t != nil && t.Task != "" && (id == t.Task || parent == t.Task) {
			return true
		}
		return false
	}
	out := []string{}
	for _, id := range st.Explicit {
		if id != q.ID && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if q.Inputs == nil {
		return out
	}
	for _, c := range candidates {
		if q.Inputs.Matches(c) && !excluded(c.ID, c.Parent) && !slices.Contains(out, c.ID) {
			out = append(out, c.ID)
		}
	}
	return out
}
