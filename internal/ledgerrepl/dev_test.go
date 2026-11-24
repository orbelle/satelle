package ledgerrepl

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

type world struct {
	srv     *fakeserver.Server
	client  *hosted.Client
	project string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	srv := fakeserver.Start(t, "tok")
	c, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "tok"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	p, err := c.Projects.CreateProject(context.Background(), &satellepb.CreateProjectRequest{Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	return &world{srv: srv, client: c, project: p.GetId()}
}

func (w *world) dev() *Dev { return &Dev{Client: w.client, Project: w.project, Server: w.srv.URL} }

func (w *world) serverHashes(item string) []string {
	var out []string
	for _, e := range w.srv.Events(w.project, item) {
		out = append(out, e.GetHash())
	}
	return out
}

type backend struct {
	name string
	open func() Store
}

func backends(t *testing.T) []backend {
	t.Helper()
	m := newMem()
	w := newWorld(t)
	return []backend{
		{"command", func() Store { return &ObjectStore{Objects: m, Name: "mem"} }},
		{"satelle.dev", func() Store { return w.dev() }},
	}
}

func forEach(t *testing.T, fn func(t *testing.T, open func() Store)) {
	for _, name := range []string{"command", "satelle.dev"} {
		t.Run(name, func(t *testing.T) {
			for _, b := range backends(t) {
				if b.name == name {
					fn(t, b.open)
				}
			}
		})
	}
}

func replOn(s *ledger.Store, st Store, out *bytes.Buffer) *Replicator {
	return &Replicator{Ledger: s, Store: st, Refs: kernel.BlobRefs, Out: out}
}

func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return", what)
	}
}

func pushOn(t *testing.T, s *ledger.Store, st Store, items ...string) (Result, string) {
	t.Helper()
	var out bytes.Buffer
	var res Result
	var err error
	within(t, "push", func() { res, err = replOn(s, st, &out).Push(ctx, items...) })
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out.String())
	}
	return res, out.String()
}

func pullOn(t *testing.T, s *ledger.Store, st Store, items ...string) (Result, string) {
	t.Helper()
	var out bytes.Buffer
	var res Result
	var err error
	within(t, "pull", func() { res, err = replOn(s, st, &out).Pull(ctx, items...) })
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out.String())
	}
	return res, out.String()
}

func held(t *testing.T, st Store, item string) []string {
	t.Helper()
	l, err := st.Chains(ctx, []string{item})
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.Events(ctx, item, 0, l.Chains[item].Len)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		out = append(out, e.Hash)
	}
	return out
}

func hashesOf(events []ledger.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Hash)
	}
	return out
}

type offers struct {
	Store
	n int
}

func (o *offers) Offer(ctx context.Context, item string, seq int, events []ledger.Event) (int, string, error) {
	o.n++
	return o.Store.Offer(ctx, item, seq, events)
}

func TestPushOfAChainTheStoreExtendsWritesNothing(t *testing.T) {
	forEach(t, func(t *testing.T, open func() Store) {
		a, b := newStore(t), newStore(t)
		add(t, a, "it", "a", 5)
		if res, out := pushOn(t, a, open()); res.Events != 5 || res.Failed() {
			t.Fatalf("A's push %+v\n%s", res, out)
		}
		if _, err := b.Import(chain(t, a, "it")[:3]); err != nil {
			t.Fatal(err)
		}
		for _, items := range [][]string{nil, {"it"}} {
			st := &offers{Store: open()}
			res, out := pushOn(t, b, st, items...)
			if res.Events != 0 || res.Failed() || res.Items != 0 || st.n > 1 {
				t.Fatalf("B's push of %v: %+v after %d offer(s)\n%s", items, res, st.n, out)
			}
			if got, want := held(t, open(), "it"), hashesOf(chain(t, a, "it")); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("the store holds %v, want A's 5", got)
			}
		}
	})
}

