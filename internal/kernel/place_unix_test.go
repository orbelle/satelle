//go:build !windows

package kernel

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/item"
)

// lineKernel is a three-state line: a → b (performed by doer) → c, with a
// review gate on b → c over Go files. dir is the working tree ("" for none).
func lineKernel(t *testing.T, placement, dir string) (*Kernel, *fakeDispatcher) {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	root := filepath.Join(dir, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"line\"\n")
	write(t, filepath.Join(root, "agents.toml"), `
[agents.doer]
command = "claude"
tools = ["Read", "Edit"]
placement = "`+placement+`"
[agents.r1]
command = "claude"
tools = ["Read"]
`)
	write(t, filepath.Join(root, "skills", "do.md"), "Do the work.\n")
	write(t, filepath.Join(root, "skills", "review.md"), "Review.\n")
	write(t, filepath.Join(root, "workflows", "line.toml"), `
[states.a]
start = true
[states.b]
performer = { agent = "doer", skill = "do" }
[states.held]
park = true
[states.c]
terminal = true
[gates.review]
skill = "review"
agent = "r1"
paths = ["**/*.go"]
[[transition]]
from = ["a"]
to = "b"
[[transition]]
from = ["*"]
to = "held"
[[transition]]
from = ["b"]
to = "c"
groups = [{ gates = ["review"] }]
`)
	k, err := Open(Options{Root: root, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"r1": accept, "doer": "did it"}}
	k.Dispatcher = d
	if k.Git == nil {
		k.Git = fakeGit{changed: []string{"src/a.go"}}
	}
	return k, d
}

