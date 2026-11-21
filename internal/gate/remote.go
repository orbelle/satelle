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

type Head struct {
	Commit string
	Ref    string
}

type RemoteCheck struct {
	Remote, Gate, Script string
	Env                  CheckEnv
	Head                 Head
	Timeout              time.Duration
}

type RemoteOutcome struct {
	Node   string
	Exit   int
	Output string
}

type RemoteChecker interface {
	RunRemote(ctx context.Context, c RemoteCheck) (RemoteOutcome, error)
}

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
	out, err := r.Remote.RunRemote(ctx, RemoteCheck{Remote: id, Gate: name, Script: skill.Script, Env: r.envFor(skill), Head: r.Head, Timeout: limit})
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
