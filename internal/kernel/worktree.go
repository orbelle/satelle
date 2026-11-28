package kernel

import (
	"errors"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

const (
	CheckoutNotWorkTree      = "not a git work tree"
	CheckoutNotTopLevel      = "not the top level"
	CheckoutUnreadableConfig = "unreadable config"
	CheckoutPartialClone     = "partial clone"
	CheckoutUnreadableFormat = "unreadable object format"
	CheckoutMoved            = "checkout moved"
	CheckoutNotRecorded      = "checkout not recorded"
	CheckoutGitTooOld        = "git too old"
	CheckoutGitUnreadable    = "unreadable git version"
)

type worktree struct {
	git         Git
	dir, target string
}

type TargetCheckoutError struct {
	Item, Target, State, Path, Top, Seated, Found string
}

func (e *TargetCheckoutError) Error() string {
	path := reporeg.PathLiteral(e.Path)
	lead := "item " + e.Item + " targets repo " + reporeg.Literal(e.Target)
	var what string
	switch e.State {
	case CheckoutGitTooOld:
		what = ", and git " + reporeg.PathLiteral(e.Found) + " is older than 2.44, which satelle needs to read a target checkout without fetching"
	case CheckoutGitUnreadable:
		what = ", and satelle cannot read the git version, so it cannot read a target checkout without fetching"
	case reporeg.CheckoutUnmapped:
		what = ", whose checkout is not mapped in .satelle/" + reporeg.LocalFile
	case reporeg.CheckoutMissing:
		what = ", whose checkout " + path + " does not exist"
	case reporeg.CheckoutMismatch:
		what = ", whose checkout " + path + " has another origin than .satelle/" + reporeg.TrackedFile + " registers"
	case reporeg.CheckoutUnreadable:
		what = ", whose checkout " + path + " has no readable origin"
	case reporeg.CheckoutInvalid:
		what = ", whose entry in .satelle/" + reporeg.TrackedFile + " is not valid"
	case reporeg.CheckoutUnsafe:
		what = ", whose checkout " + path + " is not a valid mapping: a checkout path may hold only the characters A-Z a-z 0-9 . _ / + @ -"
	case CheckoutNotWorkTree:
		what = ", whose checkout " + path + " is not a git work tree"
	case CheckoutNotTopLevel:
		top := reporeg.PathLiteral(e.Top)
		what = ", whose checkout " + path + " is inside the git work tree " + top + ", not its top level; map " + top + " in .satelle/" + reporeg.LocalFile
	case CheckoutUnreadableConfig:
		what = ", whose checkout " + path + " has git configuration satelle cannot read"
	case CheckoutPartialClone:
		what = ", whose checkout " + path + " is a partial clone; satelle reads only a complete checkout"
	case CheckoutUnreadableFormat:
		what = ", whose checkout " + path + " has an object format satelle cannot read (it reads sha1 and sha256)"
	case CheckoutMoved:
		what = ", whose checkout moved since the seat: the seat judged " + reporeg.PathLiteral(e.Seated) + ", and .satelle/" + reporeg.LocalFile + " now resolves to " + path + "; re-seat the item to judge " + path
	case CheckoutNotRecorded:
		what = ", whose seat did not record its checkout; re-seat the item so satelle records the checkout it judges"
	default:
		what = ", whose checkout " + path + " is " + e.State
	}
	return lead + what + "; satelle will not judge it against another repository (satelle doctor names the fix)"
}

func resolved(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func (k *Kernel) self() worktree {
	return worktree{git: k.Git, dir: k.Dir, target: reporeg.Self}
}

func (k *Kernel) worktreeNamed(name string) (worktree, error) {
	if name == "" || name == reporeg.Self {
		return k.self(), nil
	}
	refuse := func(state, path string) (worktree, error) {
		return worktree{}, &TargetCheckoutError{Target: name, State: state, Path: path}
	}
	c := k.Repos.CheckoutOf(name, k.targetEnv)
	if c.State != reporeg.CheckoutOK {
		return refuse(c.State, c.Path)
	}
	mapped, err := resolved(c.Path)
	if err != nil {
		return refuse(reporeg.CheckoutMissing, c.Path)
	}
	if !reporeg.ShellSafePath(mapped) {
		return refuse(reporeg.CheckoutUnsafe, mapped)
	}
	probe := gitfacts.Repo{Dir: mapped, Env: k.targetEnv, Untrusted: true}
	v, err := probe.Version()
	if err != nil {
		return refuse(CheckoutGitUnreadable, mapped)
	}
	if !v.AtLeast(2, 44) {
		return worktree{}, &TargetCheckoutError{Target: name, State: CheckoutGitTooOld, Path: mapped, Found: v.Raw}
	}
	top, err := probe.TopLevel()
	if err == nil && top != "" {
		top, err = resolved(top)
	}
	if err != nil || top == "" {
		return refuse(CheckoutNotWorkTree, mapped)
	}
	if !reporeg.ShellSafePath(top) {
		return refuse(reporeg.CheckoutUnsafe, top)
	}
	if top != mapped {
		return worktree{}, &TargetCheckoutError{Target: name, State: CheckoutNotTopLevel, Path: mapped, Top: top}
	}
	at := gitfacts.Repo{Dir: top, Env: k.targetEnv, Untrusted: true}
	pc, err := at.PartialClone()
	if err != nil {
		return refuse(CheckoutUnreadableConfig, top)
	}
	if pc {
		return refuse(CheckoutPartialClone, top)
	}
	f, err := at.ObjectFormat()
	if err != nil {
		return refuse(CheckoutUnreadableFormat, top)
	}
	tree, err := gitfacts.EmptyTree(f)
	if err != nil {
		return refuse(CheckoutUnreadableFormat, top)
	}
	at.AttrSource = tree
	return worktree{git: at, dir: top, target: name}, nil
}

func (k *Kernel) worktreeFor(it item.Item) (worktree, error) {
	wt, err := k.worktreeNamed(it.Target())
	var tce *TargetCheckoutError
	if errors.As(err, &tce) {
		tce.Item = it.ID
	}
	return wt, err
}

func (k *Kernel) worktreeOf(it item.Item, events []ledger.Event) (worktree, error) {
	if err := k.CheckTarget(it); err != nil {
		return worktree{}, err
	}
	wt, err := k.worktreeFor(it)
	if err != nil || wt.target == reporeg.Self {
		return wt, err
	}
	rec, seated := item.SeatedCheckout(events)
	switch {
	case seated && rec == "":
		return worktree{}, &TargetCheckoutError{Item: it.ID, Target: wt.target, State: CheckoutNotRecorded, Path: wt.dir}
	case rec != "" && rec != wt.dir:
		return worktree{}, &TargetCheckoutError{Item: it.ID, Target: wt.target, State: CheckoutMoved, Path: wt.dir, Seated: rec}
	}
	return wt, nil
}