func TestPushSameHashContinuesOnEveryStore(t *testing.T) {
	forEach(t, func(t *testing.T, open func() Store) {
		a, b := newStore(t), newStore(t)
		add(t, a, "it", "a", 3)
		pushOn(t, a, open())
		copyChain(t, a, b, "it")
		add(t, b, "it", "b", 2)
		res, out := pushOn(t, b, open())
		if res.Failed() || res.Events != 2 || res.Items != 1 {
			t.Fatalf("B's push %+v\n%s", res, out)
		}
		if got, want := held(t, open(), "it"), hashesOf(chain(t, b, "it")); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("store holds %v, want B's chain", got)
		}
		if res, out := pushOn(t, b, open()); res.Events != 0 || res.Failed() {
			t.Fatalf("rerun %+v\n%s", res, out)
		}
	})
}

func TestPushStopsAtADifferentHashOnEveryStore(t *testing.T) {
	forEach(t, func(t *testing.T, open func() Store) {
		a, b := newStore(t), newStore(t)
		add(t, a, "it", "s", 2)
		pushOn(t, a, open())
		copyChain(t, a, b, "it")
		add(t, a, "it", "a", 1)
		pushOn(t, a, open())
		add(t, b, "it", "b", 1)
		add(t, b, "other", "o", 2)
		res, out := pushOn(t, b, open())
		if len(res.Diverged) != 1 || res.Diverged[0].Item != "it" || res.Diverged[0].Seq != 2 || !res.Failed() {
			t.Fatalf("B's push %+v\n%s", res, out)
		}
		if !strings.Contains(out, "diverged: it at event 2: ") || !strings.Contains(out, "not pushed past it") {
			t.Errorf("output:\n%s", out)
		}
		if got := held(t, open(), "it"); len(got) != 3 || got[2] != chain(t, a, "it")[2].Hash {
			t.Errorf("the store's event 2 is not A's: %v", got)
		}
		if got := held(t, open(), "other"); len(got) != 2 {
			t.Errorf("the other item was not pushed: %v", got)
		}
	})
}

func TestPullRefusesAForkAtEveryLengthOnEveryStore(t *testing.T) {
	cases := []struct {
		name                 string
		shared, store, local int
		fork                 int
	}{
		{"longer store chain", 2, 3, 1, 2},
		{"equal tail", 2, 1, 1, 2},
		{"shorter, forked at its tail", 2, 1, 3, 2},
		{"shorter, forked before its tail", 1, 3, 5, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			forEach(t, func(t *testing.T, open func() Store) {
				a, b := newStore(t), newStore(t)
				add(t, a, "it", "s", c.shared)
				copyChain(t, a, b, "it")
				add(t, a, "it", "a", c.store)
				pushOn(t, a, open())
				add(t, b, "it", "b", c.local)
				before := chainFile(t, b, "it")
				for _, items := range [][]string{nil, {"it"}} {
					res, out := pullOn(t, b, open(), items...)
					if len(res.Diverged) != 1 || res.Diverged[0].Item != "it" || res.Diverged[0].Seq != c.fork || res.Events != 0 || !res.Failed() {
						t.Fatalf("pull %v: %+v, want a fork at %d\n%s", items, res, c.fork, out)
					}
					if want := "diverged: it at event " + strconv.Itoa(c.fork) + ": "; !strings.Contains(out, want) || !strings.Contains(out, "nothing appended") {
						t.Errorf("output lacks %q:\n%s", want, out)
					}
					if !bytes.Equal(before, chainFile(t, b, "it")) {
						t.Fatal("the local chain changed")
					}
				}
			})
		})
	}
}

func TestNamedItems(t *testing.T) {
	forEach(t, func(t *testing.T, open func() Store) {
		a := newStore(t)
		add(t, a, "it", "a", 2)
		add(t, a, "two", "a", 1)
		if _, err := replOn(a, open(), nil).Push(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "no chain nope") {
			t.Fatalf("push of an unknown item: %v", err)
		}
		if res, out := pushOn(t, a, open(), "it"); res.Events != 2 || res.Items != 1 {
			t.Fatalf("push it: %+v\n%s", res, out)
		}
		if got := held(t, open(), "two"); len(got) != 0 {
			t.Fatalf("a push of it pushed two: %v", got)
		}
		res, out := pullOn(t, newStore(t), open(), "two")
		if len(res.Refused) != 1 || !strings.Contains(out, "the store holds no chain two") {
			t.Fatalf("pull of an item the store lacks: %+v\n%s", res, out)
		}
		b := newStore(t)
		if res, out := pullOn(t, b, open(), "it"); res.Events != 2 || res.Failed() {
			t.Fatalf("pull it: %+v\n%s", res, out)
		}
	})
}

