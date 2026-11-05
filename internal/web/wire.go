package web

import (
	"sort"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// Item is the item row a client pushes into a mirror partition.
type Item = mirror.Item

// Entry is the ledger_event row a client pushes into a partition.
type Entry = mirror.Event

// sortEntries orders es oldest-first: by At, then by ID.
func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].At.Equal(es[j].At) {
			return es[i].ID < es[j].ID
		}
		return es[i].At.Before(es[j].At)
	})
}

// docEntry is a doc row a client pushes into a partition.
type docEntry = mirror.Doc

// workflowRowVM is one Workflow panel row.
type workflowRowVM struct {
	Name    string
	Source  string
	States  int
	Updated time.Time
}