func newItem(t *testing.T, k *Kernel) item.Item {
	t.Helper()
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func kinds(t *testing.T, k *Kernel, id string) []string {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestDriveWalksToTheTerminalState(t *testing.T) {
	k, d := lineKernel(t, "", "")
	it := newItem(t, k)
	res, err := k.Drive(context.Background(), it.ID, "node")
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictAccept || res.Item.Status != "c" || len(res.Steps) != 2 || !res.Steps[1].Performed {
		t.Fatalf("drive = %+v", res)
	}
	if strings.Join(d.calls, " ") != "perform:doer judge:r1" {
		t.Fatalf("calls = %v", d.calls)
	}
}

func TestDriveStopsAtARejectedTransition(t *testing.T) {
	k, d := lineKernel(t, "", "")
	d.set("r1", reject)
	it := newItem(t, k)
	res, err := k.Drive(context.Background(), it.ID, "node")
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictReject || res.Item.Status != "b" || !strings.Contains(res.Notes, "review: no") {
		t.Fatalf("drive = %+v", res)
	}
}

func TestDriveStopsWhenThePerformerFails(t *testing.T) {
	k, d := lineKernel(t, "", "")
	delete(d.answers, "doer")
	it := newItem(t, k)
	res, err := k.Drive(context.Background(), it.ID, "node")
	if err != nil || res.Verdict != VerdictReject || res.Item.Status != "b" || !strings.Contains(res.Notes, "performing b failed") {
		t.Fatalf("drive = %+v, %v", res, err)
	}
}

type fakeRemote struct {
	started []string
	reqs    []agent.Request
}

func (f *fakeRemote) StartNode(_ context.Context, remote, itemID, binding string) (string, error) {
	f.started = append(f.started, remote+"|"+itemID+"|"+binding)
	return "nod_7", nil
}

func (f *fakeRemote) PerformOn(_ context.Context, node string, req agent.Request) (agent.Result, error) {
	f.reqs = append(f.reqs, req)
	return agent.Result{Text: "done on " + node}, nil
}

func TestPerformPlacedOnARemoteRunsOnItsNode(t *testing.T) {
	k, d := lineKernel(t, "remote:ci", "")
	it := newItem(t, k)
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Perform(context.Background(), it.ID, "ada"); err == nil || !strings.Contains(err.Error(), "no remote performer") {
		t.Fatalf("perform without a remote performer = %v", err)
	}
	r := &fakeRemote{}
	k.Remote = r
	res, err := k.Perform(context.Background(), it.ID, "ada")
	if err != nil || res.Text != "done on nod_7" {
		t.Fatalf("perform = %+v, %v", res, err)
	}
	if len(d.calls) != 0 || r.started[0] != "ci|"+it.ID+"|doer" || r.reqs[0].System != "Do the work.\n" {
		t.Fatalf("local calls %v, started %v, reqs %+v", d.calls, r.started, r.reqs)
	}
	got := strings.Join(kinds(t, k, it.ID), " ")
	if !strings.Contains(got, item.KindSeatBound+" "+item.KindSeatReleased+" "+KindPerformerRun) {
		t.Fatalf("events = %s", got)
	}
	events, _ := k.Ledger.Read(it.ID)
	var seat item.Seat
	var run PerformerRun
	for _, e := range events {
		switch e.Kind {
		case item.KindSeatBound:
			_ = e.Decode(&seat)
		case KindPerformerRun:
			_ = e.Decode(&run)
		}
	}
	if seat.Location != "remote:nod_7" || run.Location != "remote:nod_7" {
		t.Fatalf("seat %+v, run %+v", seat, run)
	}
}

type fakeCloud struct{}

func (fakeCloud) Launch(context.Context, item.Item, agent.Request) (string, error) {
	return "https://claude.ai/code/session_x", nil
}

func TestPerformPlacedInTheCloud(t *testing.T) {
	k, _ := lineKernel(t, "cloud", "")
	it := newItem(t, k)
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Perform(context.Background(), it.ID, "ada"); !errors.Is(err, ErrCloudByHand) || !strings.Contains(err.Error(), "collect "+it.ID) {
		t.Fatalf("perform with no launcher = %v", err)
	}
	k.Cloud = fakeCloud{}
	if _, err := k.Perform(context.Background(), it.ID, "ada"); err != nil {
		t.Fatal(err)
	}
	seats, err := k.Cache.Seats("", it.ID)
	if err != nil || len(seats) != 1 || seats[0].Location != "cloud:https://claude.ai/code/session_x" {
		t.Fatalf("seats = %+v, %v", seats, err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=a@b", "-c", "user.name=a", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A cloud step's branch is collected read-only: its commit and merge base are
// recorded, the work tree and refs are untouched, and the gates judge the
// branch's diff.
func TestCollectRecordsTheBranchAndTheGatesJudgeIt(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "README"), "r\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	k, d := lineKernel(t, "cloud", dir)
	if _, ok := k.Git.(gitfacts.Repo); !ok {
		t.Fatalf("git = %T", k.Git)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "config")
	it := newItem(t, k)
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	base := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "-b", "cloud/work")
	write(t, filepath.Join(dir, "x.go"), "package x\n")
	git(t, dir, "add", "x.go")
	git(t, dir, "commit", "-q", "-m", "work")
	tip := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "main")
	refsBefore := git(t, dir, "for-each-ref")

	c, err := k.Collect(it.ID, "cloud/work", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if c.Commit != tip || c.Base != base || c.Branch != "cloud/work" {
		t.Fatalf("collected = %+v, want commit %s base %s", c, tip, base)
	}
	if git(t, dir, "for-each-ref") != refsBefore || git(t, dir, "rev-parse", "HEAD") != base {
		t.Fatal("collect changed the repository")
	}
	if _, err := k.Collect(it.ID, "no-such-branch", "ada"); err == nil {
		t.Fatal("collecting a missing branch succeeded")
	}
	res, err := k.Transition(context.Background(), it.ID, "c", "ada")
	if err != nil || !res.Moved || len(res.Gates) != 1 || res.Gates[0].Result != "accept" {
		t.Fatalf("transition = %+v, %v", res, err)
	}
	if strings.Join(d.calls, " ") != "judge:r1" {
		t.Fatalf("the review gate did not judge the branch: %v", d.calls)
	}
	var found bool
	events, _ := k.Ledger.Read(it.ID)
	for _, e := range events {
		found = found || e.Kind == KindBranchCollected
	}
	if !found {
		t.Fatal("no branch.collected event")
	}
}
