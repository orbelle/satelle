package web

import (
	"sort"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

type Item = mirror.Item

type Entry = mirror.Event

func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].At.Equal(es[j].At) {
			return es[i].ID < es[j].ID
		}
		return es[i].At.Before(es[j].At)
	})
}

type docEntry = mirror.Doc

type workflowRowVM struct {
	Name    string
	Source  string
	States  int
	Updated time.Time
}
