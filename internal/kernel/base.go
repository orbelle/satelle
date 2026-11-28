package kernel

import (
	"errors"
	"fmt"

	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

const DefaultBaseBranch = "origin/main"

func (k *Kernel) base(it item.Item, events []ledger.Event) (commit, ref string) {
	seats := item.FoldSeats(events)
	for i := len(seats) - 1; i >= 0; i-- {
		if seats[i].Base != "" {
			return seats[i].Base, seats[i].BaseRef
		}
	}
	return it.Base, ""
}

func (k *Kernel) judgedBase(git Git, it item.Item, events []ledger.Event) string {
	recorded, ref := k.base(it, events)
	g, ok := git.(BranchGit)
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
	head, err := git.Head()
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

type StaleBaseError struct {
	Item, Kind, Base, Head string
	Unknown                bool
}

func (e *StaleBaseError) Error() string {
	what := fmt.Sprintf("its diff base %s is not in HEAD's history (HEAD is %s); the branch was rebased or reset since the base was recorded", e.Base, e.Head)
	if e.Unknown {
		what = fmt.Sprintf("its diff base %s is not a commit in this repository", e.Base)
	}
	return fmt.Sprintf("%s: %s, so its gates would judge the wrong change. Re-seat on the commit the item's own change starts from: satelle %s seat %s --base <ref> (its parent branch, e.g. main, or the branch it is stacked on), then present again",
		e.Item, what, e.Kind, e.Item)
}

func (k *Kernel) staleBase(it item.Item, sp span) *StaleBaseError {
	g, ok := sp.git.(BranchGit)
	if !ok || sp.head != "" || sp.base == "" {
		return nil
	}
	head, err := sp.git.Head()
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

type ItemDiff struct {
	Base, Head string
	Paths      []string
	Untracked  []string
	Frozen     bool
	git        Git
}

func (k *Kernel) ItemChange(id string) (ItemDiff, *StaleBaseError, error) {
	it, events, err := k.load(id)
	if err != nil {
		return ItemDiff{}, nil, err
	}
	w, err := k.workflowOf(it)
	if err != nil {
		return ItemDiff{}, nil, err
	}
	wt, err := k.worktreeOf(it, events)
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
			bg, ok := wt.git.(BranchGit)
			if !ok {
				return ItemDiff{}, nil, fmt.Errorf("%s: its frozen diff needs a git work tree", id)
			}
			paths, err := bg.ChangedBetween(m.Change.Base, m.Change.Head)
			if err != nil {
				return ItemDiff{}, nil, err
			}
			return ItemDiff{Base: m.Change.Base, Head: m.Change.Head, Paths: paths, Frozen: true, git: wt.git}, nil, nil
		}
		if recorded, _ := k.base(it, events); recorded == "" {
			return ItemDiff{}, nil, nil
		}
		return ItemDiff{}, nil, fmt.Errorf("%s reached %s before satelle recorded the change it reached it with; it has no frozen diff", id, it.Status)
	}
	sp := span{base: k.judgedBase(wt.git, it, events), git: wt.git}
	d := ItemDiff{Base: sp.base, git: wt.git}
	if d.Base == "" || wt.git == nil {
		return d, nil, nil
	}
	stale := k.staleBase(it, sp)
	if d.Paths, err = wt.git.ChangedPaths(d.Base); err != nil {
		return ItemDiff{}, nil, err
	}
	if d.Untracked, err = wt.git.Untracked(); err != nil {
		return ItemDiff{}, nil, err
	}
	return d, stale, nil
}

func (k *Kernel) ItemStat(d ItemDiff) ([]gitfacts.FileStat, error) {
	if d.git == nil || d.Base == "" {
		return nil, nil
	}
	return k.stat(span{base: d.Base, head: d.Head, git: d.git})
}

const defaultCollectBase = DefaultBaseBranch

func (k *Kernel) resolveBase(wt worktree, ref string) (string, error) {
	g, ok := wt.git.(BranchGit)
	if !ok {
		return "", errors.New("--base needs a git work tree")
	}
	commit, err := g.Resolve(ref)
	if err != nil {
		return "", fmt.Errorf("--base %s names no commit here (fetch it first)", ref)
	}
	return commit, nil
}

func (k *Kernel) seatBase(wt worktree, ref string) (string, error) {
	commit, err := k.resolveBase(wt, ref)
	if err != nil {
		return "", err
	}
	g := wt.git.(BranchGit)
	head, err := wt.git.Head()
	if err != nil {
		return "", err
	}
	if in, err := g.IsAncestor(commit, head); err != nil || !in {
		return "", fmt.Errorf("--base %s (%s) is not in HEAD's history; the item's gated transitions would refuse it", ref, commit)
	}
	return commit, nil
}
