//go:build !windows

package ledgerrepl

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
)

// fsObjects is the reference adapter over a fresh root.
func fsObjects(t *testing.T) ledgerstore.Command {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "adapters", "satelle-store-fs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return ledgerstore.Command{Path: "sh", Args: []string{p, "--root", root}, Dir: root}
}

func push(t *testing.T, s *ledger.Store, o Objects) (Result, string) {
	t.Helper()
	var out bytes.Buffer
	res, err := repl(s, o, &out).Push(ctx)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out.String())
	}
	return res, out.String()
}

func TestPushSameHashContinues(t *testing.T) {
	o := fsObjects(t)
	a, b := newStore(t), newStore(t)
	add(t, a, "it", "a", 3)
	if res, out := push(t, a, o); res.Events != 3 || res.Failed() {
		t.Fatalf("A's push %+v\n%s", res, out)
	}
	copyChain(t, a, b, "it")
	add(t, b, "it", "b", 2)
	res, out := push(t, b, o)
	if res.Failed() || res.Events != 2 || res.Items != 1 {
		t.Fatalf("B's push %+v\n%s", res, out)
	}
	if got := seqsOf(t, o, "it"); !slices.Equal(got, upTo(5)) {
		t.Fatalf("store holds %v", got)
	}
	for i, e := range chain(t, b, "it") {
		if h := storeHash(t, o, "it", i); h != e.Hash {
			t.Errorf("seq %d: store %s, B %s", i, h, e.Hash)
		}
	}
}

func TestPushContinuesPastEqualEventsOnAStaleListing(t *testing.T) {
	o := newMem()
	a, b := newStore(t), newStore(t)
	add(t, a, "it", "a", 3)
	push(t, a, o)
	for k := range o.objs {
		o.hidden[k] = true // B's listing predates A's push
	}
	copyChain(t, a, b, "it")
	add(t, b, "it", "b", 2)
	res, out := push(t, b, o)
	if res.Failed() || res.Events != 2 {
		t.Fatalf("B's push %+v\n%s", res, out)
	}
	for i, e := range chain(t, b, "it") {
		if h := storeHash(t, o, "it", i); h != e.Hash {
			t.Errorf("seq %d: store %s, B %s", i, h, e.Hash)
		}
	}
}

func TestPushStopsAtADifferentHash(t *testing.T) {
	o := fsObjects(t)
	a, b := newStore(t), newStore(t)
	add(t, a, "it", "s", 2)
	push(t, a, o)
	copyChain(t, a, b, "it")
	add(t, a, "it", "a", 1)
	push(t, a, o)
	add(t, b, "it", "b", 2)
	add(t, b, "other", "o", 2)
	res, out := push(t, b, o)
	if len(res.Diverged) != 1 || res.Diverged[0].Item != "it" || res.Diverged[0].Seq != 2 {
		t.Fatalf("divergence %+v\n%s", res.Diverged, out)
	}
	if !strings.Contains(out, "diverged: it at event 2: ") || !strings.Contains(out, "not pushed past it") {
		t.Errorf("output:\n%s", out)
	}
	if h := storeHash(t, o, "it", 2); h != chain(t, a, "it")[2].Hash {
		t.Errorf("the store's event 2 is no longer A's: %s", h)
	}
	if got := seqsOf(t, o, "it"); !slices.Equal(got, upTo(3)) {
		t.Errorf("store holds %v of it; B pushed past the fork", got)
	}
	if got := seqsOf(t, o, "other"); !slices.Equal(got, upTo(2)) {
		t.Errorf("the other item was not pushed: %v", got)
	}
	if res.Events != 2 {
		t.Errorf("events written %d, want the other item's 2", res.Events)
	}
}

func TestPushRefusesAnItemWithASequenceHole(t *testing.T) {
	o := newMem()
	a := newStore(t)
	add(t, a, "it", "a", 3)
	push(t, a, o)
	delete(o.objs, ChainKey("it", 1))
	o.objs["chains/stray.txt"] = []byte("x")
	add(t, a, "it", "a", 1)
	res, out := push(t, a, o)
	if len(res.Refused) != 1 || !strings.Contains(out, "the store's chain it has no event at 1") {
		t.Fatalf("%+v\n%s", res, out)
	}
	if !strings.Contains(out, "store key chains/stray.txt is not a chain event key; skipped") {
		t.Errorf("the stray key is not named:\n%s", out)
	}
	if _, ok := o.objs[ChainKey("it", 3)]; ok {
		t.Error("push wrote past the hole")
	}
}
