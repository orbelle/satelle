package cache

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func ptr[T any](v T) *T { return &v }

type snapshot struct {
	Items  []item.Item
	Events []ledger.Event
	Seats  []item.Seat
	Atts   []item.Attachment
}

func snap(t *testing.T, c *Cache) snapshot {
	t.Helper()
	items, err := c.Items(Query{})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := c.Events("", "")
	seats, _ := c.Seats("", "")
	atts, _ := c.Attachments("a")
	return snapshot{items, events, seats, atts}
}

func TestSyncQueryRebuild(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.Open(dir)
	c, err := Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	l.Append("a", item.KindCreated, "me", item.Fields{Kind: ptr("k1"), Title: ptr("A"), Status: ptr("s0"), Tags: ptr([]string{"x", "y"}), Category: ptr("cat"), Workflow: ptr("w1")})
	l.Append("b", item.KindCreated, "me", item.Fields{Kind: ptr("k2"), Title: ptr("B"), Status: ptr("s0"), Parent: ptr("a"), Workflow: ptr("w2")})
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	l.Append("a", item.KindTransitioned, "me", item.Move{From: "s0", To: "s1"})
	l.Append("a", item.KindSeatBound, "me", item.Seat{Session: "sess", Location: "local:/x", Base: "h"})
	l.Append("a", item.KindAttachmentAdd, "me", item.Attachment{Name: "n", SHA256: "ab", Size: 2, MediaType: "text/plain"})
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(l); err != nil { // idempotent
		t.Fatal(err)
	}

	check := func(q Query, want ...string) {
		t.Helper()
		got, err := c.Items(q)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, it := range got {
			ids = append(ids, it.ID)
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("%+v: got %v want %v", q, ids, want)
		}
	}
	check(Query{Status: "s1"}, "a")
	check(Query{Kind: "k2"}, "b")
	check(Query{Tag: "y"}, "a")
	check(Query{Category: "cat"}, "a")
	check(Query{Parent: "a"}, "b")
	check(Query{Workflow: "w2"}, "b")
	check(Query{Tag: "nope"})

	if ev, _ := c.Events("a", item.KindTransitioned); len(ev) != 1 {
		t.Fatalf("events by kind: %d", len(ev))
	}
	if s, _ := c.Seats("sess", ""); len(s) != 1 || s[0].Item != "a" {
		t.Fatalf("seats %+v", s)
	}
	if it, ok, _ := c.Item("a"); !ok || it.Status != "s1" || len(it.Tags) != 2 {
		t.Fatalf("item %+v", it)
	}

	before := snap(t, c)
	if err := c.Rebuild(l); err != nil {
		t.Fatal(err)
	}
	after := snap(t, c)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rebuild differs:\n%+v\n%+v", before, after)
	}
	if len(after.Events) != 5 || len(after.Atts) != 1 {
		t.Fatalf("counts %d %d", len(after.Events), len(after.Atts))
	}
}
