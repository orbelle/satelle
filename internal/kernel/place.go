package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// RemotePerformer performs a step on a node of a remote (placement
// "remote:<id>"). The real one starts the node through satelle-server and
// sends the step over the sealed channel.
type RemotePerformer interface {
	// StartNode asks for a node of remote to perform binding for item, and
	// returns its id.
	StartNode(ctx context.Context, remote, itemID, binding string) (node string, err error)
	// PerformOn runs req on node and returns the performer's answer.
	PerformOn(ctx context.Context, node string, req agent.Request) (agent.Result, error)
}

// CloudLauncher starts a cloud session (placement "cloud") performing a step,
// and returns the session's URL. Its work comes back as a branch, recorded by
// Collect and judged by the local gates.
type CloudLauncher interface {
	Launch(ctx context.Context, it item.Item, req agent.Request) (url string, err error)
}

// Seat location prefixes.
const (
	LocationLocal  = "local:"
	LocationRemote = "remote:"
	LocationCloud  = "cloud:"
)

// KindBranchCollected records a branch a step's work came back on.
const KindBranchCollected = "branch.collected"

// Collected is the branch.collected payload: the branch, the commit it was at
// and the diff base its gates judge from.
type Collected struct {
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	Base   string `json:"base"`
}

// BranchGit is what Collect, seat --base, task run, the stale-base check and a
// collected branch's gates ask of git, all read-only.
type BranchGit interface {
	// Resolve returns the commit ref names; an error when there is none.
	Resolve(ref string) (string, error)
	MergeBase(a, b string) (string, error)
	// IsAncestor reports whether a is in b's history.
	IsAncestor(a, b string) (bool, error)
	// Branch is the checkout's current branch, "" when HEAD is detached.
	Branch() (string, error)
	ChangedBetween(base, head string) ([]string, error)
	DiffBetween(base, head string, paths []string) (string, error)
}

// ErrCloudByHand is returned by Perform for a cloud-placed step when no
// launcher is configured: the operator starts the session.
var ErrCloudByHand = errors.New("no cloud launcher")

// performRemote runs a step on a node: the node holds a remote:<node> seat for
// the run.
func (k *Kernel) performRemote(ctx context.Context, it item.Item, remote string, req agent.Request, actor string) (agent.Result, string, error) {
	if k.Remote == nil {
		return agent.Result{}, "", fmt.Errorf("agent %q is placed on remote %q, and no remote performer is configured (sign in and bind a project)", req.Binding.Name, remote)
	}
	node, err := k.Remote.StartNode(ctx, remote, it.ID, req.Binding.Name)
	if err != nil {
		return agent.Result{}, "", fmt.Errorf("start a node of remote %s: %w", remote, err)
	}
	loc := LocationRemote + node
	if _, err := k.Seat(it.ID, node, loc, "", actor); err != nil {
		return agent.Result{}, loc, err
	}
	res, runErr := k.Remote.PerformOn(ctx, node, req)
	if err := k.Release(it.ID, node, actor); err != nil && runErr == nil {
		runErr = err
	}
	return res, loc, runErr
}

// performCloud starts a cloud session on the step and seats it at cloud:<url>.
func (k *Kernel) performCloud(ctx context.Context, it item.Item, req agent.Request, actor string) (agent.Result, string, error) {
	if k.Cloud == nil {
		return agent.Result{}, "", fmt.Errorf("%w: agent %q is placed in the cloud. Start a cloud session on %s, seat it with `satelle %s seat %s --session <s> --location %s<url>`, and when its work comes back on a branch run `satelle %s collect %s --branch <b>`",
			ErrCloudByHand, req.Binding.Name, it.ID, it.Kind, it.ID, LocationCloud, it.Kind, it.ID)
	}
	url, err := k.Cloud.Launch(ctx, it, req)
	if err != nil {
		return agent.Result{}, "", err
	}
	loc := LocationCloud + url
	if _, err := k.Seat(it.ID, url, loc, "", actor); err != nil {
		return agent.Result{}, loc, err
	}
	return agent.Result{Text: "performing in the cloud at " + url}, loc, nil
}

// Collect records the branch a step's work came back on: the commit it is at
// and its merge base with HEAD, which becomes the diff base the item's gates
// judge from. It reads git and never writes it: bringing the branch in, if
// that is needed, is the work of a step's agent.
func (k *Kernel) Collect(id, branch, actor string) (Collected, error) {
	if _, err := k.Get(id); err != nil {
		return Collected{}, err
	}
	bg, ok := k.Git.(BranchGit)
	if !ok {
		return Collected{}, errors.New("collect needs a git work tree")
	}
	if strings.TrimSpace(branch) == "" || strings.HasPrefix(branch, "-") {
		return Collected{}, fmt.Errorf("%q is not a branch", branch)
	}
	commit, err := bg.Resolve(branch)
	if err != nil {
		return Collected{}, fmt.Errorf("branch %s is not here (fetch it first): %w", branch, err)
	}
	head, err := k.Git.Head()
	if err != nil {
		return Collected{}, err
	}
	base, err := bg.MergeBase(head, commit)
	if err != nil {
		return Collected{}, err
	}
	c := Collected{Branch: branch, Commit: commit, Base: base}
	_, err = k.append(id, KindBranchCollected, actor, c)
	return c, err
}

// collected is the latest branch recorded on the item, if any.
func collected(events []ledger.Event) (Collected, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		var c Collected
		if events[i].Kind == KindBranchCollected && events[i].Decode(&c) == nil && c.Commit != "" {
			return c, true
		}
	}
	return Collected{}, false
}

// span is what an item's gates judge: from base to head, or to the working
// tree when head is "".
type span struct{ base, head string }

func (k *Kernel) spanOf(it item.Item, events []ledger.Event) span {
	if c, ok := collected(events); ok {
		if _, branchy := k.Git.(BranchGit); branchy {
			return span{base: c.Base, head: c.Commit}
		}
	}
	return span{base: k.base(it, events)}
}

func (k *Kernel) changed(s span) ([]string, error) {
	if s.head != "" {
		return k.Git.(BranchGit).ChangedBetween(s.base, s.head)
	}
	return k.Git.ChangedPaths(s.base)
}

func (k *Kernel) diffSpan(s span, paths []string) (string, error) {
	if k.Git == nil || s.base == "" {
		return "", nil
	}
	if s.head != "" {
		return k.Git.(BranchGit).DiffBetween(s.base, s.head, paths)
	}
	return k.Git.Diff(s.base, paths)
}
