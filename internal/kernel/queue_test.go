package kernel

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// queueKernel is an initialised repo with a runbook workflow for executions
// (by rule) and a triage workflow no rule names. done enqueues into the queue
// ids given, written before the kernel opens.
func queueKernel(t *testing.T) (string, func(enqueue ...string) *Kernel) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "satelle.toml"), `[workflows]
default = "default"
[[workflows.rule]]
workflow = "runbook"
kind = "execution"
`)
	write(t, filepath.Join(root, "workflows", "runbook.toml"), `name = "runbook"
[states.backlog]
start = true
[states.in_progress]
[states.done]
terminal = true
[states.cancelled]
terminal = true
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["backlog", "in_progress"]
to = "cancelled"
`)
	write(t, filepath.Join(root, "workflows", "triage.toml"), `name = "triage"
[states.open]
start = true
[states.closed]
terminal = true
[[transition]]
from = ["open"]
to = "closed"
`)
	var opened []*Kernel
	t.Cleanup(func() {
		for _, k := range opened {
			k.Close()
		}
	})
	return root, func(enqueue ...string) *Kernel {
		t.Helper()
		q := ""
		if len(enqueue) > 0 {
			q = "enqueue = [\"" + strings.Join(enqueue, "\", \"") + "\"]\n"
		}
		write(t, filepath.Join(root, "workflows", "default.toml"), `name = "default"
[states.backlog]
start = true
[states.done]
terminal = true
`+q+`[[transition]]
from = ["backlog"]
to = "done"
`)
		k, err := Open(Options{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		opened = append(opened, k)
		return k
	}
}

func mustCreate(t *testing.T, k *Kernel, f item.Fields) item.Item {
	t.Helper()
	it, err := k.Create(f, "me")
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func newQueue(t *testing.T, k *Kernel, sel *item.Selection, trig item.Trigger) item.Item {
	t.Helper()
	return mustCreate(t, k, item.Fields{Kind: ptr(item.QueueKind), Title: ptr("q"), Inputs: sel, Trigger: &trig})
}

func runRecord(t *testing.T, k *Kernel, run string) TaskRunRecord {
	t.Helper()
	ev, err := k.Cache.Events(run, KindTaskRun)
	if err != nil || len(ev) != 1 {
		t.Fatalf("task.run on %s: %v %v", run, ev, err)
	}
	var rec TaskRunRecord
	if err := ev[0].Decode(&rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func eventKinds(events []ledger.Event, prefix string) []string {
	var out []string
	for _, e := range events {
		if strings.HasPrefix(e.Kind, prefix) {
			out = append(out, e.Kind)
		}
	}
	return out
}

func TestQueueCreateAndTriggerRefusals(t *testing.T) {
	_, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	q := newQueue(t, k, &item.Selection{Kind: "story"}, item.Trigger{Task: task.ID})
	if !strings.HasPrefix(q.ID, "que_") || q.Trigger == nil || q.Trigger.Task != task.ID || q.Inputs.Kind != "story" {
		t.Fatalf("queue %+v", q)
	}
	wq := newQueue(t, k, nil, item.Trigger{Workflow: "triage", MinSize: 2})
	if wq.Trigger.Workflow != "triage" || wq.Trigger.MinSize != 2 {
		t.Fatalf("workflow queue %+v", wq)
	}
	story := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	for name, f := range map[string]item.Fields{
		"a trigger on a story": {Kind: ptr("story"), Title: ptr("x"), Trigger: &item.Trigger{Task: task.ID}},
		"an unknown task":      {Kind: ptr(item.QueueKind), Title: ptr("x"), Trigger: &item.Trigger{Task: "tas_00000000"}},
		"a story as task":      {Kind: ptr(item.QueueKind), Title: ptr("x"), Trigger: &item.Trigger{Task: story.ID}},
		"an unknown workflow":  {Kind: ptr(item.QueueKind), Title: ptr("x"), Trigger: &item.Trigger{Workflow: "nope"}},
		"both targets":         {Kind: ptr(item.QueueKind), Title: ptr("x"), Trigger: &item.Trigger{Task: task.ID, Workflow: "triage"}},
		"no target":            {Kind: ptr(item.QueueKind), Title: ptr("x"), Trigger: &item.Trigger{}},
		"a negative size":      {Kind: ptr(item.QueueKind), Title: ptr("x"), Trigger: &item.Trigger{Task: task.ID, MinSize: -1}},
	} {
		if _, err := k.Create(f, "me"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := k.Update(story.ID, item.Fields{Trigger: &item.Trigger{Task: task.ID}}, "me"); err == nil {
		t.Error("update set a trigger on a story")
	}
	// Clearing a selection: the zero selection removes it.
	up, err := k.Update(q.ID, item.Fields{Inputs: &item.Selection{}}, "me")
	if err != nil || up.Inputs != nil {
		t.Fatalf("clear inputs %+v %v", up, err)
	}
}

func TestQueueLedgerRecordsAddRemoveFire(t *testing.T) {
	_, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	a := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("a")})
	b := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("b")})
	q := newQueue(t, k, nil, item.Trigger{Task: task.ID})
	out, err := k.Enqueue(q.ID, []string{a.ID, b.ID, a.ID}, "", "me")
	if err != nil || len(out.Added) != 2 || !slices.Equal(out.Already, []string{a.ID}) || out.Fire != nil {
		t.Fatalf("enqueue %+v %v", out, err)
	}
	if again, _ := k.Enqueue(q.ID, []string{a.ID}, "", "me"); len(again.Added) != 0 || len(again.Already) != 1 {
		t.Fatalf("re-enqueue %+v", again)
	}
	if _, err := k.Enqueue(q.ID, []string{q.ID}, "", "me"); err == nil {
		t.Fatal("a queue held itself")
	}
	if _, err := k.Enqueue(q.ID, []string{"sto_00000000"}, "", "me"); err == nil {
		t.Fatal("enqueued a missing item")
	}
	if removed, err := k.Dequeue(q.ID, []string{b.ID}, "me"); err != nil || !slices.Equal(removed, []string{b.ID}) {
		t.Fatalf("dequeue %v %v", removed, err)
	}
	f, err := k.Fire(q.ID, "", "by hand", "me")
	if err != nil || f.Run == "" || !slices.Equal(f.Inputs, []string{a.ID}) {
		t.Fatalf("fire %+v %v", f, err)
	}
	events, _ := k.Ledger.Read(q.ID)
	want := []string{item.KindQueueAdded, item.KindQueueAdded, item.KindQueueRemoved, item.KindQueueFired}
	if got := eventKinds(events, "queue."); !slices.Equal(got, want) {
		t.Fatalf("queue chain %v, want %v", got, want)
	}
	_, st, ms, _ := k.QueueMembers(q.ID)
	if len(st.Explicit) != 0 || len(ms.Members) != 0 || st.LastFire.Run != f.Run {
		t.Fatalf("after fire %+v %v", st, ms.Members)
	}
}

// addCauses lists the causes of every queue.added for id on a queue's chain.
func addCauses(t *testing.T, k *Kernel, queueID, id string) []string {
	t.Helper()
	events, err := k.Ledger.Read(queueID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		var q item.QueueEntry
		if e.Kind == item.KindQueueAdded && e.Decode(&q) == nil && q.Item == id {
			out = append(out, q.Cause)
		}
	}
	return out
}

func TestProducerCauseIdempotentAcrossFire(t *testing.T) {
	_, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	q := newQueue(t, k, nil, item.Trigger{Task: task.ID})
	out, err := k.Enqueue(q.ID, []string{s.ID}, "intake r1 d1", "me")
	if err != nil || !slices.Equal(out.Added, []string{s.ID}) {
		t.Fatalf("enqueue %+v %v", out, err)
	}
	if out, err = k.Enqueue(q.ID, []string{s.ID}, "intake r1 d1", "me"); err != nil || len(out.Added) != 0 || !slices.Equal(out.Already, []string{s.ID}) {
		t.Fatalf("retry before fire %+v %v", out, err)
	}
	f, err := k.Fire(q.ID, "", "by hand", "me")
	if err != nil || !slices.Equal(f.Inputs, []string{s.ID}) {
		t.Fatalf("fire %+v %v", f, err)
	}
	// A retry after the fire does not re-queue the item the run took.
	if out, err = k.Enqueue(q.ID, []string{s.ID}, "intake r1 d1", "me"); err != nil || len(out.Added) != 0 || !slices.Equal(out.Already, []string{s.ID}) {
		t.Fatalf("retry after fire %+v %v", out, err)
	}
	if got := addCauses(t, k, q.ID, s.ID); !slices.Equal(got, []string{"intake r1 d1"}) {
		t.Fatalf("adds after retry %v", got)
	}
	// A different cause is a different key: it adds again.
	if out, err = k.Enqueue(q.ID, []string{s.ID}, "intake r1 d2", "me"); err != nil || !slices.Equal(out.Added, []string{s.ID}) {
		t.Fatalf("new cause %+v %v", out, err)
	}
	if got := addCauses(t, k, q.ID, s.ID); !slices.Equal(got, []string{"intake r1 d1", "intake r1 d2"}) {
		t.Fatalf("adds after new cause %v", got)
	}
	// A retry of a recorded pair is a no-op even once the item is terminal.
	if _, err := k.Transition(context.Background(), s.ID, "done", "me"); err != nil {
		t.Fatal(err)
	}
	if out, err = k.Enqueue(q.ID, []string{s.ID}, "intake r1 d1", "me"); err != nil || len(out.Added) != 0 || !slices.Equal(out.Already, []string{s.ID}) {
		t.Fatalf("retry at terminal %+v %v", out, err)
	}
}

func TestEmptyCauseRequeuesAfterFire(t *testing.T) {
	_, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	q := newQueue(t, k, nil, item.Trigger{Task: task.ID})
	if _, err := k.Enqueue(q.ID, []string{s.ID}, "", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Fire(q.ID, "", "by hand", "me"); err != nil {
		t.Fatal(err)
	}
	out, err := k.Enqueue(q.ID, []string{s.ID}, "", "me")
	if err != nil || !slices.Equal(out.Added, []string{s.ID}) {
		t.Fatalf("re-add after fire %+v %v", out, err)
	}
	if got := addCauses(t, k, q.ID, s.ID); !slices.Equal(got, []string{"", ""}) {
		t.Fatalf("adds %q", got)
	}
}

func TestQueueFiresTaskSynchronously(t *testing.T) {
	_, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	q := newQueue(t, k, nil, item.Trigger{Task: task.ID, MinSize: 1})
	out, err := k.Enqueue(q.ID, []string{s.ID}, "", "me")
	if err != nil || out.Fire == nil || out.Fire.Run == "" {
		t.Fatalf("enqueue %+v %v", out, err)
	}
	// The run exists when Enqueue returns: the fire ran in this call.
	run, err := k.Get(out.Fire.Run)
	if err != nil {
		t.Fatal(err)
	}
	if run.Kind != ExecutionKind || run.Parent != task.ID || run.Workflow != "runbook" || run.Status != "backlog" || run.Title != task.Title {
		t.Fatalf("run %+v", run)
	}
	rec := runRecord(t, k, run.ID)
	if rec.Task != task.ID || rec.Queue != q.ID || !slices.Equal(rec.Inputs, []string{s.ID}) {
		t.Fatalf("task.run %+v", rec)
	}
}

func TestQueueFiresWorkflow(t *testing.T) {
	_, open := queueKernel(t)
	k := open()
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	check := func(q item.Item, runID string) {
		t.Helper()
		run, err := k.Get(runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Kind != ExecutionKind || run.Workflow != "triage" || run.Status != "open" || run.Parent != q.ID {
			t.Fatalf("run %+v", run)
		}
		rec := runRecord(t, k, run.ID)
		if rec.Task != "" || rec.Queue != q.ID || !slices.Equal(rec.Inputs, []string{s.ID}) {
			t.Fatalf("task.run %+v", rec)
		}
		events, _ := k.Ledger.Read(q.ID)
		var f item.QueueFire
		_ = events[len(events)-1].Decode(&f)
		if f.Workflow != "triage" || f.Run != run.ID {
			t.Fatalf("queue.fired %+v", f)
		}
	}
	auto := newQueue(t, k, nil, item.Trigger{Workflow: "triage", MinSize: 1})
	out, err := k.Enqueue(auto.ID, []string{s.ID}, "", "me")
	if err != nil || out.Fire == nil {
		t.Fatalf("enqueue %+v %v", out, err)
	}
	check(auto, out.Fire.Run)

	hand := newQueue(t, k, nil, item.Trigger{Workflow: "triage"})
	out, err = k.Enqueue(hand.ID, []string{s.ID}, "", "me")
	if err != nil || out.Fire != nil || out.Held != "" {
		t.Fatalf("min size 0 fired on enqueue %+v %v", out, err)
	}
	f, err := k.Fire(hand.ID, "", "by hand", "me")
	if err != nil || f.Run == "" {
		t.Fatalf("fire %+v %v", f, err)
	}
	check(hand, f.Run)
}

func TestQueueMinSizeAndOpenRun(t *testing.T) {
	_, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	q := newQueue(t, k, nil, item.Trigger{Task: task.ID, MinSize: 3})
	var ids []string
	for i := range 3 {
		s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
		ids = append(ids, s.ID)
		out, err := k.Enqueue(q.ID, []string{s.ID}, "", "me")
		if err != nil {
			t.Fatal(err)
		}
		if fired := out.Fire != nil; fired != (i == 2) {
			t.Fatalf("member %d fired=%v", i+1, fired)
		}
	}
	// A second fire while the run is open is refused, and held on enqueue.
	extra := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("x")})
	if _, err := k.Enqueue(q.ID, []string{extra.ID}, "", "me"); err != nil {
		t.Fatal(err)
	}
	_, err := k.Fire(q.ID, "", "by hand", "me")
	if !errors.Is(err, ErrRunOpen) {
		t.Fatalf("fire with an open run: %v", err)
	}
	_, st, _, _ := k.QueueMembers(q.ID)
	if res, err := k.Transition(context.Background(), st.LastFire.Run, "cancelled", "me"); err != nil || !res.Moved {
		t.Fatalf("cancel run %+v %v", res, err)
	}
	f, err := k.Fire(q.ID, "", "by hand", "me")
	if err != nil || !slices.Equal(f.Inputs, []string{extra.ID}) {
		t.Fatalf("fire after cancel %+v %v", f, err)
	}
	if len(ids) != 3 {
		t.Fatal(ids)
	}
}

func TestQueueEmptyFireAndStaleClaim(t *testing.T) {
	_, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	q := newQueue(t, k, nil, item.Trigger{Task: task.ID})
	f, err := k.Fire(q.ID, "", "by hand", "me")
	if err != nil || f.Run != "" {
		t.Fatalf("empty fire %+v %v", f, err)
	}
	// A claim at a stale tail fails and creates no run.
	events, _ := k.Ledger.Read(q.ID)
	stale := events[len(events)-1].Hash
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	if _, err := k.Enqueue(q.ID, []string{s.ID}, "", "me"); err != nil {
		t.Fatal(err)
	}
	_, err = k.appendAfter(q.ID, item.KindQueueFired, "me", stale, item.QueueFire{Run: "exe_00000000"})
	var st *ledger.StaleTail
	if !errors.As(err, &st) {
		t.Fatalf("stale claim: %v", err)
	}
	if runs, _ := k.Cache.Items(cache.Query{Kind: ExecutionKind}); len(runs) != 0 {
		t.Fatalf("runs %+v", runs)
	}
}

func TestStateEnqueue(t *testing.T) {
	root, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	k.Close()
	write(t, filepath.Join(root, "queues", "q.toml"), "title = \"q\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 1\n")
	k = open("q")
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	res, err := k.Transition(context.Background(), s.ID, "done", "me")
	if err != nil || !res.Moved || len(res.Queued) != 1 || res.Queued[0].Fire == nil {
		t.Fatalf("transition %+v %v", res, err)
	}
	if o := res.Queued[0]; o.Queue != "que_q" || o.Name != "q" {
		t.Fatalf("queued into %q (%q), want que_q (q)", o.Queue, o.Name)
	}
	events, _ := k.Ledger.Read("que_q")
	var e item.QueueEntry
	for _, ev := range events {
		if ev.Kind == item.KindQueueAdded {
			_ = ev.Decode(&e)
		}
	}
	if e.Item != s.ID || e.Cause != "default:backlog->done" {
		t.Fatalf("queue.added %+v", e)
	}
	if got := eventKinds(events, "queue."); !slices.Equal(got, []string{item.KindQueueAdded, item.KindQueueFired}) {
		t.Fatalf("queue chain %v", got)
	}

	// A state naming a queue with no file, or by id, does not open.
	k.Close()
	for _, ref := range []string{"nope", "que_q"} {
		write(t, filepath.Join(root, "workflows", "default.toml"), `name = "default"
[states.backlog]
start = true
[states.done]
terminal = true
enqueue = ["`+ref+`"]
[[transition]]
from = ["backlog"]
to = "done"
`)
		_, err := Open(Options{Root: root})
		if err == nil || !strings.Contains(err.Error(), "enqueues into \""+ref+"\", which has no .satelle/queues/"+ref+".toml") {
			t.Fatalf("enqueue %q opened: %v", ref, err)
		}
	}
}
