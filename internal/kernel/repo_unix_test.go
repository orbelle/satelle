//go:build !windows

package kernel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

func targetGhost(t *testing.T, k *Kernel, id string) {
	t.Helper()
	if _, err := k.Ledger.Append(id, item.KindUpdated, "me", item.Fields{Repo: ptr("ghost")}); err != nil {
		t.Fatal(err)
	}
}

func refusedTarget(t *testing.T, err error, name string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "does not register") || !strings.Contains(err.Error(), name) {
		t.Fatalf("err = %v, want the target refusal naming %s", err, name)
	}
}

func executionsOf(t *testing.T, k *Kernel) []item.Item {
	t.Helper()
	runs, err := k.List(cache.Query{Kind: ExecutionKind})
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func TestRepoCreateAndUpdate(t *testing.T) {
	k, _ := lineKernel(t, "", "")
	k.Repos = reporeg.Registry{Tracked: map[string]reporeg.Entry{"web": {URL: "https://github.com/o/web"}}}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t"), Repo: ptr("web")}, "me")
	if err != nil || it.Repo != "web" {
		t.Fatalf("create %+v %v", it, err)
	}
	if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t"), Repo: ptr("")}, "me"); err == nil {
		t.Fatal("an empty repo was accepted")
	}
	k.Repos = reporeg.Registry{}
	err = k.CheckTarget(it)
	refusedTarget(t, err, "web")
	if !strings.Contains(err.Error(), "registered: self") {
		t.Fatalf("err = %v", err)
	}
}

func TestPerformRefusesUnregisteredTargetBeforeLaunch(t *testing.T) {
	for _, placement := range []string{"", "remote:ci", "cloud"} {
		t.Run("placement="+placement, func(t *testing.T) {
			k, d := lineKernel(t, placement, "")
			it := newItem(t, k)
			if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
				t.Fatal(err)
			}
			targetGhost(t, k, it.ID)
			r := &fakeRemote{}
			var got agent.Request
			k.Remote = r
			k.Cloud = fakeCloud{got: &got}
			_, err := k.Perform(context.Background(), it.ID, "ada")
			refusedTarget(t, err, "ghost")
			if len(d.calls) != 0 || len(r.started) != 0 || got.Binding.Name != "" {
				t.Fatalf("launched: calls %v, started %v, cloud %q", d.calls, r.started, got.Binding.Name)
			}
			for _, kind := range kinds(t, k, it.ID) {
				if kind == item.KindSeatBound || kind == KindPerformerRun {
					t.Fatalf("a refused perform wrote %s", kind)
				}
			}
		})
	}
	t.Run("cloud by hand", func(t *testing.T) {
		k, _ := lineKernel(t, "cloud", "")
		it := newItem(t, k)
		if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
			t.Fatal(err)
		}
		targetGhost(t, k, it.ID)
		_, err := k.Perform(context.Background(), it.ID, "ada")
		refusedTarget(t, err, "ghost")
		if errors.Is(err, ErrCloudByHand) {
			t.Fatal("refused with the by-hand hint, not the target")
		}
	})
}

func TestGatedTransitionRefusesUnregisteredTarget(t *testing.T) {
	k, d := lineKernel(t, "", "")
	it := newItem(t, k)
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	targetGhost(t, k, it.ID)
	before := kinds(t, k, it.ID)
	_, err := k.Transition(context.Background(), it.ID, "c", "ada")
	refusedTarget(t, err, "ghost")
	if len(d.calls) != 0 || !slices.Equal(before, kinds(t, k, it.ID)) {
		t.Fatalf("gated transition ran: calls %v, events %v", d.calls, kinds(t, k, it.ID))
	}
	if res, err := k.Transition(context.Background(), it.ID, "held", "ada"); err != nil || !res.Moved {
		t.Fatalf("groupless transition = %+v, %v", res, err)
	}
}

func TestSeatRefusesUnregisteredTarget(t *testing.T) {
	k, _ := lineKernel(t, "", "")
	it := newItem(t, k)
	targetGhost(t, k, it.ID)
	before := kinds(t, k, it.ID)
	_, err := k.Seat(it.ID, "s-1", "local:/x", "", "me")
	refusedTarget(t, err, "ghost")
	if !slices.Equal(before, kinds(t, k, it.ID)) {
		t.Fatal("a refused seat appended")
	}
}

