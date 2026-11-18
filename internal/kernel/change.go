package kernel

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func latestChange(results []gate.Result) (gate.Result, bool) {
	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		if r.Change != nil && (r.Result == gate.Accept || r.Result == gate.Carried) {
			return r, true
		}
	}
	return gate.Result{}, false
}

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

func (k *Kernel) changeNow(rec gate.Change, sp span, paths []string) (head, diff string, err error) {
	if bg, ok := k.Git.(BranchGit); ok && rec.Branch != "" {
		if h, rerr := bg.Resolve(rec.Branch); rerr == nil {
			if cur, _ := bg.Branch(); sp.head == "" && cur == rec.Branch {
				head, err = k.Git.Head()
				if err != nil {
					return "", "", err
				}
				diff, err = k.diffSpan(span{base: rec.Base}, paths)
				return head, diff, err
			}
			diff, err = k.diffSpan(span{base: rec.Base, head: h}, paths)
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

func gatePaths(w *workflow.Workflow, name string) []string {
	if g, ok := w.Gates[name]; ok {
		return g.Paths
	}
	return nil
}

func (k *Kernel) frozenSpan(it item.Item, w *workflow.Workflow, sp span, results []gate.Result) (*item.Span, error) {
	rec, ok := latestChange(results)
	if !ok || rec.Change.Base == "" {
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

type JudgedChange struct {
	Gate, Transition, Result, Attempt string
	Base, Branch                      string
	Judged, Now, NowHead              string
}

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
	for _, r := range GateRuns(events) {
		if r.Change == nil {
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
