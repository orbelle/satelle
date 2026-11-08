//go:build !windows

package ledgerrepl

import (
	"bytes"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func pull(t *testing.T, s *ledger.Store, o Objects) (Result, string) {
	t.Helper()
	var out bytes.Buffer
	res, err := repl(s, o, &out).Pull(ctx)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out.String())
	}
	return res, out.String()
}

func chainFile(t *testing.T, s *ledger.Store, it string) []byte {
	t.Helper()
	b, err := os.ReadFile(s.Path(it))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// refusesFork pulls into b and checks the fork is named at seq and b's chain
// is unchanged.
func refusesFork(t *testing.T, b *ledger.Store, o Objects, seq int) {
	t.Helper()
	before := chainFile(t, b, "it")
	res, out := pull(t, b, o)
	if len(res.Diverged) != 1 || res.Diverged[0].Item != "it" || res.Diverged[0].Seq != seq || res.Events != 0 {
		t.Fatalf("pull %+v, want a fork of it at %d\n%s", res, seq, out)
	}
	if want := "diverged: it at event " + strconv.Itoa(seq) + ": "; !strings.Contains(out, want) || !strings.Contains(out, "nothing appended") {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if !bytes.Equal(before, chainFile(t, b, "it")) {
		t.Error("the local chain changed")
	}
	if !res.Failed() {
		t.Error("a fork did not fail the pull")
	}
}

// shared returns A and B holding the same n-event chain of it, pushed.
func shared(t *testing.T, o Objects, n int) (*ledger.Store, *ledger.Store) {
	t.Helper()
	a, b := newStore(t), newStore(t)
	add(t, a, "it", "s", n)
	push(t, a, o)
	copyChain(t, a, b, "it")
	return a, b
}

func TestPullAppendsVerifiedEvents(t *testing.T) {
	o := fsObjects(t)
	a, b := shared(t, o, 2)
	add(t, a, "it", "a", 3)
	push(t, a, o)
	res, out := pull(t, b, o)
	if res.Failed() || res.Events != 3 || res.Items != 1 {
		t.Fatalf("pull %+v\n%s", res, out)
	}
	if !bytes.Equal(chainFile(t, a, "it"), chainFile(t, b, "it")) {
		t.Fatal("B's chain is not A's")
	}
	if err := b.Verify(); err != nil {
		t.Fatal(err)
	}
	// A fresh runtime gets the whole chain.
	c := newStore(t)
	if res, out := pull(t, c, o); res.Events != 5 || res.Failed() {
		t.Fatalf("fresh pull %+v\n%s", res, out)
	}
}

func TestPullRefusesAForkInALongerStoreChain(t *testing.T) {
	o := fsObjects(t)
	a, b := shared(t, o, 2)
	add(t, a, "it", "a", 3)
	push(t, a, o)
	add(t, b, "it", "b", 1)
	refusesFork(t, b, o, 2)
}

func TestPullRefusesAForkAtAnEqualTail(t *testing.T) {
	o := fsObjects(t)
	a, b := shared(t, o, 2)
	add(t, a, "it", "a", 1)
	add(t, b, "it", "b", 1)
	push(t, a, o)
	refusesFork(t, b, o, 2)
}

func TestPullRefusesAForkAtTheTailOfAShorterStoreChain(t *testing.T) {
	o := fsObjects(t)
	a, b := newStore(t), newStore(t)
	add(t, a, "it", "s", 2)
	copyChain(t, a, b, "it")
	add(t, a, "it", "a", 1)
	push(t, a, o) // the store holds 0, 1, 2a
	add(t, b, "it", "b", 3)
	refusesFork(t, b, o, 2) // B holds 0, 1, 2b, 3b, 4b
}

func TestPullRefusesAForkBeforeTheTailOfAShorterStoreChain(t *testing.T) {
	o := fsObjects(t)
	a, b := newStore(t), newStore(t)
	add(t, a, "it", "s", 1)
	copyChain(t, a, b, "it")
	add(t, a, "it", "a", 3)
	push(t, a, o) // the store holds 0, 1a, 2a, 3a
	add(t, b, "it", "b", 5)
	refusesFork(t, b, o, 1) // B holds 0, 1b..5b
}

func TestPullRefusesAStoreEventThatDoesNotVerify(t *testing.T) {
	o := newMem()
	a, b := shared(t, o, 3)
	add(t, a, "it", "a", 1)
	push(t, a, o)
	e := chain(t, a, "it")[3]
	e.Actor = "forger" // the hash no longer matches the content
	body, _ := ledger.Encode(e)
	o.objs[ChainKey("it", 3)] = body
	before := chainFile(t, b, "it")
	res, out := pull(t, b, o)
	if len(res.Refused) != 1 || !strings.Contains(out, "it event 3 from the store does not verify") || res.Events != 0 {
		t.Fatalf("pull %+v\n%s", res, out)
	}
	if !bytes.Equal(before, chainFile(t, b, "it")) {
		t.Error("a forged event was appended")
	}
}

func TestPullOfAPurePrefixIsNotAConflict(t *testing.T) {
	o := fsObjects(t)
	a := newStore(t)
	add(t, a, "it", "a", 3)
	push(t, a, o)
	add(t, a, "it", "b", 2)
	before := chainFile(t, a, "it")
	res, out := pull(t, a, o)
	if res.Failed() || res.Events != 0 {
		t.Fatalf("pull %+v\n%s", res, out)
	}
	if !bytes.Equal(before, chainFile(t, a, "it")) {
		t.Error("the local chain changed")
	}
	if res, out := push(t, a, o); res.Events != 2 || res.Failed() {
		t.Fatalf("the next push %+v\n%s", res, out)
	}
	if got := seqsOf(t, o, "it"); !slices.Equal(got, upTo(5)) {
		t.Fatalf("store holds %v", got)
	}
}
