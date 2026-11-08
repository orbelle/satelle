package kernel

import (
	"errors"
	"fmt"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// DefaultBaseBranch is an item's base branch when its seat named none: the
// branch a merge of main into the item's branch brings in.
const DefaultBaseBranch = "origin/main"

// base is the commit an item's work is judged from as recorded: the latest
// bound seat's base, else the commit at create. ref is the --base ref that
// seat named, "" when it named none or the base is the create commit.
func (k *Kernel) base(it item.Item, events []ledger.Event) (commit, ref string) {
	seats := item.FoldSeats(events)
	for i := len(seats) - 1; i >= 0; i-- {
		if seats[i].Base != "" {
			return seats[i].Base, seats[i].BaseRef
		}
	}
	return it.Base, ""
}

// judgedBase is the commit a locally seated item's diff is judged from: the
// merge base of HEAD and the item's base branch (the seat's --base ref, else
// DefaultBaseBranch) when the recorded base is an ancestor of it, so merging
// that branch into the item's branch never widens the judged scope. The
// recorded base stands when the branch does not resolve, when the merge base
// is the recorded base or HEAD itself (HEAD is wholly inside the branch, so
// there is no own change to separate), or when the recorded base is not in the
// merge base's history (a stacked or story-branch seat, or a stale base).
func (k *Kernel) judgedBase(it item.Item, events []ledger.Event) string {
	recorded, ref := k.base(it, events)
	g, ok := k.Git.(BranchGit)
	if recorded == "" || !ok {
		return recorded
	}
	if ref == "" {
		ref = DefaultBaseBranch
	}
	tip, err := g.Resolve(ref)
	if err != nil {
		return recorded
	}
	head, err := k.Git.Head()
	if err != nil {
		return recorded
	}
	mb, err := g.MergeBase(head, tip)
	if err != nil || mb == recorded || mb == head {
		return recorded
	}
	if in, err := g.IsAncestor(recorded, mb); err != nil || !in {
		return recorded
	}
	return mb
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

// ItemDiff is an item's change: the paths changed from Base to Head, or to
// the working tree when Head is "". Frozen says it is the change the item
// reached its terminal state with, which later work does not move.
type ItemDiff struct {
	Base, Head string
	Paths      []string
	Frozen     bool
}

// ItemChange returns id's change. A terminal item's is the span recorded on
// its move into that state; any other item's is this working tree's diff
// from its judged base, with the stale-base finding when HEAD's history no
// longer contains that base. Base is "" when the item has none.
func (k *Kernel) ItemChange(id string) (ItemDiff, *StaleBaseError, error) {
	it, events, err := k.load(id)
	if err != nil {
		return ItemDiff{}, nil, err
	}
	w, err := k.workflowOf(it)
	if err != nil {
		return ItemDiff{}, nil, err
	}
	if w.States[it.Status].Terminal {
		for i := len(events) - 1; i >= 0; i-- {
			var m item.Move
			if events[i].Kind != item.KindTransitioned || events[i].Decode(&m) != nil || m.To != it.Status {
				continue
			}
			if m.Change == nil {
				break
			}
			bg, ok := k.Git.(BranchGit)
			if !ok {
				return ItemDiff{}, nil, fmt.Errorf("%s: its frozen diff needs a git work tree", id)
			}
			paths, err := bg.ChangedBetween(m.Change.Base, m.Change.Head)
			if err != nil {
				return ItemDiff{}, nil, err
			}
			return ItemDiff{Base: m.Change.Base, Head: m.Change.Head, Paths: paths, Frozen: true}, nil, nil
		}
		if recorded, _ := k.base(it, events); recorded == "" {
			return ItemDiff{}, nil, nil
		}
		return ItemDiff{}, nil, fmt.Errorf("%s reached %s before satelle recorded the change it reached it with; it has no frozen diff", id, it.Status)
	}
	sp := span{base: k.judgedBase(it, events)}
	d := ItemDiff{Base: sp.base}
	if d.Base == "" || k.Git == nil {
		return d, nil, nil
	}
	stale := k.staleBase(it, sp)
	if d.Paths, err = k.Git.ChangedPaths(d.Base); err != nil {
		return ItemDiff{}, nil, err
	}
	return d, stale, nil
}

// defaultCollectBase is the branch a collected branch's diff base is taken
// from when collect is given no --base: its merge base with origin/main. HEAD
// is never used, since a tree whose HEAD already is the branch would make the
// base the branch's own commit and the span empty.
const defaultCollectBase = DefaultBaseBranch

// resolveBase resolves a --base ref to its commit, now.
func (k *Kernel) resolveBase(ref string) (string, error) {
	g, ok := k.Git.(BranchGit)
	if !ok {
		return "", errors.New("--base needs a git work tree")
	}
	commit, err := g.Resolve(ref)
	if err != nil {
		return "", fmt.Errorf("--base %s names no commit here (fetch it first)", ref)
	}
	return commit, nil
}

// seatBase resolves a seat's --base to a commit in HEAD's history.
func (k *Kernel) seatBase(ref string) (string, error) {
	commit, err := k.resolveBase(ref)
	if err != nil {
		return "", err
	}
	g := k.Git.(BranchGit)
	head, err := k.Git.Head()
	if err != nil {
		return "", err
	}
	if in, err := g.IsAncestor(commit, head); err != nil || !in {
		return "", fmt.Errorf("--base %s (%s) is not in HEAD's history; the item's gated transitions would refuse it", ref, commit)
	}
	return commit, nil
}
