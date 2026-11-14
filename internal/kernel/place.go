package kernel

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
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

// Seat location prefixes (owned by package item).
const (
	LocationLocal  = item.LocationLocal
	LocationRemote = item.LocationRemote
	LocationCloud  = item.LocationCloud
)

// KindBranchCollected records a branch a step's work came back on.
const KindBranchCollected = "branch.collected"

// Collected is the branch.collected payload: the branch, the commit it was at,
// the diff base its gates judge from (its merge base with the base branch) and
// the ref that base was taken from.
type Collected struct {
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Base    string `json:"base"`
	BaseRef string `json:"base_ref,omitempty"`
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

// LocalLocation is the seat location of a working tree: local: and the tree's
// canonical path, so locations compare equal however the tree was reached.
func LocalLocation(tree string) string {
	if abs, err := filepath.Abs(tree); err == nil {
		tree = abs
	}
	if r, err := filepath.EvalSymlinks(tree); err == nil {
		tree = r
	}
	return LocationLocal + filepath.Clean(tree)
}

// performLocal runs a step here: the run is seated at local:<tree> under a
// session of its own, handed to the performer as SATELLE_SESSION with the tree
// as SATELLE_PROJECT_DIR, so its harness's edit gate admits its edits. The
// seat carries the item's recorded base, so it never moves what the gates
// judge, and it is released when the run ends, also on failure or panic. The
// run's process owns the session's record (k.Sessions) until then.
func (k *Kernel) performLocal(ctx context.Context, it item.Item, events []ledger.Event, req agent.Request, actor string) (res agent.Result, err error) {
	session := item.NewID("perform")
	base, ref := k.base(it, events)
	if _, err := k.bindSeat(it.ID, item.Seat{Session: session, Location: LocalLocation(k.Dir), Base: base, BaseRef: ref}, actor); err != nil {
		return agent.Result{}, err
	}
	// The run's process owns its session while it runs, so a seat left by a
	// run that died is provably gone (internal/seatlife). Best effort: a
	// session without a record is only ever unverified.
	if k.Sessions != nil {
		_ = k.Sessions.Own(session)
	}
	defer func() {
		rerr := k.Release(it.ID, session, actor)
		if err == nil {
			err = rerr
		} else if rerr != nil {
			err = errors.Join(err, rerr)
		}
		if k.Sessions != nil {
			_ = k.Sessions.Forget(session)
		}
	}()
	req.Env = append(append([]string{}, req.Env...), config.SessionEnv+"="+session, config.ProjectDirEnv+"="+k.Dir)
	return k.Dispatcher.Dispatch(ctx, agent.Perform, req)
}

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
// and its merge base with the base branch, which becomes the diff base the
// item's gates judge from. The base branch is baseRef when given, else
// origin/main, else the item's recorded base; never HEAD, so the base is the
// same whichever tree collect runs in. A branch that adds nothing over that
// base is refused: its gates would judge an empty diff. It reads git and never
// writes it: bringing the branch in, if that is needed, is the work of a step's
// agent.
func (k *Kernel) Collect(id, branch, baseRef, actor string) (Collected, error) {
	it, events, err := k.load(id)
	if err != nil {
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
	ref, tip := baseRef, ""
	switch {
	case baseRef != "":
		if tip, err = k.resolveBase(baseRef); err != nil {
			return Collected{}, err
		}
	default:
		if c, rerr := bg.Resolve(defaultCollectBase); rerr == nil {
			ref, tip = defaultCollectBase, c
		} else if recorded, _ := k.base(it, events); recorded != "" {
			ref, tip = "recorded base", recorded
		} else {
			return Collected{}, fmt.Errorf("collect %s: no base: %s is not here and the item records none; pass --base <ref>", id, defaultCollectBase)
		}
	}
	base, err := bg.MergeBase(tip, commit)
	if err != nil {
		return Collected{}, fmt.Errorf("branch %s shares no history with %s (%s): %w", branch, ref, tip, err)
	}
	paths, err := bg.ChangedBetween(base, commit)
	if err != nil {
		return Collected{}, err
	}
	if len(paths) == 0 {
		return Collected{}, fmt.Errorf("branch %s (%s) adds nothing over %s (base %s): its gates would judge an empty diff. Collect a branch with the item's work, or pass --base <ref> for the branch it is stacked on",
			branch, ledger.ShortHash(commit), ref, ledger.ShortHash(base))
	}
	c := Collected{Branch: branch, Commit: commit, Base: base, BaseRef: ref}
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
// tree when head is "", whose base is the judged base (judgedBase).
type span struct{ base, head string }

func (k *Kernel) spanOf(it item.Item, events []ledger.Event) span {
	if c, ok := collected(events); ok {
		if _, branchy := k.Git.(BranchGit); branchy {
			return span{base: c.Base, head: c.Commit}
		}
	}
	return span{base: k.judgedBase(it, events)}
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

// remotePlaced reports whether any gate of tr is placed on a remote.
func remotePlaced(w *workflow.Workflow, tr *workflow.Transition) bool {
	for _, g := range tr.Groups {
		for _, name := range g.Gates {
			if strings.HasPrefix(w.Gates[name].Placement, agent.PlaceRemote) {
				return true
			}
		}
	}
	return false
}

// pushedHead is the head a remote-placed check judges: the collected branch's
// commit when the item has one, else the checkout's HEAD on its branch (a
// dirty tree or a detached HEAD is refused: what a node would test is not
// what is here). The head must be in the history of the branch's
// remote-tracking ref on the base branch's remote (origin), which is what the
// node is handed.
func (k *Kernel) pushedHead(events []ledger.Event, sp span) (gate.Head, error) {
	bg, ok := k.Git.(BranchGit)
	if !ok {
		return gate.Head{}, errors.New("no git work tree here: a remote check needs a pushed head")
	}
	var commit, branch string
	if c, ok := collected(events); ok && sp.head != "" {
		commit, branch = sp.head, c.Branch
	} else {
		if d, ok := k.Git.(interface{ Dirty() (bool, error) }); ok {
			dirty, err := d.Dirty()
			if err != nil {
				return gate.Head{}, err
			}
			if dirty {
				return gate.Head{}, errors.New("the working tree has uncommitted changes, which the node would not test: commit and push them")
			}
		}
		var err error
		if commit, err = k.Git.Head(); err != nil {
			return gate.Head{}, err
		}
		if branch, err = bg.Branch(); err != nil {
			return gate.Head{}, err
		}
		if branch == "" {
			return gate.Head{}, fmt.Errorf("HEAD (%s) is detached: a remote check tests a pushed branch", ledger.ShortHash(commit))
		}
	}
	remote, _, _ := strings.Cut(DefaultBaseBranch, "/")
	ref := "refs/remotes/" + remote + "/" + branch
	notPushed := fmt.Errorf("head %s is not pushed to %s/%s: push it first", ledger.ShortHash(commit), remote, branch)
	tip, err := bg.Resolve(ref)
	if err != nil {
		return gate.Head{}, notPushed
	}
	in, err := bg.IsAncestor(commit, tip)
	if err != nil {
		return gate.Head{}, err
	}
	if !in {
		return gate.Head{}, notPushed
	}
	return gate.Head{Commit: commit, Ref: ref}, nil
}
