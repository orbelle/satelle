//go:build !windows

package ledgerrepl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/ledger"
)

type fakeMirrors struct {
	s          *ledger.Store
	mirrorOnly bool
	extra      int
	remirrored []string
	froms      []int
}

func (f *fakeMirrors) MirrorTail(_ string, _ []ledger.Event, from int) bool {
	f.froms = append(f.froms, from)
	return f.mirrorOnly
}

func (f *fakeMirrors) Remirror(id string) (bool, error) {
	f.remirrored = append(f.remirrored, id)
	for range f.extra {
		if _, err := f.s.Append(id, "item.updated", Actor, map[string]string{"cause": "queue file q"}); err != nil {
			return false, err
		}
	}
	return f.extra > 0, nil
}

func forked(t *testing.T) (a, b *ledger.Store, o Objects) {
	t.Helper()
	o = fsObjects(t)
	a, b = newStore(t), newStore(t)
	add(t, a, "it", "a", 3)
	push(t, a, o)
	add(t, b, "it", "b", 1)
	return a, b, o
}

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
	if !slices.Equal(f.froms, []int{0}) {
		t.Fatalf("MirrorTail asked from %v, want [0]", f.froms)
	}
	if want := "adopted: it: this runtime's 1 queue-file event(s) replaced by the store's 3"; !strings.Contains(out.String(), want) {
		t.Fatalf("output lacks %q:\n%s", want, out.String())
	}
}

func mirrorTailFork(t *testing.T) (a, b *ledger.Store, o Objects) {
	t.Helper()
	o = fsObjects(t)
	a, b = shared(t, o, 2)
	add(t, a, "it", "a", 1)
	push(t, a, o)
	add(t, b, "it", "b", 1)
	return a, b, o
}

func TestPullAdoptsMirrorTailFork(t *testing.T) {
	a, b, o := mirrorTailFork(t)
	var out bytes.Buffer
	r := repl(b, o, &out)
	f := &fakeMirrors{s: b, mirrorOnly: true}
	r.Mirrors = f
	res, err := r.Pull(ctx)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out.String())
	}
	if res.Failed() || !slices.Equal(res.Adopted, []string{"it"}) {
		t.Fatalf("pull %+v\n%s", res, out.String())
	}
	if !bytes.Equal(chainFile(t, a, "it"), chainFile(t, b, "it")) {
		t.Fatal("B's chain is not the store's")
	}
	if !slices.Equal(f.froms, []int{2}) {
		t.Fatalf("MirrorTail asked from %v, want [2]", f.froms)
	}
	if want := "adopted: it: this runtime's 1 queue-file event(s) replaced by the store's 3"; !strings.Contains(out.String(), want) {
		t.Fatalf("output lacks %q:\n%s", want, out.String())
	}
}

type equivocatingStore struct {
	Store
	events Store
}

func (s equivocatingStore) Events(ctx context.Context, item string, from, to int) ([]ledger.Event, error) {
	return s.events.Events(ctx, item, from, to)
}

