package kernel

import (
	"fmt"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// UncoveredError refuses a move into a terminal state when the item's change
// is not the one the latest accepted model gate saw: a commit made after the
// judge would otherwise land unjudged.
type UncoveredError struct {
	Item, Kind, To string
	// Gate and Transition name the judge to re-run and where it sits.
	Gate, Transition string
	// Judged and Now are the change hashes: what the judge accepted, and the
	// item's change now.
	Judged, Now string
}

func (e *UncoveredError) Error() string {
	from, to := gate.FromOf(e.Transition), gate.ToOf(e.Transition)
	return fmt.Sprintf("%s: its change is not the one gate %s accepted on %s -> %s (judged %s, now %s); a terminal state is reached only with a change a judge accepted. Re-run %s: return the item to %s, present %s again, then present %s again",
		e.Item, e.Gate, from, to, short(e.Judged), short(e.Now), e.Gate, from, to, e.To)
}

// latestJudged is the newest model gate verdict that accepted (or carried) a
// change; false when there is none, as for results recorded before changes
// were.
func latestJudged(results []gate.Result) (gate.Result, bool) {
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

// uncovered checks a move of it into terminal state to against results, the
// item's gate verdicts oldest first: the refusal when its change now is not
// the one the latest accepted model gate saw, else the span its change is
// frozen at (nil when it has no base or head to freeze).
func (k *Kernel) uncovered(it item.Item, to string, w *workflow.Workflow, sp span, results []gate.Result) (*UncoveredError, *item.Span, error) {
	rec, ok := latestJudged(results)
	if !ok {
		head := sp.head
		if head == "" {
			head, _ = k.Git.Head()
		}
		if sp.base == "" || head == "" {
			return nil, nil, nil
		}
		return nil, &item.Span{Base: sp.base, Head: head}, nil
	}
	var paths []string
	if g, defined := w.Gates[rec.Gate]; defined {
		paths = g.Paths
	}
	head, diff, err := k.changeNow(*rec.Change, sp, paths)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: reading the change gate %s accepted on %s: %w", it.ID, rec.Gate, rec.Transition, err)
	}
	if now := changeHash(diff); now != rec.Change.Hash {
		return &UncoveredError{Item: it.ID, Kind: it.Kind, To: to, Gate: rec.Gate, Transition: rec.Transition, Judged: rec.Change.Hash, Now: now}, nil, nil
	}
	return nil, &item.Span{Base: rec.Change.Base, Head: head}, nil
}
