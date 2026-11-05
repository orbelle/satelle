// Package item folds ledger events into the kernel's records: work items,
// seats and attachments. Folds are pure functions of an item's chain.
package item

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/bobmcallan/satelle/internal/kledger"
)

// Event kinds the folds read. Every other kind is carried, never read.
const (
	KindCreated       = "item.created"
	KindUpdated       = "item.updated"
	KindTransitioned  = "item.transitioned"
	KindSeatBound     = "seat.bound"
	KindSeatReleased  = "seat.released"
	KindAttachmentAdd = "attachment.added"
)

// Selection names a set of items by field; empty fields match anything. A
// task item carries one as its inputs.
type Selection struct {
	Kind     string `json:"kind,omitempty" toml:"kind"`
	Status   string `json:"status,omitempty" toml:"status"`
	Tag      string `json:"tag,omitempty" toml:"tag"`
	Category string `json:"category,omitempty" toml:"category"`
}

// Matches reports whether it is selected by s.
func (s Selection) Matches(it Item) bool {
	return (s.Kind == "" || s.Kind == it.Kind) &&
		(s.Status == "" || s.Status == it.Status) &&
		(s.Category == "" || s.Category == it.Category) &&
		(s.Tag == "" || slices.Contains(it.Tags, s.Tag))
}

// Item is the folded state of a work item.
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
	Assignee  string     `json:"assignee,omitempty"`
	Inputs    *Selection `json:"inputs,omitempty"`
	Base      string     `json:"base,omitempty"` // the commit the item was created at
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Fields is the payload of item.created (every field it names) and
// item.updated (only the fields it changes). Status is honoured only on
// item.created; a status change is item.transitioned.
type Fields struct {
	Kind     *string    `json:"kind,omitempty"`
	Title    *string    `json:"title,omitempty"`
	Body     *string    `json:"body,omitempty"`
	Criteria *[]string  `json:"criteria,omitempty"`
	Status   *string    `json:"status,omitempty"`
	Tags     *[]string  `json:"tags,omitempty"`
	Parent   *string    `json:"parent,omitempty"`
	Category *string    `json:"category,omitempty"`
	Workflow *string    `json:"workflow,omitempty"`
	Assignee *string    `json:"assignee,omitempty"`
	Inputs   *Selection `json:"inputs,omitempty"`
	Base     *string    `json:"base,omitempty"`
}

// Move is the payload of item.transitioned.
type Move struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Apply sets every field f names on it, status included.
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
	set(&it.Assignee, f.Assignee)
	set(&it.Base, f.Base)
	if f.Criteria != nil {
		it.Criteria = slices.Clone(*f.Criteria)
	}
	if f.Tags != nil {
		it.Tags = slices.Clone(*f.Tags)
	}
	if f.Inputs != nil {
		in := *f.Inputs
		it.Inputs = &in
	}
}

// Fold replays an item's chain. It fails when the chain does not start with
// item.created.
func Fold(events []kledger.Event) (Item, error) {
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

// PriorStatus returns the status the item held before it entered its current
// one (the from of the last transition), "" when it never moved.
func PriorStatus(events []kledger.Event) string {
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

// NewID returns prefix + "_" + 8 random hex digits.
func NewID(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