func TestPullRefusesMirrorTailWhenStoreChangesPrefix(t *testing.T) {
	for _, served := range []int{3, 1} {
		t.Run(strconv.Itoa(served), func(t *testing.T) {
			_, b, o := mirrorTailFork(t)
			o2 := fsObjects(t)
			c := newStore(t)
			add(t, c, "it", "c", served)
			push(t, c, o2)
			setup := func() (*Replicator, *fakeMirrors, *bytes.Buffer) {
				var out bytes.Buffer
				r := repl(b, o, &out)
				r.Store = equivocatingStore{Store: &ObjectStore{Objects: o, Name: "test"}, events: &ObjectStore{Objects: o2, Name: "other"}}
				f := &fakeMirrors{s: b, mirrorOnly: true, extra: 1}
				r.Mirrors = f
				return r, f, &out
			}
			localBefore, storeBefore := chainFile(t, b, "it"), objectsOf(t, o)
			unchanged := func(what string, f *fakeMirrors) {
				t.Helper()
				if !bytes.Equal(localBefore, chainFile(t, b, "it")) {
					t.Fatalf("%s changed the local chain", what)
				}
				if !maps.EqualFunc(storeBefore, objectsOf(t, o), bytes.Equal) {
					t.Fatalf("%s changed the store", what)
				}
				if len(f.remirrored) != 0 {
					t.Fatalf("%s called Remirror for %v", what, f.remirrored)
				}
			}
			r, f, out := setup()
			res, err := r.Pull(ctx)
			if err != nil && served == 3 {
				t.Fatalf("pull: %v\n%s", err, out.String())
			}
			if len(res.Adopted) != 0 || strings.Contains(out.String(), "adopted:") || !res.Failed() {
				t.Fatalf("pull %+v\n%s", res, out.String())
			}
			if served == 3 {
				if len(res.Diverged) != 1 || res.Diverged[0].Item != "it" || res.Diverged[0].Seq != 1 {
					t.Fatalf("pull %+v, want it diverged at 1\n%s", res, out.String())
				}
				if !strings.Contains(out.String(), "diverged: it at event 1") || !strings.Contains(out.String(), "nothing appended") {
					t.Fatalf("output lacks the refusal:\n%s", out.String())
				}
			}
			if !slices.Equal(f.froms, []int{2}) {
				t.Fatalf("MirrorTail asked from %v, want [2]", f.froms)
			}
			unchanged("pull", f)
			r, f, out = setup()
			sres, err := r.Sync(ctx)
			if err != nil && served == 3 {
				t.Fatalf("sync: %v\n%s", err, out.String())
			}
			if !sres.Failed() || len(sres.Pull.Adopted) != 0 || sres.Push.Events != 0 {
				t.Fatalf("sync %+v\n%s", sres, out.String())
			}
			unchanged("sync", f)
		})
	}
}

func TestAdoptRefusesWhenStoreChangesPrefix(t *testing.T) {
	for _, served := range []int{3, 1} {
		t.Run(strconv.Itoa(served), func(t *testing.T) {
			_, b, o := mirrorTailFork(t)
			o2 := fsObjects(t)
			c := newStore(t)
			add(t, c, "it", "c", served)
			push(t, c, o2)
			var out bytes.Buffer
			r := repl(b, o, &out)
			r.Store = equivocatingStore{Store: &ObjectStore{Objects: o, Name: "test"}, events: &ObjectStore{Objects: o2, Name: "other"}}
			r.Mirrors = &fakeMirrors{s: b, mirrorOnly: false}
			localBefore, storeBefore := chainFile(t, b, "it"), objectsOf(t, o)
			done, res, err := r.Adopt(ctx, []string{"it"})
			if err != nil || len(done) != 0 || len(res.Adopted) != 0 || !res.Failed() || len(res.Refused) != 1 {
				t.Fatalf("adopt %+v %+v %v\n%s", done, res, err, out.String())
			}
			if served == 3 {
				line := res.Refused[0]
				if !strings.HasPrefix(line, "it event 1 from the store") || !strings.Contains(line, "the fork at 2") || !strings.Contains(line, "nothing replaced") {
					t.Fatalf("refusal %q", line)
				}
			}
			if strings.Contains(out.String(), "adopted:") {
				t.Fatalf("output reports an adoption:\n%s", out.String())
			}
			if !bytes.Equal(localBefore, chainFile(t, b, "it")) {
				t.Fatal("adopt changed the local chain")
			}
			if got := forksDir(t, b); len(got) != 0 {
				t.Fatalf("adopt set events aside in %v", got)
			}
			if !maps.EqualFunc(storeBefore, objectsOf(t, o), bytes.Equal) {
				t.Fatal("adopt changed the store")
			}
		})
	}
}