func TestDevCarriesBlobs(t *testing.T) {
	w := newWorld(t)
	a := newStore(t)
	add(t, a, "it", "a", 1)
	_, sha := attach(t, a, "it", "plan", "# the plan\n")
	out, err := a.PutBlob([]byte("the performer's answer"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Append("it", kernel.KindPerformerRun, "me", kernel.PerformerRun{State: "work", Output: out})
	if err != nil {
		t.Fatal(err)
	}
	if res, o := pushOn(t, a, w.dev()); res.Blobs != 2 || res.Events != 3 || len(res.Gaps) != 0 {
		t.Fatalf("push %+v\n%s", res, o)
	}
	names := map[string]string{}
	for _, at := range w.srv.Attachments() {
		names[at.GetName()] = at.GetSha256()
	}
	if names["plan"] != sha || names[PerformerOutputPrefix+run.ID] != out || len(names) != 2 {
		t.Fatalf("server attachments %v", names)
	}
	if res, o := pushOn(t, a, w.dev()); res.Blobs != 0 || res.Events != 0 {
		t.Fatalf("rerun %+v\n%s", res, o)
	}
	b := newStore(t)
	if res, o := pullOn(t, b, w.dev()); res.Blobs != 2 || res.Events != 3 || len(res.Gaps) != 0 {
		t.Fatalf("pull %+v\n%s", res, o)
	}
	for _, s := range []string{sha, out} {
		if _, err := b.GetBlob(s); err != nil {
			t.Errorf("blob %s: %v", s, err)
		}
	}
	gaps, err := b.BlobGaps("it", kernel.BlobRefs(chain(t, b, "it")))
	if err != nil || len(gaps) != 0 {
		t.Fatalf("gaps %v %v", gaps, err)
	}
}

func TestStatusOnEveryStore(t *testing.T) {
	forEach(t, func(t *testing.T, open func() Store) {
		a, b := newStore(t), newStore(t)
		add(t, a, "same", "s", 2)
		add(t, a, "ahead", "s", 2)
		add(t, a, "behind", "s", 2)
		add(t, a, "fork", "s", 2)
		add(t, a, "theirs", "s", 1)
		pushOn(t, a, open())
		for _, it := range []string{"same", "ahead", "behind", "fork"} {
			copyChain(t, a, b, it)
		}
		add(t, a, "behind", "a", 1)
		add(t, a, "fork", "a", 1)
		pushOn(t, a, open())
		add(t, b, "ahead", "b", 1)
		add(t, b, "fork", "b", 1)
		add(t, b, "mine", "b", 1)
		rows, err := replOn(b, open(), nil).Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, r := range rows {
			got[r.Item] = r.State
			wantFork := -1
			if r.Item == "fork" {
				wantFork = 2
			}
			if r.Fork != wantFork {
				t.Errorf("%s: Fork %d, want %d", r.Item, r.Fork, wantFork)
			}
			body, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var keys map[string]any
			if err := json.Unmarshal(body, &keys); err != nil {
				t.Fatal(err)
			}
			if len(keys) != 4 || keys["fork"] != nil || keys["Fork"] != nil {
				t.Errorf("%s: row serialises more than item, local, store and state: %s", r.Item, body)
			}
		}
		want := map[string]string{"same": InSync, "ahead": Ahead, "behind": Behind, "fork": "diverged at 2", "theirs": StoreOnly, "mine": LocalOnly}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %q, want %q (all %v)", k, got[k], v, got)
			}
		}
	})
}
