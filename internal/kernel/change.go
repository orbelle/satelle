package kernel

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// latestChange is the newest gate verdict that accepted (or carried) a
// recorded change; false when there is none, as for results recorded before
// changes were. It chooses the span a terminal move records, nothing else.
func latestChange(results []gate.Result) (gate.Result, bool) {
	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		if r.Change != nil && (r.Result == gate.Accept || r.Result == gate.Carried) {
			return r, true
		}
	}
	return gate.Result{}, false
}

// runSpan is the base, head and branch a presentation over sp judges: a
// collected branch's head and name, else this tree's HEAD and branch.
func (k *Kernel) runSpan(events []ledger.Event, sp span) gate.Change {
	c := gate.Change{Base: sp.base, Head: sp.head}
	if sp.head != "" {
		if col, ok := collected(events); ok {
			c.Branch = col.Branch
		}
		return c
	}
	c.Head, _ = k.Git.Head()
	if bg, ok := k.Git.(BranchGit); ok {
		c.Branch, _ = bg.Branch()
	}
	return c
}

// changeNow is the item's own change now, over the paths a judge recorded
// with rec saw, and the head it ends at. The judged branch is read when it
// still resolves: this tree itself when it is that branch (uncommitted work
// included), else the branch's commits, whatever this checkout's HEAD
// carries. A detached judge, or a branch gone, reads the presented span.
func (k *Kernel) changeNow(rec gate.Change, sp span, paths []string) (head, diff string, err error) {
	if bg, ok := k.Git.(BranchGit); ok && rec.Branch != "" {
		if h, rerr := bg.Resolve(rec.Branch); rerr == nil {
			if cur, _ := bg.Branch(); sp.head == "" && cur == rec.Branch {
				head, err = k.Git.Head()
				if err != nil {
					return "", "", err
				}
				diff, err = k.Git.Diff(rec.Base, paths)
				return head, diff, err
			}
			diff, err = bg.DiffBetween(rec.Base, h, paths)
			return h, diff, err
		}
	}
	head = sp.head
	if head == "" {
		if head, err = k.Git.Head(); err != nil {
			return "", "", err
		}
	}
	diff, err = k.diffSpan(span{base: rec.Base, head: sp.head}, paths)
	return head, diff, err
}

// gatePaths are the paths gate name of w judges; nil when it is not defined.
func gatePaths(w *workflow.Workflow, name string) []string {
	if g, ok := w.Gates[name]; ok {
		return g.Paths
	}
	return nil
}

// frozenSpan is the span a move of it into a terminal state records, given
// results, its gate verdicts oldest first: from the latest accepted change's
// base to the head that change ends at now, else the presented span; nil when
// there is no base or head to record. It never decides whether the move
// happens.
func (k *Kernel) frozenSpan(it item.Item, w *workflow.Workflow, sp span, results []gate.Result) (*item.Span, error) {
	rec, ok := latestChange(results)
	if !ok {
		head := sp.head
		if head == "" {
			head, _ = k.Git.Head()
		}
		if sp.base == "" || head == "" {
			return nil, nil
		}
		return &item.Span{Base: sp.base, Head: head}, nil
	}
	head, _, err := k.changeNow(*rec.Change, sp, gatePaths(w, rec.Gate))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the change gate %s accepted on %s: %w", it.ID, rec.Gate, rec.Transition, err)
	}
	return &item.Span{Base: rec.Change.Base, Head: head}, nil
}

// JudgedChange is one recorded gate verdict over a change: what the gate saw
// (Judged, the hash of the diff alone) and the item's change now over the same
// base, branch and paths (Now, ending at NowHead). The binary reports both;
// whether they must match is a workflow's check. Now is the hash of the pinned
// diff text alone: a verdict hashed over other text is a mismatch, even at the
// head it recorded.
type JudgedChange struct {
	Gate, Transition, Result, Attempt string
	Base, Branch                      string
	Judged, Now, NowHead              string
}

// JudgedChanges lists id's gate verdicts that recorded a change, oldest first,
// each with the change now. A terminal item's change is frozen, so it has
// none; nor has an item outside a git work tree.
func (k *Kernel) JudgedChanges(id string) ([]JudgedChange, error) {
	it, events, err := k.load(id)
	if err != nil {
		return nil, err
	}
	w, err := k.workflowOf(it)
	if err != nil {
		return nil, err
	}
	out := []JudgedChange{}
	if k.Git == nil || w.States[it.Status].Terminal {
		return out, nil
	}
	sp := k.spanOf(it, events)
	type now struct{ hash, head string }
	seen := map[string]now{}
	for _, e := range events {
		var r gate.Result
		if e.Kind != gate.KindRun || e.Decode(&r) != nil || r.Change == nil {
			continue
		}
		paths := gatePaths(w, r.Gate)
		key := strings.Join([]string{r.Change.Base, r.Change.Branch, r.Change.Head, strings.Join(paths, "\x1f")}, "\x00")
		n, ok := seen[key]
		if !ok {
			head, diff, err := k.changeNow(*r.Change, sp, paths)
			if err != nil {
				return nil, fmt.Errorf("%s: reading the change gate %s saw on %s: %w", id, r.Gate, r.Transition, err)
			}
			n = now{hash: changeHash(diff), head: head}
			seen[key] = n
		}
		out = append(out, JudgedChange{
			Gate: r.Gate, Transition: r.Transition, Result: r.Result, Attempt: r.Attempt,
			Base: r.Change.Base, Branch: r.Change.Branch,
			Judged: r.Change.Hash, Now: n.hash, NowHead: n.head,
		})
	}
	return out, nil
}