func TestAdoptTakesStoreChainPastSharedPrefix(t *testing.T) {
	a, b, o := mirrorTailFork(t)
	var out bytes.Buffer
	r := repl(b, o, &out)
	r.Mirrors = &fakeMirrors{s: b, mirrorOnly: false}
	localBefore := chain(t, b, "it")
	done, res, err := r.Adopt(ctx, []string{"it"})
	if err != nil || res.Failed() || len(done) != 1 || !slices.Equal(res.Adopted, []string{"it"}) {
		t.Fatalf("adopt %+v %+v %v\n%s", done, res, err, out.String())
	}
	s := done[0]
	if s.Fork != 2 || s.Taken != 3 {
		t.Fatalf("set aside %+v", s)
	}
	if !bytes.Equal(chainFile(t, a, "it"), chainFile(t, b, "it")) {
		t.Fatal("B's chain is not the store's")
	}
	if got, kept := readSetAside(t, s.Path), localBefore[2:]; !slices.Equal(hashesOf(got), hashesOf(kept)) {
		t.Fatalf("%s holds %v, want %v", s.Path, idsOf(got), idsOf(kept))
	}
	if want := "adopted: it: this runtime's events 2..2 set aside in "; !strings.Contains(out.String(), want) {
		t.Fatalf("output lacks %q:\n%s", want, out.String())
	}
}

func TestSyncPushesRemirroredAdoption(t *testing.T) {
	cases := []struct {
		name string
		fork func(t *testing.T) (a, b *ledger.Store, o Objects)
		at   int
	}{
		{"whole chain", forked, 0},
		{"tail past a shared prefix", mirrorTailFork, 2},
	}
	for _, tc := range cases {
		for _, extra := range []int{1, 0} {
			t.Run(tc.name+"/"+strconv.Itoa(extra), func(t *testing.T) {
				syncsRemirroredAdoption(t, tc.fork, tc.at, extra)
			})
		}
	}
}

