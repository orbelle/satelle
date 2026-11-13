package cache

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// chainOf builds a chain of item "q" from titles alone: a genesis, then one
// item.updated per further title, every event id the same length and the
// same time, so two chains of equally long titles are files of one size.
func chainOf(t *testing.T, idPrefix string, titles ...string) []ledger.Event {
	t.Helper()
	at := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	var out []ledger.Event
	prev := ""
	for i, title := range titles {
		kind, f := item.KindUpdated, item.Fields{Title: ptr(title)}
		if i == 0 {
			kind, f = item.KindCreated, item.Fields{Kind: ptr("queue"), Title: ptr(title), Status: ptr("open")}
		}
		e, err := ledger.NewEventWith(idPrefix+string(rune('a'+i)), at, "q", kind, "me", f, prev)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
		prev = e.Hash
	}
	return out
}

// TestResetItem: after a chain file is replaced in place, larger than the
// offset the cache read and then exactly that size, ResetItem folds the new
// chain and keeps no event row of the old one.
func TestResetItem(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.Open(dir)
	c, err := Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	check := func(want []ledger.Event, title string, gone []ledger.Event) {
		t.Helper()
		got, err := c.Events("q", "")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(eventIDs(got), eventIDs(want)) {
			t.Fatalf("cached events %v, want %v", eventIDs(got), eventIDs(want))
		}
		all, _ := c.Events("", "")
		for _, e := range gone {
			if slices.Contains(eventIDs(all), e.ID) {
				t.Fatalf("old event %s is still cached", e.ID)
			}
		}
		it, ok, err := c.Item("q")
		if err != nil || !ok || it.Title != title {
			t.Fatalf("cached item %+v %v %v, want title %s", it, ok, err, title)
		}
		noDuplicateIDs(t, c)
	}

	old := chainOf(t, "old_", "A")
	for _, e := range old {
		if err := l.AppendRaw(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.SyncItem(l, "q"); err != nil {
		t.Fatal(err)
	}
	check(old, "A", nil)

	// Larger: the plain sync would read on from the old offset.
	larger := chainOf(t, "new_", "B", "C", "D")
	if err := l.Replace("q", old[len(old)-1].Hash, larger); err != nil {
		t.Fatal(err)
	}
	if err := c.ResetItem(l, "q"); err != nil {
		t.Fatal(err)
	}
	check(larger, "D", old)

	// Exactly the old size: the plain sync would see nothing new.
	size, _ := l.Size("q")
	same := chainOf(t, "alt_", "E", "F", "G")
	if err := l.Replace("q", larger[len(larger)-1].Hash, same); err != nil {
		t.Fatal(err)
	}
	if now, _ := l.Size("q"); now != size {
		t.Fatalf("fixture: replaced file is %d bytes, want %d", now, size)
	}
	if err := c.SyncItem(l, "q"); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Events("q", ""); !slices.Equal(eventIDs(got), eventIDs(larger)) {
		t.Fatalf("fixture: a plain sync re-read the same-size chain: %v", eventIDs(got))
	}
	if err := c.ResetItem(l, "q"); err != nil {
		t.Fatal(err)
	}
	check(same, "G", append(old, larger...))
}
