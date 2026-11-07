package kernel

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// lapseKernel is a repo whose delivery workflow enqueues an approved item into
// the release queue: backlog -> in_progress -> approved, approved sent back to
// in_progress or moved to done, and backlog -> done. The release queue has no
// filter (a status filter would mask a lapse), fires by hand only, and runs
// the runbook workflow.
func lapseKernel(t *testing.T) *Kernel {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "satelle.toml"), `[workflows]
default = "delivery"
[[workflows.rule]]
workflow = "runbook"
kind = "execution"
`)
	write(t, filepath.Join(root, "workflows", "runbook.toml"), `name = "runbook"
[states.backlog]
start = true
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "done"
`)
	write(t, filepath.Join(root, "workflows", "delivery.toml"), `name = "delivery"
[states.backlog]
start = true
[states.in_progress]
[states.approved]
enqueue = ["release"]
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "approved"
[[transition]]
from = ["approved"]
to = "in_progress"
[[transition]]
from = ["approved", "backlog"]
to = "done"
`)
	write(t, filepath.Join(root, "queues", "release.toml"), "title = \"release\"\n[trigger]\nworkflow = \"runbook\"\nmin_size = 0\n")
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

// move transitions id through the states given, failing on a refusal.
func move(t *testing.T, k *Kernel, id string, to ...string) TransitionResult {
	t.Helper()
	var res TransitionResult
	for _, s := range to {
		var err error
		if res, err = k.Transition(context.Background(), id, s, "me"); err != nil || !res.Moved {
			t.Fatalf("%s -> %s: %+v %v", id, s, res, err)
		}
	}
	return res
}

func story(t *testing.T, k *Kernel, title string) string {
	t.Helper()
	return mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr(title)}).ID
}

// queueAdds is the queue.added entries on the release chain for id.
func queueAdds(t *testing.T, k *Kernel, id string) []item.QueueEntry {
	t.Helper()
	qid, _ := k.ResolveQueue("release")
	events, err := k.Ledger.Read(qid)
	if err != nil {
		t.Fatal(err)
	}
	var out []item.QueueEntry
	for _, e := range events {
		var q item.QueueEntry
		if e.Kind == item.KindQueueAdded && e.Decode(&q) == nil && q.Item == id {
			out = append(out, q)
		}
	}
	return out
}

func TestSentBackMemberNotFired(t *testing.T) {
	k := lapseKernel(t)
	a := story(t, k, "a")
	res := move(t, k, a, "in_progress", "approved")
	if len(res.Queued) != 1 || !slices.Equal(res.Queued[0].Added, []string{a}) {
		t.Fatalf("approve %+v", res.Queued)
	}
	move(t, k, a, "in_progress") // sent back, no fire in between

	_, st, ms, err := k.QueueMembers("release")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ms.Members, a) || !slices.Equal(ms.Lapsed, []item.Lapse{{ID: a, Status: "in_progress"}}) {
		t.Fatalf("sent back: members %v lapsed %v", ms.Members, ms.Lapsed)
	}
	if !slices.Contains(st.Explicit, a) || len(queueAdds(t, k, a)) != 1 {
		t.Fatalf("the ledger lost a's queue.added: explicit %v", st.Explicit)
	}

	b := story(t, k, "b")
	move(t, k, b, "in_progress", "approved")
	f, err := k.Fire("release", "", "by hand", "me")
	if err != nil || f.Run == "" || !slices.Equal(f.Inputs, []string{b}) {
		t.Fatalf("fire %+v %v, want inputs [%s]", f, err, b)
	}
	if rec := runRecord(t, k, f.Run); !slices.Equal(rec.Inputs, []string{b}) {
		t.Fatalf("task.run inputs %v", rec.Inputs)
	}
}

func TestDoneMemberNotFired(t *testing.T) {
	k := lapseKernel(t)
	c := story(t, k, "c")
	move(t, k, c, "in_progress", "approved", "done")
	f, err := k.Fire("release", "", "by hand", "me")
	if err != nil || f.Run != "" || len(f.Inputs) != 0 {
		t.Fatalf("fire %+v %v, want nothing taken", f, err)
	}
	qid, _ := k.ResolveQueue("release")
	events, _ := k.Ledger.Read(qid)
	if got := eventKinds(events, "queue."); !slices.Equal(got, []string{item.KindQueueAdded}) {
		t.Fatalf("queue chain %v, want only c's queue.added", got)
	}
}

func TestReapproveEnqueuesAgain(t *testing.T) {
	k := lapseKernel(t)
	a := story(t, k, "a")
	move(t, k, a, "in_progress", "approved", "in_progress")
	res := move(t, k, a, "approved")
	if len(res.Queued) != 1 || !slices.Equal(res.Queued[0].Added, []string{a}) || len(res.Queued[0].Already) != 0 {
		t.Fatalf("re-approve %+v", res.Queued)
	}
	adds := queueAdds(t, k, a)
	if len(adds) != 2 || adds[1].Cause != "delivery:in_progress->approved" {
		t.Fatalf("queue.added for %s: %+v", a, adds)
	}
	// A hand add of a live member is still a duplicate.
	o, err := k.Enqueue("release", []string{a}, "", "me")
	if err != nil || !slices.Equal(o.Already, []string{a}) || len(o.Added) != 0 || len(queueAdds(t, k, a)) != 2 {
		t.Fatalf("hand re-add %+v %v", o, err)
	}
	f, err := k.Fire("release", "", "by hand", "me")
	if err != nil || !slices.Equal(f.Inputs, []string{a}) {
		t.Fatalf("fire %+v %v", f, err)
	}
}

func TestHandMemberLapsesAtTerminal(t *testing.T) {
	k := lapseKernel(t)
	s := story(t, k, "s")
	if o, err := k.Enqueue("release", []string{s}, "", "me"); err != nil || !slices.Equal(o.Added, []string{s}) {
		t.Fatalf("hand add %+v %v", o, err)
	}
	if _, _, ms, _ := k.QueueMembers("release"); !slices.Equal(ms.Members, []string{s}) {
		t.Fatalf("live hand member: %+v", ms)
	}
	move(t, k, s, "done")
	_, _, ms, err := k.QueueMembers("release")
	if err != nil || len(ms.Members) != 0 || !slices.Equal(ms.Lapsed, []item.Lapse{{ID: s, Status: "done"}}) {
		t.Fatalf("after done: %+v %v", ms, err)
	}
	if f, err := k.Fire("release", "", "by hand", "me"); err != nil || f.Run != "" {
		t.Fatalf("fire %+v %v", f, err)
	}
	if _, err := k.Enqueue("release", []string{s}, "", "me"); err == nil || !strings.Contains(err.Error(), "a terminal state") {
		t.Fatalf("hand add of a done item: %v", err)
	}
}
