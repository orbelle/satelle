//go:build !windows

package ledgerrepl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

type step func(t *testing.T, s *ledger.Store)

func app(kind string, payload any) step {
	return func(t *testing.T, s *ledger.Store) {
		t.Helper()
		if _, err := s.Append("it", kind, "me", payload); err != nil {
			t.Fatal(err)
		}
	}
}

func createdAs(status string, inputs *item.Selection) step {
	kind, title := item.QueueKind, "q"
	return app(item.KindCreated, item.Fields{Kind: &kind, Title: &title, Status: &status, Trigger: &item.Trigger{Workflow: "triage"}, Inputs: inputs})
}

func bindStep(session string) step {
	return app(item.KindSeatBound, item.Seat{Session: session, Location: "local:/x"})
}

func releaseStep(session string) step {
	return app(item.KindSeatReleased, item.SeatRelease{Session: session})
}

func addStep(id, cause string) step {
	return app(item.KindQueueAdded, item.QueueEntry{Item: id, Cause: cause})
}

func fireStep(kept ...string) step {
	return app(item.KindQueueFired, item.QueueFire{Run: "exe_1", Workflow: "triage", Kept: kept})
}

func moveStep(from, to string) step {
	return app(item.KindTransitioned, item.Move{From: from, To: to})
}

func attachStep(operator bool) step {
	return func(t *testing.T, s *ledger.Store) {
		t.Helper()
		sha, err := s.PutBlob([]byte("body"))
		if err != nil {
			t.Fatal(err)
		}
		app(item.KindAttachmentAdd, item.Attachment{Name: "n", SHA256: sha, Size: 4, MediaType: "text/plain", Operator: operator})(t, s)
	}
}

func run(t *testing.T, s *ledger.Store, steps []step) {
	t.Helper()
	for _, st := range steps {
		st(t, s)
	}
}

var sharedQueue = []step{createdAs("plan", nil), bindStep("x"), addStep("s1", "c")}

func forkWith(t *testing.T, prefix, store, local []step) (a, b *ledger.Store, o Objects) {
	t.Helper()
	o = newMem()
	a, b = newStore(t), newStore(t)
	run(t, a, prefix)
	push(t, a, o)
	copyChain(t, a, b, "it")
	run(t, a, store)
	push(t, a, o)
	run(t, b, local)
	return a, b, o
}

func syncOut(t *testing.T, s *ledger.Store, o Objects) (SyncResult, string) {
	t.Helper()
	var out bytes.Buffer
	res, err := repl(s, o, &out).Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out.String())
	}
	return res, out.String()
}

func sameContent(t *testing.T, got, want []ledger.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d events, want %d", len(got), len(want))
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.ID != w.ID || g.Kind != w.Kind || g.Actor != w.Actor || !g.At.Equal(w.At) || string(g.Payload) != string(w.Payload) {
			t.Errorf("event %d is %+v, want the content of %+v", i, g, w)
		}
	}
}

func rebasedTail(t *testing.T) (a, b *ledger.Store, o Objects, tail []ledger.Event, res SyncResult, out string) {
	t.Helper()
	a, b, o = forkWith(t, sharedQueue,
		[]step{fireStep(), releaseStep("x")},
		[]step{addStep("s2", "c"), releaseStep("x")})
	tail = slices.Clone(chain(t, b, "it")[len(sharedQueue):])
	res, out = syncOut(t, b, o)
	return a, b, o, tail, res, out
}

func TestSyncRebasesCommutingAppends(t *testing.T) {
	a, b, o, tail, res, out := rebasedTail(t)
	if res.Failed() || !slices.Equal(res.Pull.Rebased, []string{"it"}) {
		t.Fatalf("sync %+v\n%s", res, out)
	}
	if got := forksDir(t, b); len(got) != 0 {
		t.Errorf("set aside %v", got)
	}
	theirs, mine := chain(t, a, "it"), chain(t, b, "it")
	if !slices.Equal(hashesOf(mine[:len(theirs)]), hashesOf(theirs)) {
		t.Error("B's chain does not start with the store's")
	}
	sameContent(t, mine[len(theirs):], tail)
	if err := ledger.VerifyChain("it", mine); err != nil {
		t.Fatal(err)
	}
	sameAsStore(t, "B", b, o)
	if !strings.Contains(out, "rebased: it:") {
		t.Errorf("output lacks the rebase:\n%s", out)
	}
}