func syncsRemirroredAdoption(t *testing.T, fork func(t *testing.T) (a, b *ledger.Store, o Objects), at, extra int) {
	t.Helper()
	a, b, o := fork(t)
	var out bytes.Buffer
	r := repl(b, o, &out)
	f := &fakeMirrors{s: b, mirrorOnly: true, extra: extra}
	r.Mirrors = f
	res, err := r.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out.String())
	}
	if res.Failed() || !slices.Equal(res.Pull.Adopted, []string{"it"}) || res.Push.Events != extra {
		t.Fatalf("extra %d: sync %+v\n%s", extra, res, out.String())
	}
	if !slices.Equal(f.froms, []int{at, at}) {
		t.Fatalf("MirrorTail asked from %v, want the fork point %d from adoptable and pull", f.froms, at)
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

func idsOf(events []ledger.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

func forksDir(t *testing.T, s *ledger.Store) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.Dir(), "forks"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func readSetAside(t *testing.T, path string) []ledger.Event {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []ledger.Event
	for line := range strings.SplitSeq(strings.TrimSuffix(string(body), "\n"), "\n") {
		var e ledger.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestAdoptTakesStoreChainForWorkedFork(t *testing.T) {
	a, b, o := forked(t)
	var out bytes.Buffer
	r := repl(b, o, &out)
	r.Mirrors = &fakeMirrors{s: b, mirrorOnly: false}
	c, err := cache.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	r.Cache = c
	if err := c.Sync(b); err != nil {
		t.Fatal(err)
	}
	localBefore := chain(t, b, "it")
	if res, err := r.Pull(ctx); err != nil || len(res.Diverged) != 1 {
		t.Fatalf("pull %+v %v, want it diverged\n%s", res, err, out.String())
	}
	out.Reset()
	done, res, err := r.Adopt(ctx, []string{"it"})
	if err != nil || res.Failed() || len(done) != 1 || !slices.Equal(res.Adopted, []string{"it"}) {
		t.Fatalf("adopt %+v %+v %v\n%s", done, res, err, out.String())
	}
	if !bytes.Equal(chainFile(t, a, "it"), chainFile(t, b, "it")) {
		t.Fatal("B's chain is not the store's")
	}
	if err := ledger.VerifyChain("it", chain(t, b, "it")); err != nil {
		t.Fatal(err)
	}
	cached, err := c.Events("it", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := idsOf(cached), idsOf(chain(t, a, "it")); !slices.Equal(got, want) {
		t.Fatalf("cache holds %v, want the store's %v", got, want)
	}
	kept := localBefore[done[0].Fork:]
	for _, id := range idsOf(kept) {
		if slices.Contains(idsOf(cached), id) {
			t.Fatalf("cache still holds set-aside event %s", id)
		}
	}
	rows, err := r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != InSync {
		t.Fatalf("status %+v, want it in sync", rows)
	}
	s := done[0]
	if s.Fork != 0 || s.Taken != 3 {
		t.Fatalf("set aside %+v", s)
	}
	if got := readSetAside(t, s.Path); !slices.Equal(hashesOf(got), hashesOf(kept)) || !slices.Equal(idsOf(got), idsOf(kept)) {
		t.Fatalf("%s holds %v, want %v", s.Path, idsOf(got), idsOf(kept))
	}
	if want := "adopted: it: this runtime's events 0..0 set aside in " + s.Path + "; the store's 3 event(s) taken"; !strings.Contains(out.String(), want) {
		t.Fatalf("output lacks %q:\n%s", want, out.String())
	}
	for i, e := range kept {
		if want := "  " + strconv.Itoa(s.Fork+i) + "  " + e.Kind + "  "; !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestAdoptRefusesUnforkedItems(t *testing.T) {
	cases := []struct {
		name, state string
		setup       func(t *testing.T, o Objects) *ledger.Store
	}{
		{"in sync", InSync, func(t *testing.T, o Objects) *ledger.Store {
			a := newStore(t)
			add(t, a, "it", "a", 2)
			push(t, a, o)
			return a
		}},
		{"ahead", Ahead, func(t *testing.T, o Objects) *ledger.Store {
			a := newStore(t)
			add(t, a, "it", "a", 2)
			push(t, a, o)
			add(t, a, "it", "x", 1)
			return a
		}},
		{"behind", Behind, func(t *testing.T, o Objects) *ledger.Store {
			a, b := shared(t, o, 2)
			add(t, a, "it", "a", 1)
			push(t, a, o)
			return b
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := fsObjects(t)
			s := tc.setup(t, o)
			before := chainFile(t, s, "it")
			var out bytes.Buffer
			done, res, err := repl(s, o, &out).Adopt(ctx, []string{"it"})
			if err != nil {
				t.Fatal(err)
			}
			want := "it is " + tc.state + ": only a chain that diverged from the store's is adopted"
			if len(done) != 0 || !slices.Equal(res.Refused, []string{want}) {
				t.Fatalf("adopt %+v %+v, want refused %q", done, res, want)
			}
			if !bytes.Equal(before, chainFile(t, s, "it")) {
				t.Fatal("the local chain changed")
			}
			if f := forksDir(t, s); len(f) != 0 {
				t.Fatalf("set-aside files written: %v", f)
			}
		})
	}
	t.Run("mixed", func(t *testing.T) {
		_, b, o := forked(t)
		add(t, b, "other", "o", 1)
		push(t, b, o)
		itBefore, otherBefore := chainFile(t, b, "it"), chainFile(t, b, "other")
		var out bytes.Buffer
		done, res, err := repl(b, o, &out).Adopt(ctx, []string{"it", "other"})
		if err != nil {
			t.Fatal(err)
		}
		if len(done) != 0 || len(res.Refused) != 1 || !strings.Contains(res.Refused[0], "other is in sync") {
			t.Fatalf("adopt %+v %+v", done, res)
		}
		if !bytes.Equal(itBefore, chainFile(t, b, "it")) || !bytes.Equal(otherBefore, chainFile(t, b, "other")) {
			t.Fatal("a chain changed")
		}
		if f := forksDir(t, b); len(f) != 0 {
			t.Fatalf("set-aside files written: %v", f)
		}
	})
}

func TestDivergentNamesOnlyForkedItems(t *testing.T) {
	_, b, o := forked(t)
	add(t, b, "other", "o", 1)
	push(t, b, o)
	var out bytes.Buffer
	got, err := repl(b, o, &out).Divergent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"it"}) {
		t.Fatalf("Divergent() = %v, want [it]", got)
	}
}
