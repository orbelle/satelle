package uipush

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func TestQueueRowsAndTopic(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := cache.Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	create := func(id, kind string, f item.Fields) {
		f.Kind, f.Title, f.Status, f.Workflow = &kind, &id, ptrTo("open"), ptrTo("w")
		if _, err := l.Append(id, item.KindCreated, "me", f); err != nil {
			t.Fatal(err)
		}
	}
	create("que_1", item.QueueKind, item.Fields{Inputs: &item.Selection{Status: "open"}, Trigger: &item.Trigger{Task: "tas_1", MinSize: 2}})
	create("sto_1", "story", item.Fields{})
	create("exe_1", "execution", item.Fields{})
	create("tas_1", "task", item.Fields{})
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	src := Source{Ledger: l, Cache: c, RepoKey: "rk", Slug: "p"}
	snap, err := Delta(src, []string{"que_1", "sto_1"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(snap.MergeKinds, "queue") || len(snap.Queues) != 1 || len(snap.Stories) != 1 {
		t.Fatalf("delta merge %v queues %d stories %d", snap.MergeKinds, len(snap.Queues), len(snap.Stories))
	}
	var row mirror.Item
	if err := json.Unmarshal(snap.Queues[0], &row); err != nil {
		t.Fatal(err)
	}
	if row.Inputs == nil || row.Inputs.Status != "open" || row.Trigger == nil || row.Trigger.Task != "tas_1" || row.Trigger.MinSize != 2 {
		t.Fatalf("queue row %+v", row)
	}
	for id, want := range map[string]string{"que_1": "queues", "sto_1": "stories", "exe_1": "tasks", "tas_1": "tasks"} {
		if got := topics(src, []string{id}); !slices.Equal(got, []string{want}) {
			t.Errorf("%s: topics %v, want %s", id, got, want)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }
