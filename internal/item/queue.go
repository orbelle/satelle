package item

import (
	"errors"
	"fmt"
	"slices"
	"strings"
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

// QueueState is a queue's chain folded: its explicit members in add order,
// the cause each was last added with, and its last fire.
type QueueState struct {
	Explicit []string          `json:"explicit"`
	Causes   map[string]string `json:"causes,omitempty"`
	LastFire *Fired            `json:"last_fire,omitempty"`
}

// FoldQueue folds a queue's chain. An add appends an item and records its
// cause; adding an item already explicit moves it to the end with the new
// cause. A remove deletes it, and a fire takes every member, so it empties the
// explicit list.
func FoldQueue(events []ledger.Event) QueueState {
	st := QueueState{Explicit: []string{}, Causes: map[string]string{}}
	drop := func(id string) {
		st.Explicit = slices.DeleteFunc(st.Explicit, func(e string) bool { return e == id })
		delete(st.Causes, id)
	}
	for _, e := range events {
		switch e.Kind {
		case KindQueueAdded:
			var q QueueEntry
			if e.Decode(&q) == nil && q.Item != "" {
				drop(q.Item)
				st.Explicit = append(st.Explicit, q.Item)
				st.Causes[q.Item] = q.Cause
			}
		case KindQueueRemoved:
			var q QueueEntry
			if e.Decode(&q) == nil {
				drop(q.Item)
			}
		case KindQueueFired:
			var f QueueFire
			if e.Decode(&f) == nil {
				st.Explicit = []string{}
				st.Causes = map[string]string{}
				st.LastFire = &Fired{QueueFire: f, At: e.At, Actor: e.Actor}
			}
		}
	}
	return st
}

// StateCause is the cause a state's enqueue records: the transition that
// entered the state, "<workflow>:<from>-><to>". It is the only writer of the
// format EnqueuedState reads.
func StateCause(workflow, from, to string) string {
	return fmt.Sprintf("%s:%s->%s", workflow, from, to)
}

// EnqueuedState is the state a state cause entered; ok is false for any other
// cause (a hand add's "" or an external producer's text).
func EnqueuedState(cause string) (state string, ok bool) {
	i := strings.LastIndex(cause, "->")
	if i < 0 || !strings.Contains(cause[:i], ":") || cause[i+2:] == "" {
		return "", false
	}
	return cause[i+2:], true
}

// MemberStatus resolves an explicit member's current status and whether it is
// a terminal state of its workflow; ok is false when the item is unknown.
type MemberStatus func(id string) (status string, terminal bool, ok bool)

// Lapse is an explicit member that no longer counts: its item left the state
// that enqueued it, or (added by hand or an external cause) reached a terminal
// state. Status is the item's status now.
type Lapse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Membership is a queue's members now and the explicit members that lapsed.
type Membership struct {
	Members []string
	Lapsed  []Lapse
}

// lapsed reports whether an explicit member added with cause has lapsed, and
// the status it lapsed at. A member whose item cannot be resolved has not.
func lapsed(id, cause string, status MemberStatus) (string, bool) {
	if status == nil {
		return "", false
	}
	s, terminal, ok := status(id)
	if !ok {
		return "", false
	}
	if state, isState := EnqueuedState(cause); isState {
		return s, s != state
	}
	return s, terminal
}

// Members returns the queue's members: its live explicit items first, then
// the candidates its filter selects, without duplicates. An explicit member
// whose item left the state that enqueued it, or (added by hand or by an
// external cause) reached a terminal state, has lapsed: it is not a member
// unless the filter selects it, and is listed in Lapsed otherwise. A nil
// status lapses nothing. The queue itself, its children, the trigger's task
// and that task's children are never members (the exclusion a task run
// applies to its own inputs).
func Members(q Item, st QueueState, candidates []Item, status MemberStatus) Membership {
	excluded := func(id, parent string) bool {
		if id == q.ID || parent == q.ID {
			return true
		}
		if t := q.Trigger; t != nil && t.Task != "" && (id == t.Task || parent == t.Task) {
			return true
		}
		return false
	}
	out := Membership{Members: []string{}, Lapsed: []Lapse{}}
	var gone []Lapse
	for _, id := range st.Explicit {
		if id == q.ID || slices.Contains(out.Members, id) {
			continue
		}
		if s, ok := lapsed(id, st.Causes[id], status); ok {
			gone = append(gone, Lapse{ID: id, Status: s})
			continue
		}
		out.Members = append(out.Members, id)
	}
	if q.Inputs != nil {
		for _, c := range candidates {
			if q.Inputs.Matches(c) && !excluded(c.ID, c.Parent) && !slices.Contains(out.Members, c.ID) {
				out.Members = append(out.Members, c.ID)
			}
		}
	}
	for _, l := range gone {
		if !slices.Contains(out.Members, l.ID) {
			out.Lapsed = append(out.Lapsed, l)
		}
	}
	return out
}
