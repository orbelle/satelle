//go:build !windows

package gate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/workflow"
)

// fakeRemote answers every RunRemote with a canned outcome and records the
// checks it was given.
type fakeRemote struct {
	mu     sync.Mutex
	out    RemoteOutcome
	err    error
	checks []RemoteCheck
}

func (f *fakeRemote) RunRemote(_ context.Context, c RemoteCheck) (RemoteOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks = append(f.checks, c)
	return f.out, f.err
}

const headSHA = "0123456789abcdef0123456789abcdef01234567"

// remoteSetup is a run whose gate "suite" is a check placed on remote box. The
// check script would fail if it ran here, so a local run is visible.
func remoteSetup(t *testing.T, g workflow.Gate) (Run, *fakeRemote) {
	t.Helper()
	g.Skill = "suite"
	r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"suite"}}}, map[string]workflow.Gate{"suite": g})
	r.Skills["suite"] = checkSkill("echo ran here; exit 7")
	r.Dir = t.TempDir()
	f := &fakeRemote{}
	r.Remote = f
	r.Head = Head{Commit: headSHA, Ref: "refs/remotes/origin/b"}
	return r, f
}

// A check placed on a remote calls the runner, never runs here, and its exit
// is the verdict: the notes name the remote, node, head and exit, and the
// result carries the node's location.
func TestRemoteCheckRunsOnTheNode(t *testing.T) {
	for _, c := range []struct {
		exit int
		want string
	}{{0, Accept}, {3, Reject}} {
		r, f := remoteSetup(t, workflow.Gate{Placement: "remote:box", Timeout: "5m"})
		f.out = RemoteOutcome{Node: "nod_1", Exit: c.exit, Output: "suite output"}
		rs, pass := r.Execute(context.Background())
		if len(rs) != 1 || rs[0].Result != c.want || pass != (c.want == Accept) {
			t.Fatalf("exit %d: %v %+v", c.exit, pass, rs)
		}
		want := "remote box: node nod_1 at 0123456789ab: exit " + string(rune('0'+c.exit)) + "\nsuite output"
		if rs[0].Notes != want || rs[0].Location != "remote:nod_1" || strings.Contains(rs[0].Notes, "ran here") {
			t.Fatalf("exit %d: notes %q location %q", c.exit, rs[0].Notes, rs[0].Location)
		}
		if len(f.checks) != 1 {
			t.Fatalf("runner called %d times", len(f.checks))
		}
		got := f.checks[0]
		if got.Remote != "box" || got.Gate != "suite" || got.Script != "echo ran here; exit 7" || got.Head != r.Head ||
			got.Timeout != 5*time.Minute || got.Env.Item != "i1" || got.Env.From != "s0" || got.Env.To != "s1" {
			t.Fatalf("check handed %+v", got)
		}
	}
}

// A local placement still runs here, and the runner is not called.
func TestLocalPlacementRunsHere(t *testing.T) {
	r, f := remoteSetup(t, workflow.Gate{Placement: "local"})
	rs, _ := r.Execute(context.Background())
	if rs[0].Result != Reject || rs[0].Notes != "ran here" || rs[0].Location != "" || len(f.checks) != 0 {
		t.Fatalf("%+v %d", rs[0], len(f.checks))
	}
}

// A carried remote gate starts no node; after runs here on the node's verdict.
func TestRemoteCarryAndAfter(t *testing.T) {
	r, f := remoteSetup(t, workflow.Gate{Placement: "remote:box", Carry: true})
	r.Prior = []Result{{Transition: "s0->s1", Gate: "suite", Result: Accept, Coverage: "cov:"}}
	rs, _ := r.Execute(context.Background())
	if rs[0].Result != Carried || len(f.checks) != 0 {
		t.Fatalf("carried: %+v %d", rs[0], len(f.checks))
	}

	r, f = remoteSetup(t, workflow.Gate{Placement: "remote:box", After: "publish"})
	r.Skills["publish"] = checkSkill(`echo "published $SATELLE_GATE_RESULT from $PWD"`)
	f.out = RemoteOutcome{Node: "nod_2", Exit: 0}
	rs, _ = r.Execute(context.Background())
	if rs[0].Result != Accept || !strings.Contains(rs[0].Notes, "after publish: published accept from "+r.Dir) {
		t.Fatalf("after: %+v", rs[0])
	}
}

// No runner, no pushed head, a runner error and a timeout each reject with the
// cause named, and none is a verdict (after does not run).
func TestRemoteCheckFailuresReject(t *testing.T) {
	check := func(name string, r Run, wants ...string) {
		t.Helper()
		r.Workflow.Gates["suite"] = func(g workflow.Gate) workflow.Gate { g.After = "publish"; return g }(r.Workflow.Gates["suite"])
		r.Skills["publish"] = checkSkill("echo published")
		rs, pass := r.Execute(context.Background())
		if pass || rs[0].Result != Reject || rs[0].verdict || strings.Contains(rs[0].Notes, "published") {
			t.Fatalf("%s: %v %+v", name, pass, rs[0])
		}
		for _, w := range wants {
			if !strings.Contains(rs[0].Notes, w) {
				t.Fatalf("%s: notes %q lack %q", name, rs[0].Notes, w)
			}
		}
	}
	r, _ := remoteSetup(t, workflow.Gate{Placement: "remote:box"})
	r.Remote = nil
	check("no runner", r, "gate suite is placed on remote box, and no remote runner is configured here")

	r, f := remoteSetup(t, workflow.Gate{Placement: "remote:box"})
	r.Head, r.HeadErr = Head{}, errors.New("head abc is not pushed to origin/b: push it first")
	check("unpushed", r, "remote box: head abc is not pushed to origin/b: push it first")
	if len(f.checks) != 0 {
		t.Fatal("an unpushed head started a node")
	}

	r, f = remoteSetup(t, workflow.Gate{Placement: "remote:box"})
	f.out, f.err = RemoteOutcome{Node: "nod_3", Output: "the container is left"}, errors.New("node failed to start: docker run: boom")
	check("start", r, "remote box: node failed to start: docker run: boom", "the container is left")

	r, f = remoteSetup(t, workflow.Gate{Placement: "remote:box", Timeout: "2s"})
	f.out, f.err = RemoteOutcome{Node: "nod_4", Output: "cut while running the check on node nod_4"}, &TimeoutError{Bound: 2 * time.Second}
	check("timeout", r, "remote box: timed out after 2s (gates.suite timeout)", "cut while running the check")
}