func TestRebasedChainFoldsAlikeOnBothLocations(t *testing.T) {
	a, b, o, _, res, out := rebasedTail(t)
	if res.Failed() {
		t.Fatalf("B's sync %+v\n%s", res, out)
	}
	if res, out := syncOut(t, a, o); res.Failed() {
		t.Fatalf("A's sync %+v\n%s", res, out)
	}
	if !bytes.Equal(chainFile(t, a, "it"), chainFile(t, b, "it")) {
		t.Fatal("A and B hold different chains")
	}
	ca, cb := chain(t, a, "it"), chain(t, b, "it")
	ia, err := item.Fold(ca)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := item.Fold(cb)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ia, ib) {
		t.Errorf("A folds %+v, B %+v", ia, ib)
	}
	if !reflect.DeepEqual(item.FoldSeats(ca), item.FoldSeats(cb)) {
		t.Error("A and B fold different seats")
	}
	qa, qb := item.FoldQueue(ca), item.FoldQueue(cb)
	if !slices.Equal(qa.Explicit, []string{"s2"}) || !slices.Equal(qb.Explicit, []string{"s2"}) || !maps.Equal(qa.Causes, qb.Causes) {
		t.Errorf("queues %+v and %+v, want s2 alone", qa, qb)
	}
	if qa.LastFire == nil || len(qa.LastFire.Kept) != 0 || len(qa.Fires) != 1 {
		t.Errorf("the fire changed: %+v", qa.LastFire)
	}
	for name, s := range map[string]*ledger.Store{"A": a, "B": b} {
		if res, out := syncOut(t, s, o); res.Failed() || res.Push.Events != 0 || res.Pull.Events != 0 {
			t.Errorf("%s's idle pass %+v\n%s", name, res, out)
		}
	}
}

type untouched struct {
	file    []byte
	objects map[string][]byte
}

func snapshot(t *testing.T, b *ledger.Store, o Objects) untouched {
	return untouched{file: chainFile(t, b, "it"), objects: objectsOf(t, o)}
}

func (u untouched) check(t *testing.T, b *ledger.Store, o Objects, res SyncResult) {
	t.Helper()
	if !bytes.Equal(u.file, chainFile(t, b, "it")) {
		t.Error("B's chain changed")
	}
	if !maps.EqualFunc(u.objects, objectsOf(t, o), bytes.Equal) {
		t.Error("the store changed")
	}
	if got := forksDir(t, b); len(got) != 0 {
		t.Errorf("set aside %v", got)
	}
	if len(res.Pull.Rebased) != 0 {
		t.Errorf("rebased %v", res.Pull.Rebased)
	}
	if !res.Failed() {
		t.Error("a conflict did not fail the pass")
	}
}

func TestSyncRefusesAQueueAddOfAClaimedMember(t *testing.T) {
	a, b, o := forkWith(t, sharedQueue, []step{fireStep()}, []step{addStep("s1", "c2")})
	before := snapshot(t, b, o)
	res, out := syncOut(t, b, o)
	before.check(t, b, o, res)
	if want := "conflict: it at event 3: this runtime's queue.added does not commute with the store's queue.fired (event 3)"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if q := item.FoldQueue(chain(t, a, "it")); slices.Contains(q.Explicit, "s1") {
		t.Errorf("the store's queue holds s1 again: %+v", q)
	}
}

func TestSyncRebasesASeatReleasePastATransition(t *testing.T) {
	a, b, o := forkWith(t, []step{createdAs("backlog", nil), bindStep("x")}, []step{moveStep("backlog", "plan")}, []step{releaseStep("x")})
	res, out := syncOut(t, b, o)
	if res.Failed() || !slices.Equal(res.Pull.Rebased, []string{"it"}) {
		t.Fatalf("sync %+v\n%s", res, out)
	}
	sameAsStore(t, "B", b, o)
	mine := chain(t, b, "it")
	if len(mine) != len(chain(t, a, "it"))+1 {
		t.Fatalf("B holds %d events", len(mine))
	}
	if seats := item.FoldSeats(mine); len(seats) != 0 {
		t.Errorf("seats %+v", seats)
	}
	if it, err := item.Fold(mine); err != nil || it.Status != "plan" {
		t.Errorf("fold %+v %v", it, err)
	}
}

func TestPullRebasesWithoutSync(t *testing.T) {
	_, b, o := forkWith(t, sharedQueue, []step{releaseStep("x")}, []step{addStep("s2", "c")})
	res, out := pull(t, b, o)
	if res.Failed() || !slices.Equal(res.Rebased, []string{"it"}) {
		t.Fatalf("pull %+v\n%s", res, out)
	}
	sts, err := repl(b, o, &bytes.Buffer{}).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sts) != 1 || sts[0].State != Ahead {
		t.Fatalf("status %+v", sts)
	}
	if res, out := push(t, b, o); res.Failed() || res.Events != 1 {
		t.Fatalf("push %+v\n%s", res, out)
	}
	sameAsStore(t, "B", b, o)
}

