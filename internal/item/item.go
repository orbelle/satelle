package item

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

const (
	KindCreated       = "item.created"
	KindUpdated       = "item.updated"
	KindTransitioned  = "item.transitioned"
	KindSeatBound     = "seat.bound"
	KindSeatReleased  = "seat.released"
	KindAttachmentAdd = "attachment.added"
)

const KindPerformerRun = "performer.run"

const (
	LocationLocal  = "local:"
	LocationRemote = "remote:"
	LocationCloud  = "cloud:"
)

func Folded(kind string) bool {
	switch kind {
	case KindCreated, KindUpdated, KindTransitioned, KindSeatBound, KindSeatReleased, KindAttachmentAdd:
		return true
	}
	return false
}

func SinceEntered(events []ledger.Event) []ledger.Event {
	start := 0
	for i, e := range events {
		if e.Kind == KindTransitioned {
			start = i + 1
		}
	}
	return events[start:]
}

type Selection struct {
	Kind     string `json:"kind,omitempty" toml:"kind"`
	Status   string `json:"status,omitempty" toml:"status"`
	Tag      string `json:"tag,omitempty" toml:"tag"`
	Category string `json:"category,omitempty" toml:"category"`
}

func (s Selection) Matches(it Item) bool {
	return (s.Kind == "" || s.Kind == it.Kind) &&
		(s.Status == "" || s.Status == it.Status) &&
		(s.Category == "" || s.Category == it.Category) &&
		(s.Tag == "" || slices.Contains(it.Tags, s.Tag))
}

type Item struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Body      string     `json:"body,omitempty"`
	Criteria  []string   `json:"criteria,omitempty"`
	Status    string     `json:"status"`
	Tags      []string   `json:"tags,omitempty"`
	Parent    string     `json:"parent,omitempty"`
	Category  string     `json:"category,omitempty"`
	Workflow  string     `json:"workflow"`
	Assignees []string   `json:"assignees,omitempty"`
	Inputs    *Selection `json:"inputs,omitempty"`
	Trigger   *Trigger   `json:"trigger,omitempty"`
	Base      string     `json:"base,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type Fields struct {
	Kind      *string    `json:"kind,omitempty"`
	Title     *string    `json:"title,omitempty"`
	Body      *string    `json:"body,omitempty"`
	Criteria  *[]string  `json:"criteria,omitempty"`
	Status    *string    `json:"status,omitempty"`
	Tags      *[]string  `json:"tags,omitempty"`
	Parent    *string    `json:"parent,omitempty"`
	Category  *string    `json:"category,omitempty"`
	Workflow  *string    `json:"workflow,omitempty"`
	Assignees *[]string  `json:"assignees,omitempty"`
	Assignee  *string    `json:"assignee,omitempty"`
	Inputs    *Selection `json:"inputs,omitempty"`
	Trigger   *Trigger   `json:"trigger,omitempty"`
	Base      *string    `json:"base,omitempty"`
}

func Placement(workflow, from, start string) (Fields, Move) {
	return Fields{Workflow: &workflow}, Move{From: from, To: start}
}

type Move struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Hold   *Hold  `json:"hold,omitempty"`
	Change *Span  `json:"change,omitempty"`
}

type Span struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

type Hold struct {
	State    string    `json:"state"`
	Rounds   int       `json:"rounds"`
	Dissent  []Dissent `json:"dissent"`
	Accepted []string  `json:"accepted"`
}

type Dissent struct {
	Gate        string `json:"gate"`
	Binding     string `json:"binding,omitempty"`
	Rejected    int    `json:"rejected"`
	Class       string `json:"class"`
	ClassRounds int    `json:"class_rounds"`
}

func (d Dissent) Key() string { return VerdictKey(d.Gate, d.Binding) }

func VerdictKey(gate, binding string) string {
	if binding == "" {
		return gate
	}
	return gate + "/" + binding
}

func (f Fields) Apply(it *Item) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&it.Kind, f.Kind)
	set(&it.Title, f.Title)
	set(&it.Body, f.Body)
	set(&it.Status, f.Status)
	set(&it.Parent, f.Parent)
	set(&it.Category, f.Category)
	set(&it.Workflow, f.Workflow)
	set(&it.Base, f.Base)
	if f.Criteria != nil {
		it.Criteria = slices.Clone(*f.Criteria)
	}
	if f.Tags != nil {
		it.Tags = slices.Clone(*f.Tags)
	}
	switch {
	case f.Assignees != nil:
		it.Assignees = slices.Clone(*f.Assignees)
	case f.Assignee != nil && *f.Assignee != "":
		it.Assignees = []string{*f.Assignee}
	case f.Assignee != nil:
		it.Assignees = nil
	}
	if f.Inputs != nil {
		if *f.Inputs == (Selection{}) {
			it.Inputs = nil
		} else {
			in := *f.Inputs
			it.Inputs = &in
		}
	}
	if f.Trigger != nil {
		if *f.Trigger == (Trigger{}) {
			it.Trigger = nil
		} else {
			t := *f.Trigger
			it.Trigger = &t
		}
	}
}

func Fold(events []ledger.Event) (Item, error) {
	var it Item
	for i, e := range events {
		switch e.Kind {
		case KindCreated:
			if i != 0 {
				return it, fmt.Errorf("item: %s: %s at position %d", e.Item, KindCreated, i)
			}
			var f Fields
			if err := e.Decode(&f); err != nil {
				return it, fmt.Errorf("item: %s: %w", e.Item, err)
			}
			it = Item{ID: e.Item, CreatedAt: e.At}
			f.Apply(&it)
		case KindUpdated:
			var f Fields
			if err := e.Decode(&f); err != nil {
				return it, fmt.Errorf("item: %s: %w", e.Item, err)
			}
			f.Status = nil
			f.Apply(&it)
		case KindTransitioned:
			var m Move
			if err := e.Decode(&m); err != nil {
				return it, fmt.Errorf("item: %s: %w", e.Item, err)
			}
			it.Status = m.To
		default:
			if i == 0 {
				return it, fmt.Errorf("item: %s: chain starts with %s, not %s", e.Item, e.Kind, KindCreated)
			}
			continue
		}
		it.UpdatedAt = e.At
	}
	if len(events) == 0 {
		return it, fmt.Errorf("item: empty chain")
	}
	return it, nil
}

func PriorStatus(events []ledger.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == KindTransitioned {
			var m Move
			if events[i].Decode(&m) == nil {
				return m.From
			}
		}
	}
	return ""
}

func NewID(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
