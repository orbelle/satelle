package item

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

const QueueKind = "queue"

const (
	KindQueueAdded   = "queue.added"
	KindQueueRemoved = "queue.removed"
	KindQueueFired   = "queue.fired"
)

type Trigger struct {
	Task     string `json:"task,omitempty" toml:"task"`
	Workflow string `json:"workflow,omitempty" toml:"workflow"`
	Base     string `json:"base,omitempty" toml:"base"`
	MinSize  int    `json:"min_size" toml:"min_size"`
}

func (t Trigger) Validate() error {
	if (t.Task == "") == (t.Workflow == "") {
		return errors.New("a trigger names exactly one of a task and a workflow")
	}
	if t.MinSize < 0 {
		return errors.New("a trigger's minimum size is 0 (fires only on queue fire) or more")
	}
	return nil
}

type QueueEntry struct {
	Item  string `json:"item"`
	Cause string `json:"cause,omitempty"`
}

type QueueFire struct {
	Run      string `json:"run"`
	Task     string `json:"task,omitempty"`
	Workflow string `json:"workflow,omitempty"`
	Cause    string `json:"cause,omitempty"`
}

type Fired struct {
	QueueFire
	At    time.Time `json:"at"`
	Actor string    `json:"actor,omitempty"`
}

type QueueState struct {
	Explicit []string          `json:"explicit"`
	Causes   map[string]string `json:"causes,omitempty"`
	LastFire *Fired            `json:"last_fire,omitempty"`
	added    map[QueueEntry]bool
}

func (s QueueState) AddedWith(id, cause string) bool {
	return s.added[QueueEntry{Item: id, Cause: cause}]
}

func FoldQueue(events []ledger.Event) QueueState {
	st := QueueState{Explicit: []string{}, Causes: map[string]string{}, added: map[QueueEntry]bool{}}
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
				st.added[QueueEntry{Item: q.Item, Cause: q.Cause}] = true
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

func StateCause(workflow, from, to string) string {
	return fmt.Sprintf("%s:%s->%s", workflow, from, to)
}

func EnqueuedState(cause string) (state string, ok bool) {
	i := strings.LastIndex(cause, "->")
	if i < 0 || !strings.Contains(cause[:i], ":") || cause[i+2:] == "" {
		return "", false
	}
	return cause[i+2:], true
}

type MemberStatus func(id string) (status string, terminal bool, ok bool)

type Lapse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type Membership struct {
	Members []string
	Lapsed  []Lapse
}

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