func TestSyncReportsATransitionConflict(t *testing.T) {
	_, b, o := forkWith(t, []step{createdAs("plan", nil), bindStep("x")}, []step{moveStep("plan", "in_progress")}, []step{releaseStep("x"), moveStep("plan", "blocked")})
	before := snapshot(t, b, o)
	mine := slices.Clone(chain(t, b, "it")[2:])
	res, out := syncOut(t, b, o)
	before.check(t, b, o, res)
	want := "conflict at 2: this runtime's transition plan → blocked, the store's transition plan → in_progress"
	if len(res.Unsynced) != 1 || res.Unsynced[0].State != want {
		t.Fatalf("unsynced %+v", res.Unsynced)
	}
	if line := "conflict: it at event 3: this runtime's transition plan → blocked does not commute with the store's transition plan → in_progress (event 2)"; !strings.Contains(out, line) {
		t.Errorf("output lacks %q:\n%s", line, out)
	}
	if len(res.Pull.Diverged) != 1 {
		t.Fatalf("diverged %+v", res.Pull.Diverged)
	}
	c := res.Pull.Diverged[0].Conflict
	if c == nil || c.Local == nil || c.Store == nil || *c.Local != (item.Move{From: "plan", To: "blocked"}) || *c.Store != (item.Move{From: "plan", To: "in_progress"}) {
		t.Fatalf("conflict %+v", c)
	}
	done, _, err := repl(b, o, &bytes.Buffer{}).Adopt(ctx, []string{"it"})
	if err != nil || len(done) != 1 {
		t.Fatalf("adopt %+v %v", done, err)
	}
	if got := readSetAside(t, done[0].Path); !slices.Equal(hashesOf(got), hashesOf(mine)) {
		t.Errorf("set aside %v, want B's %v", idsOf(got), idsOf(mine))
	}
}

func TestSyncReportsEveryNonAllowlistedTailAsAConflict(t *testing.T) {
	gate := func(verdict string) step { return app("gate.run", map[string]string{"gate": "g", "verdict": verdict}) }
	workflow := "triage"
	for _, tc := range []struct {
		name         string
		prefix       []step
		store, local step
		lk, sk       string
	}{
		{"gate.run past a reject", sharedQueue, gate("reject"), gate("accept"), "gate.run", "gate.run"},
		{"bind past a release", sharedQueue, releaseStep("x"), bindStep("y"), "seat.bound", "seat.released"},
		{"update past a gate", sharedQueue, gate("accept"), app(item.KindUpdated, item.Fields{Workflow: &workflow}), "item.updated", "gate.run"},
		{"operator attachment past a transition", sharedQueue, moveStep("plan", "in_progress"), attachStep(true), "attachment.added", "transition plan → in_progress"},
		{"matching transition past an update", sharedQueue, app(item.KindUpdated, item.Fields{Workflow: &workflow}), moveStep("plan", "in_progress"), "transition plan → in_progress", "item.updated"},
		{"matching transition past an attachment", sharedQueue, attachStep(false), moveStep("plan", "in_progress"), "transition plan → in_progress", "attachment.added"},
		{"matching transition past a collect", sharedQueue, app("branch.collected", map[string]string{}), moveStep("plan", "in_progress"), "transition plan → in_progress", "branch.collected"},
		{"matching transition past a gate", sharedQueue, gate("accept"), moveStep("plan", "in_progress"), "transition plan → in_progress", "gate.run"},
		{"release past a bind of the session", sharedQueue, bindStep("x"), releaseStep("x"), "seat.released", "seat.bound"},
		{"add of a claimed member", sharedQueue, fireStep(), addStep("s1", "c"), "queue.added", "queue.fired"},
		{"add of a kept member", sharedQueue, fireStep("s1"), addStep("s1", "c2"), "queue.added", "queue.fired"},
		{"add on a filtered queue", []step{createdAs("plan", &item.Selection{Kind: "story"})}, fireStep(), addStep("s9", "c"), "queue.added", "queue.fired"},
		{"undecodable local release", sharedQueue, releaseStep("y"), app(item.KindSeatReleased, json.RawMessage(`{"session":1}`)), "seat.released", "seat.released"},
		{"undecodable store add", sharedQueue, app(item.KindQueueAdded, json.RawMessage(`{"item":1}`)), addStep("s2", "c"), "queue.added", "queue.added"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, b, o := forkWith(t, tc.prefix, []step{tc.store}, []step{tc.local})
			before := snapshot(t, b, o)
			res, out := syncOut(t, b, o)
			before.check(t, b, o, res)
			at := len(tc.prefix)
			want := fmt.Sprintf("conflict: it at event %d: this runtime's %s does not commute with the store's %s (event %d)", at, tc.lk, tc.sk, at)
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		})
	}
}
