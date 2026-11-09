//go:build !windows

package ledgerrepl

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func syncOnce(t *testing.T, s *ledger.Store, st Store) SyncResult {
	t.Helper()
	var out bytes.Buffer
	res, err := replOn(s, st, &out).Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out.String())
	}
	return res
}

// sameAsStore checks s holds exactly the store's items, each with the
// store's head.
func sameAsStore(t *testing.T, name string, s *ledger.Store, o Objects) {
	t.Helper()
	st := &ObjectStore{Objects: o, Name: "test"}
	lst, err := st.Chains(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := s.Items()
	if err != nil {
		t.Fatal(err)
	}
	var stored []string
	for id := range lst.Chains {
		stored = append(stored, id)
	}
	slices.Sort(stored)
	slices.Sort(ids)
	if !slices.Equal(ids, stored) {
		t.Fatalf("%s holds %v, the store %v", name, ids, stored)
	}
	for _, id := range ids {
		local := chain(t, s, id)
		if h := storeHash(t, o, id, len(local)-1); h != local[len(local)-1].Hash || lst.Chains[id].Len != len(local) {
			t.Errorf("%s's head of %s is not the store's", name, id)
		}
	}
}

func TestSyncDisjointNewItemsConverge(t *testing.T) {
	o := fsObjects(t)
	st := func() Store { return &ObjectStore{Objects: o, Name: "test"} }
	a, b := newStore(t), newStore(t)
	add(t, a, "s1", "a", 2)
	add(t, b, "s2", "b", 3)

	if res := syncOnce(t, a, st()); res.Failed() || res.Push.Events != 2 || res.Pull.Events != 0 {
		t.Fatalf("A's first pass %+v", res)
	}
	if res := syncOnce(t, b, st()); res.Failed() || res.Push.Events != 3 || res.Pull.Events != 2 {
		t.Fatalf("B's pass %+v", res)
	}
	if ids, _ := b.Items(); !slices.Contains(ids, "s1") || !slices.Contains(ids, "s2") {
		t.Fatalf("B holds %v", ids)
	}
	if res := syncOnce(t, a, st()); res.Failed() || res.Push.Events != 0 || res.Pull.Events != 3 {
		t.Fatalf("A's second pass %+v", res)
	}
	sameAsStore(t, "A", a, o)
	sameAsStore(t, "B", b, o)
	for name, s := range map[string]*ledger.Store{"A": a, "B": b} {
		if res := syncOnce(t, s, st()); res.Push.Events != 0 || res.Pull.Events != 0 || res.Failed() {
			t.Errorf("%s's idle pass wrote %+v", name, res)
		}
	}
}

// countingStore counts the calls that write to the store or read events
// from it.
type countingStore struct {
	Store
	offers, events int
}

func (c *countingStore) Offer(ctx context.Context, item string, seq int, events []ledger.Event) (int, string, error) {
	c.offers++
	return c.Store.Offer(ctx, item, seq, events)
}

func (c *countingStore) Events(ctx context.Context, item string, from, to int) ([]ledger.Event, error) {
	c.events++
	return c.Store.Events(ctx, item, from, to)
}

func TestSyncLeavesInSyncAndDivergedAlone(t *testing.T) {
	o := newMem()
	a, b := shared(t, o, 2) // "it": 2 events, pushed, held by both
	add(t, a, "same", "s", 2)
	push(t, a, o)
	copyChain(t, a, b, "same")
	add(t, a, "it", "a", 1)
	push(t, a, o)
	add(t, b, "it", "b", 1) // B forks it at 2

	before := chainFile(t, b, "it")
	storeBefore := seqsOf(t, o, "it")
	cs := &countingStore{Store: &ObjectStore{Objects: o, Name: "test"}}
	var out bytes.Buffer
	res, err := (&Replicator{Ledger: b, Store: cs, Refs: kernel.BlobRefs, Out: &out}).Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out.String())
	}
	if cs.offers != 0 || cs.events != 0 {
		t.Errorf("an in-sync and a diverged item cost %d offers and %d event reads", cs.offers, cs.events)
	}
	if len(res.Unsynced) != 1 || res.Unsynced[0].Item != "it" || res.Unsynced[0].State != "diverged at 2" {
		t.Fatalf("unsynced %+v", res.Unsynced)
	}
	if !res.Failed() {
		t.Error("a diverged item did not fail the pass")
	}
	if !bytes.Equal(before, chainFile(t, b, "it")) {
		t.Error("B's chain changed")
	}
	if got := seqsOf(t, o, "it"); !slices.Equal(got, storeBefore) {
		t.Errorf("the store's chain changed: %v", got)
	}
	if strings.Contains(out.String(), "same") {
		t.Errorf("the in-sync item was replicated:\n%s", out.String())
	}
}