func TestTaskRunRefusesUnregisteredTarget(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		_, open := queueKernel(t)
		k := open()
		task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("r"), Inputs: &item.Selection{Tag: "t"}})
		s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s"), Tags: ptr([]string{"t"})})
		targetGhost(t, k, s.ID)
		_, _, err := k.TaskRun(task.ID, "", "me")
		refusedTarget(t, err, "ghost")
		if runs := executionsOf(t, k); len(runs) != 0 {
			t.Fatalf("executions %v", runs)
		}
		refusedTarget(t, k.CheckFire(task.ID), "ghost")
	})
	t.Run("task", func(t *testing.T) {
		_, open := queueKernel(t)
		k := open()
		task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("r"), Inputs: &item.Selection{Tag: "none"}})
		targetGhost(t, k, task.ID)
		_, _, err := k.TaskRun(task.ID, "", "me")
		refusedTarget(t, err, "ghost")
		if runs := executionsOf(t, k); len(runs) != 0 {
			t.Fatalf("executions %v", runs)
		}
	})
}

func TestCheckFireChecksInputs(t *testing.T) {
	_, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("r"), Inputs: &item.Selection{Tag: "t"}})
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s"), Tags: ptr([]string{"t"})})
	if err := k.CheckFire(task.ID); err != nil {
		t.Fatalf("registered inputs refused: %v", err)
	}
	targetGhost(t, k, s.ID)
	refusedTarget(t, k.CheckFire(task.ID), "ghost")
}

func TestQueueFireRefusesUnregisteredMember(t *testing.T) {
	f := newOverlapFixture(t, 0)
	k := f.k
	targetGhost(t, k, f.s1)
	before := len(firedClaims(t, k, "que_q"))
	_, err := k.Fire("q", "", "by hand", "me")
	refusedTarget(t, err, "ghost")
	if n := len(firedClaims(t, k, "que_q")); n != before {
		t.Fatalf("a refused fire wrote a claim: %d, was %d", n, before)
	}
	if runs := executionsOf(t, k); len(runs) != 0 {
		t.Fatalf("executions %v", runs)
	}
}

func TestQueueFireUnsettledRefusesUnregisteredMember(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ghost func(f overlapFixture) string
	}{
		{"unregistered member", func(f overlapFixture) string { return f.s1 }},
		{"unregistered trigger task", func(f overlapFixture) string { return f.task.ID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOverlapFixture(t, 0)
			k := f.k
			targetGhost(t, k, tc.ghost(f))
			run := f.claim(t)
			before := len(firedClaims(t, k, "que_q"))
			_, err := k.Fire("q", "", "by hand", "me")
			refusedTarget(t, err, "ghost")
			if !strings.Contains(err.Error(), "queue que_q claimed its members for "+run+", but the run was not created") || errors.Is(err, ErrQueueMoved) {
				t.Fatalf("err = %v", err)
			}
			if n, _ := k.Ledger.Size(run); n != 0 {
				t.Fatalf("%s was created over an unregistered target", run)
			}
			if n := len(firedClaims(t, k, "que_q")); n != before {
				t.Fatalf("claims %d, was %d", n, before)
			}
			if runs := executionsOf(t, k); len(runs) != 0 {
				t.Fatalf("executions %v", runs)
			}
		})
	}
	t.Run("legacy claim settles", func(t *testing.T) {
		f := newOverlapFixture(t, 0)
		k := f.k
		run := f.claim(t)
		_, err := k.Fire("q", "", "by hand", "me")
		if !errors.Is(err, ErrQueueMoved) || !strings.Contains(err.Error(), run) {
			t.Fatalf("err = %v", err)
		}
		if rec := ledgerRun(t, k, run); !slices.Equal(rec.Inputs, []string{f.s1, f.s2}) {
			t.Fatalf("settled %s took %v", run, rec.Inputs)
		}
	})
}
