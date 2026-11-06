package kernel

import (
	"errors"
	"fmt"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// base is the commit an item's work is judged from: the latest bound seat's
// base, else the commit at create.
func (k *Kernel) base(it item.Item, events []ledger.Event) string {
	seats := item.FoldSeats(events)
	for i := len(seats) - 1; i >= 0; i-- {
		if seats[i].Base != "" {
			return seats[i].Base
		}
	}
	return it.Base
}

// StaleBaseError refuses a gated transition whose diff base is no longer in
// HEAD's history: after a rebase or reset, the diff from it is not the item's
// own change, so every gate would judge the wrong slice.
type StaleBaseError struct {
	Item, Kind, Base, Head string
	// Unknown is set when the base names no commit in this repository.
	Unknown bool
}

func (e *StaleBaseError) Error() string {
	what := fmt.Sprintf("its diff base %s is not in HEAD's history (HEAD is %s); the branch was rebased or reset since the base was recorded", e.Base, e.Head)
	if e.Unknown {
		what = fmt.Sprintf("its diff base %s is not a commit in this repository", e.Base)
	}
	return fmt.Sprintf("%s: %s, so its gates would judge the wrong change. Re-seat on the commit the item's own change starts from: satelle %s seat %s --base <ref> (its parent branch, e.g. main, or the branch it is stacked on), then present again",
		e.Item, what, e.Kind, e.Item)
}

// staleBase reports a worktree span whose base HEAD's history no longer
// contains. A collected branch's base is a merge-base of its head, so it is
// never stale; without branch facts nothing is checked.
func (k *Kernel) staleBase(it item.Item, sp span) *StaleBaseError {
	g, ok := k.Git.(BranchGit)
	if !ok || sp.head != "" || sp.base == "" {
		return nil
	}
	head, err := k.Git.Head()
	if err != nil {
		return nil
	}
	in, err := g.IsAncestor(sp.base, head)
	if err != nil {
		return &StaleBaseError{Item: it.ID, Kind: it.Kind, Base: sp.base, Head: head, Unknown: true}
	}
	if !in {
		return &StaleBaseError{Item: it.ID, Kind: it.Kind, Base: sp.base, Head: head}
	}
	return nil
}

// ItemBase returns the base id's working-tree diff is judged from, and the
// stale-base finding when HEAD's history no longer contains it.
func (k *Kernel) ItemBase(id string) (string, *StaleBaseError, error) {
	it, events, err := k.load(id)
	if err != nil {
		return "", nil, err
	}
	sp := span{base: k.base(it, events)}
	return sp.base, k.staleBase(it, sp), nil
}

// seatBase resolves a seat's --base to a commit in HEAD's history.
func (k *Kernel) seatBase(ref string) (string, error) {
	g, ok := k.Git.(BranchGit)
	if !ok {
		return "", errors.New("--base needs a git work tree")
	}
	commit, err := g.Resolve(ref)
	if err != nil {
		return "", fmt.Errorf("--base %s names no commit here (fetch it first)", ref)
	}
	head, err := k.Git.Head()
	if err != nil {
		return "", err
	}
	if in, err := g.IsAncestor(commit, head); err != nil || !in {
		return "", fmt.Errorf("--base %s (%s) is not in HEAD's history; the item's gated transitions would refuse it", ref, commit)
	}
	return commit, nil
}
