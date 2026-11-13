//go:build !windows

package ledgerrepl

import (
	"bytes"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// fakeMirrors is a FileMirrors that answers MirrorOnly with mirrorOnly and,
// on Remirror, appends extra events to the item's chain in s.
type fakeMirrors struct {
	s          *ledger.Store
	mirrorOnly bool
	extra      int
	remirrored []string
}

func (f *fakeMirrors) MirrorOnly(string, []ledger.Event) bool { return f.mirrorOnly }

func (f *fakeMirrors) Remirror(id string) (bool, error) {
	f.remirrored = append(f.remirrored, id)
	for range f.extra {
		if _, err := f.s.Append(id, "item.updated", Actor, map[string]string{"cause": "queue file q"}); err != nil {
			return false, err
		}
	}
	return f.extra > 0, nil
}

// forked returns the store holding A's 3-event chain of it and B holding a
// different 1-event chain of it: a fork at 0.
func forked(t *testing.T) (a, b *ledger.Store, o Objects) {
	t.Helper()
	o = fsObjects(t)
	a, b = newStore(t), newStore(t)
	add(t, a, "it", "a", 3)
	push(t, a, o)
	add(t, b, "it", "b", 1)
	return a, b, o
}

// objectsOf is every object the store holds, by key.
func objectsOf(t *testing.T, o Objects) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	var keys []string
	for _, prefix := range []string{ChainsPrefix, BlobsPrefix} {
		ks, err := o.List(ctx, prefix, "")
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, ks...)
	}
	for _, k := range keys {
		b, _, err := o.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		out[k] = b
	}
	return out
}

func TestPullAdoptsFileMirrorOnlyChain(t *testing.T) {
	a, b, o := forked(t)
	var out bytes.Buffer
	r := repl(b, o, &out)
	f := &fakeMirrors{s: b, mirrorOnly: true}
	r.Mirrors = f
	res, err := r.Pull(ctx)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out.String())
	}
	if res.Failed() || !slices.Equal(res.Adopted, []string{"it"}) || res.Events != 3 || res.Items != 1 {
		t.Fatalf("pull %+v\n%s", res, out.String())
	}
	if !bytes.Equal(chainFile(t, a, "it"), chainFile(t, b, "it")) {
		t.Fatal("B's chain is not the store's")
	}
	if !slices.Equal(f.remirrored, []string{"it"}) {
		t.Fatalf("Remirror called for %v, want once for it", f.remirrored)
	}
	if want := "adopted: it: this runtime's 1 queue-file event(s) replaced by the store's 3"; !strings.Contains(out.String(), want) {
		t.Fatalf("output lacks %q:\n%s", want, out.String())
	}
}

func TestSyncPushesRemirroredAdoption(t *testing.T) {
	for _, extra := range []int{1, 0} {
		a, b, o := forked(t)
		var out bytes.Buffer
		r := repl(b, o, &out)
		r.Mirrors = &fakeMirrors{s: b, mirrorOnly: true, extra: extra}
		res, err := r.Sync(ctx)
		if err != nil {
			t.Fatalf("sync: %v\n%s", err, out.String())
		}
		if res.Failed() || !slices.Equal(res.Pull.Adopted, []string{"it"}) || res.Push.Events != extra {
			t.Fatalf("extra %d: sync %+v\n%s", extra, res, out.String())
		}
		local := chain(t, b, "it")
		if len(local) != 3+extra || storeHash(t, o, "it", len(local)-1) != local[len(local)-1].Hash {
			t.Fatalf("extra %d: B holds %d events, not the store's head", extra, len(local))
		}
		if prefix := chain(t, a, "it"); !slices.Equal(hashesOf(local[:3]), hashesOf(prefix)) {
			t.Fatalf("extra %d: B's chain does not start with A's", extra)
		}
		if again, err := r.Sync(ctx); err != nil || again.Failed() || again.Push.Events != 0 || again.Pull.Events != 0 {
			t.Fatalf("extra %d: second pass %+v %v", extra, again, err)
		}
	}
}

// A fork whose local chain holds any other event is never dropped: pull and
// sync leave both sides as they were and name the divergence.
func TestSyncLeavesWorkedForkAlone(t *testing.T) {
	_, b, o := forked(t)
	f := &fakeMirrors{s: b, mirrorOnly: false, extra: 1}
	localBefore, storeBefore := chainFile(t, b, "it"), objectsOf(t, o)
	unchanged := func(what string) {
		t.Helper()
		if !bytes.Equal(localBefore, chainFile(t, b, "it")) {
			t.Fatalf("%s changed the local chain", what)
		}
		if !maps.EqualFunc(storeBefore, objectsOf(t, o), bytes.Equal) {
			t.Fatalf("%s changed the store", what)
		}
	}
	var out bytes.Buffer
	r := repl(b, o, &out)
	r.Mirrors = f
	sres, err := r.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sres.Unsynced) != 1 || sres.Unsynced[0].Item != "it" || sres.Unsynced[0].State != "diverged at 0" || len(sres.Pull.Adopted) != 0 {
		t.Fatalf("sync %+v", sres)
	}
	unchanged("sync")
	pres, err := r.Pull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pres.Diverged) != 1 || pres.Diverged[0].Item != "it" || pres.Diverged[0].Seq != 0 || len(pres.Adopted) != 0 {
		t.Fatalf("pull %+v\n%s", pres, out.String())
	}
	unchanged("pull")
	if len(f.remirrored) != 0 {
		t.Fatalf("Remirror called for %v", f.remirrored)
	}
}
