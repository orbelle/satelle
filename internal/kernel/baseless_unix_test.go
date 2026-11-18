//go:build !windows

package kernel

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
)

func baseless(t *testing.T, k *Kernel) item.Item {
	t.Helper()
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t"), Base: ptr("")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if it.Base != "" {
		t.Fatalf("item base = %q, want none", it.Base)
	}
	return it
}

type strictGit struct {
	Git
	t *testing.T
}

func (g strictGit) bg() BranchGit { return g.Git.(BranchGit) }

func (g strictGit) empty(what string, revs ...string) {
	for _, r := range revs {
		if r == "" {
			g.t.Errorf("git %s read with an empty revision", what)
		}
	}
}

func (g strictGit) Diff(base string, p []string) (string, error) {
	g.empty("diff", base)
	return g.Git.Diff(base, p)
}

func (g strictGit) ChangedPaths(base string) ([]string, error) {
	g.empty("changed paths", base)
	return g.Git.ChangedPaths(base)
}

func (g strictGit) Resolve(ref string) (string, error)    { return g.bg().Resolve(ref) }
func (g strictGit) MergeBase(a, b string) (string, error) { return g.bg().MergeBase(a, b) }
func (g strictGit) IsAncestor(a, b string) (bool, error)  { return g.bg().IsAncestor(a, b) }
func (g strictGit) Branch() (string, error)               { return g.bg().Branch() }

func (g strictGit) ChangedBetween(base, head string) ([]string, error) {
	g.empty("changed between", base, head)
	return g.bg().ChangedBetween(base, head)
}

func (g strictGit) DiffBetween(base, head string, p []string) (string, error) {
	g.empty("diff between", base, head)
	return g.bg().DiffBetween(base, head, p)
}

func TestABaselessItemReachesAJudgedTerminalState(t *testing.T) {
	t.Run("no base ever", func(t *testing.T) {
		_, k := coveredKernel(t)
		it := baseless(t, k)
		moveTo(t, k, it.ID, "r")
		res := moveTo(t, k, it.ID, "x")
		if g := res.Gates[0]; g.Gate != "final" || g.Result != gate.Accept || g.Change == nil || g.Change.Base != "" {
			t.Fatalf("final = %+v, want an accept that recorded a change with no base", g)
		}
		got, err := k.Get(it.ID)
		if err != nil || got.Status != "x" {
			t.Fatalf("status = %q, err %v, want x", got.Status, err)
		}
		if fs := frozenOf(t, k, it.ID, "x"); fs != nil {
			t.Fatalf("frozen change = %+v, want none", fs)
		}
		if d, _, err := k.ItemChange(it.ID); err != nil || len(d.Paths) != 0 || d.Base != "" {
			t.Fatalf("item change = %+v, err %v, want an empty change", d, err)
		}
	})
	t.Run("a base recorded after the judgment", func(t *testing.T) {
		dir, k := coveredKernel(t)
		it := baseless(t, k)
		moveTo(t, k, it.ID, "r")
		seat, err := k.Seat(it.ID, "s1", "local:"+dir, "", "ada")
		if err != nil {
			t.Fatal(err)
		}
		moveTo(t, k, it.ID, "x")
		head := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD"))
		if fs := frozenOf(t, k, it.ID, "x"); fs == nil || fs.Base != seat.Base || fs.Head != head {
			t.Fatalf("frozen change = %+v, want base %s head %s", fs, seat.Base, head)
		}
	})
}

func TestABaselessChangeRunsNoGitDiff(t *testing.T) {
	for _, other := range []bool{false, true} {
		dir, k := coveredKernel(t)
		k.Git = strictGit{Git: k.Git, t: t}
		it := baseless(t, k)
		moveTo(t, k, it.ID, "r")
		if other {
			gittest.Git(t, dir, "checkout", "-q", "main")
		}
		js, err := k.JudgedChanges(it.ID)
		if err != nil || len(js) != 1 {
			t.Fatalf("other branch %v: judged = %+v, err %v", other, js, err)
		}
		if j := js[0]; j.Base != "" || j.Now != j.Judged || j.Now != changeHash("") {
			t.Fatalf("other branch %v: judged = %+v, want an empty change read as empty", other, j)
		}
		if other {
			gittest.Git(t, dir, "checkout", "-q", "feat/x")
		}
		moveTo(t, k, it.ID, "x")
	}
}

func TestABaselessTransitionReportsNoGitError(t *testing.T) {
	_, k := coveredKernel(t)
	d := k.Dispatcher.(*fakeDispatcher)
	it := baseless(t, k)
	clean := func(res TransitionResult) {
		t.Helper()
		if res.Item.ID != it.ID {
			t.Fatalf("result names %q, want %s", res.Item.ID, it.ID)
		}
		for _, g := range res.Gates {
			if strings.Contains(g.Notes, "bad revision") || strings.Contains(g.Warning, "bad revision") {
				t.Fatalf("gate %s: %+v", g.Gate, g)
			}
		}
	}
	d.set("r1", reject)
	res, err := k.Transition(context.Background(), it.ID, "r", "ada")
	if err != nil || res.Moved || len(res.Rejects()) != 1 {
		t.Fatalf("refused move: moved %v, rejects %d, err %v", res.Moved, len(res.Rejects()), err)
	}
	clean(res)
	if _, err := k.JudgedChanges(it.ID); err != nil {
		t.Fatalf("judged changes after a baseless reject: %v", err)
	}
	d.set("r1", accept)
	clean(moveTo(t, k, it.ID, "r"))
	clean(moveTo(t, k, it.ID, "x"))
}
