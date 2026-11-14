package gate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// Head is the pushed commit a remote check judges, and the remote-tracking
// ref it is pushed on (the commit is in that ref's history).
type Head struct {
	Commit string
	Ref    string
}

// RemoteCheck is one check skill to run on a node of Remote, against Head,
// with the environment a local check sees, bounded by Timeout as a whole
// (the node's start, setup, fetch and the check itself).
type RemoteCheck struct {
	Remote, Gate, Script string
	Env                  CheckEnv
	Head                 Head
	Timeout              time.Duration
}

// RemoteOutcome is what a node returned: the node it ran on, the check's exit
// code and its output. On an error, Output says what is known of the run.
type RemoteOutcome struct {
	Node   string
	Exit   int
	Output string
}

// RemoteChecker runs a check gate on a node of a remote. An error is a run
// that gave no result (the node did not start, the head did not check out,
// the bound cut it, the node returned no exit): never a verdict. A cut run's
// error is a *TimeoutError.
type RemoteChecker interface {
	RunRemote(ctx context.Context, c RemoteCheck) (RemoteOutcome, error)
}

// remote runs the gate's check skill on a node of the remote its placement
// names. Exit 0 accepts and any other exit rejects, as a verdict; a run with
// no result rejects naming its cause, and is not a verdict.
func (r Run) remote(ctx context.Context, name string, g workflow.Gate, skill workflow.Skill, limit time.Duration, source string) Result {
	id := strings.TrimPrefix(g.Placement, agent.PlaceRemote)
	prefix := "remote " + id + ": "
	switch {
	case r.Remote == nil:
		return Result{Result: Reject, Notes: fmt.Sprintf("gate %s is placed on remote %s, and no remote runner is configured here", name, id)}
	case r.HeadErr != nil:
		return Result{Result: Reject, Notes: prefix + r.HeadErr.Error()}
	case r.Head.Commit == "" || r.Head.Ref == "":
		return Result{Result: Reject, Notes: prefix + "no pushed head to check"}
	}
	out, err := r.Remote.RunRemote(ctx, RemoteCheck{Remote: id, Gate: name, Script: skill.Script, Env: r.env(), Head: r.Head, Timeout: limit})
	loc := ""
	if out.Node != "" {
		loc = agent.PlaceRemote + out.Node
	}
	if err != nil {
		return Result{Result: Reject, Notes: prefix + checkFailure(err, source, bound([]byte(out.Output))), Location: loc}
	}
	res := Result{Result: Reject, Location: loc, verdict: true}
	res.Notes = fmt.Sprintf("%snode %s at %s: exit %d\n%s", prefix, out.Node, ledger.ShortHash(r.Head.Commit), out.Exit, bound([]byte(out.Output)))
	if out.Exit == 0 {
		res.Result = Accept
	}
	return res
}
