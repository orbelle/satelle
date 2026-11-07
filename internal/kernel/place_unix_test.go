//go:build !windows

package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/gittest"
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

type fakeCloud struct{ got *agent.Request }

func (c fakeCloud) Launch(_ context.Context, _ item.Item, req agent.Request) (string, error) {
	if c.got != nil {
		*c.got = req
	}
	return "https://claude.ai/code/session_x", nil
}

// A detached (cloud) run cannot read this machine: its prompt carries the
// record inline, bounded, and names no local path; no folder is written.
func TestPerformPlacedInTheCloudCarriesTheRecordInline(t *testing.T) {
	k, _ := lineKernel(t, "cloud", "")
	it := newItem(t, k)
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("abcdefghij", 2048) // 20480 bytes
	if _, err := k.Attach(it.ID, "plan", []byte(body), "text/markdown", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a child"), Parent: &it.ID}, "ada"); err != nil {
		t.Fatal(err)
	}
	var got agent.Request
	k.Cloud = fakeCloud{got: &got}
	if _, err := k.Perform(context.Background(), it.ID, "ada"); err != nil {
		t.Fatal(err)
	}
	p := got.Prompt
	for _, want := range []string{"The item's record (attachments and children), inline:", body[:InlineBodyMax], "[shown 16384 of 20480 bytes]", "a child"} {
		if !strings.Contains(p, want) {
			t.Errorf("the detached prompt lacks %.60q", want)
		}
	}
	for _, bad := range []string{RecordLead, ".satelle/runs", k.Root, k.Dir} {
		if strings.Contains(p, bad) {
			t.Errorf("the detached prompt names a local path: %q", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(k.Root, runsDir)); !os.IsNotExist(err) {
		t.Errorf("a detached run wrote a record folder: %v", err)
	}
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

// A cloud step's branch is collected read-only: its commit and merge base are
// recorded, the work tree and refs are untouched, and the gates judge the
// branch's diff.
func TestCollectRecordsTheBranchAndTheGatesJudgeIt(t *testing.T) {
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "README"), "r\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "init")
	k, d := lineKernel(t, "cloud", dir)
	if _, ok := k.Git.(gitfacts.Repo); !ok {
		t.Fatalf("git = %T", k.Git)
	}
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "config")
	it := newItem(t, k)
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	base := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "checkout", "-q", "-b", "cloud/work")
	write(t, filepath.Join(dir, "x.go"), "package x\n")
	gittest.Git(t, dir, "add", "x.go")
	gittest.Git(t, dir, "commit", "-q", "-m", "work")
	tip := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "checkout", "-q", "main")
	refsBefore := gittest.Git(t, dir, "for-each-ref")

	c, err := k.Collect(it.ID, "cloud/work", "", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if c.Commit != tip || c.Base != base || c.Branch != "cloud/work" || c.BaseRef != "recorded base" {
		t.Fatalf("collected = %+v, want commit %s base %s", c, tip, base)
	}
	if gittest.Git(t, dir, "for-each-ref") != refsBefore || gittest.Git(t, dir, "rev-parse", "HEAD") != base {
		t.Fatal("collect changed the repository")
	}
	if _, err := k.Collect(it.ID, "no-such-branch", "", "ada"); err == nil {
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

// collectFixture is a repository whose origin/main moved on after the story's
// branch sto/x forked, and a worktree on sto/x that merged origin/main (a
// send-back), so the worktree's HEAD is the branch tip. It returns the kernel,
// the item, the main checkout, the worktree, the branch tip and origin/main.
func collectFixture(t *testing.T) (k *Kernel, it item.Item, dir, wt, tip, originMain string) {
	t.Helper()
	dir = t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "README"), "r\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "init")
	k, _ = lineKernel(t, "cloud", dir)
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "config")
	it = newItem(t, k)
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	fork := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", fork)
	gittest.Git(t, dir, "checkout", "-q", "-b", "sto/x")
	write(t, filepath.Join(dir, "x.go"), "package x\n")
	gittest.Git(t, dir, "add", "x.go")
	gittest.Git(t, dir, "commit", "-q", "-m", "work")
	gittest.Git(t, dir, "checkout", "-q", "main")
	write(t, filepath.Join(dir, "y.go"), "package y\n")
	gittest.Git(t, dir, "add", "y.go")
	gittest.Git(t, dir, "commit", "-q", "-m", "main moves on")
	originMain = gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", originMain)
	wt = filepath.Join(t.TempDir(), "wt")
	gittest.Git(t, dir, "worktree", "add", "-q", wt, "sto/x")
	gittest.Git(t, wt, "merge", "-q", "--no-edit", "origin/main")
	tip = gittest.Git(t, wt, "rev-parse", "HEAD")
	return k, it, dir, wt, tip, originMain
}

// Collecting from inside a worktree whose HEAD already is the branch takes the
// branch's merge base with origin/main, not with HEAD (which would be the
// branch itself and an empty span), and the main checkout gets the same base.
func TestCollectBaseIsTheBaseBranchNotHEAD(t *testing.T) {
	k, it, dir, wt, tip, originMain := collectFixture(t)
	want := gittest.Git(t, dir, "merge-base", "origin/main", "sto/x")
	if want != originMain {
		t.Fatalf("fixture: merge-base = %s, want origin/main %s", want, originMain)
	}
	refsBefore := gittest.Git(t, dir, "for-each-ref")
	headDir, headWT := gittest.Git(t, dir, "rev-parse", "HEAD"), gittest.Git(t, wt, "rev-parse", "HEAD")

	k.Git = gitfacts.Repo{Dir: wt}
	inWT, err := k.Collect(it.ID, "sto/x", "", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if inWT.Commit != tip || inWT.Base != want || inWT.Base == inWT.Commit || inWT.BaseRef != "origin/main" {
		t.Fatalf("collected in the worktree = %+v, want commit %s base %s from origin/main", inWT, tip, want)
	}
	k.Git = gitfacts.Repo{Dir: dir}
	inMain, err := k.Collect(it.ID, "sto/x", "", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if inMain.Base != inWT.Base || inMain.Commit != inWT.Commit {
		t.Fatalf("collected in the main checkout = %+v, in the worktree = %+v", inMain, inWT)
	}
	paths, err := k.Git.(BranchGit).ChangedBetween(inWT.Base, inWT.Commit)
	if err != nil || strings.Join(paths, " ") != "x.go" {
		t.Fatalf("collected span = %v, %v; want only the story's x.go", paths, err)
	}
	if gittest.Git(t, dir, "for-each-ref") != refsBefore || gittest.Git(t, dir, "rev-parse", "HEAD") != headDir || gittest.Git(t, wt, "rev-parse", "HEAD") != headWT {
		t.Fatal("collect changed the repository")
	}
}

// --base names the ref the merge base is taken with, resolved now; a ref that
// names no commit is refused.
func TestCollectBaseRef(t *testing.T) {
	k, it, dir, wt, _, _ := collectFixture(t)
	k.Git = gitfacts.Repo{Dir: wt}
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", gittest.Git(t, dir, "rev-parse", "main~1"))
	c, err := k.Collect(it.ID, "sto/x", "main", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if want := gittest.Git(t, dir, "merge-base", "main", "sto/x"); c.Base != want || c.BaseRef != "main" {
		t.Fatalf("collected = %+v, want base %s from main", c, want)
	}
	if _, err := k.Collect(it.ID, "sto/x", "nope", "ada"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("collect --base nope = %v", err)
	}
}

// A branch that adds nothing over its base (here, one already merged into
// origin/main) is refused, naming the branch and the base, and nothing is
// recorded.
func TestCollectRefusesABranchThatAddsNothing(t *testing.T) {
	k, it, dir, wt, tip, _ := collectFixture(t)
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", tip)
	k.Git = gitfacts.Repo{Dir: wt}
	_, err := k.Collect(it.ID, "sto/x", "", "ada")
	if err == nil {
		t.Fatal("collecting a branch that adds nothing succeeded")
	}
	for _, want := range []string{"sto/x", "origin/main", tip[:12]} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %s", err, want)
		}
	}
	events, _ := k.Ledger.Read(it.ID)
	for _, e := range events {
		if e.Kind == KindBranchCollected {
			t.Fatal("a refused collect recorded a branch")
		}
	}
}

// A bounded drive moves its steps and concludes accept; Drive is unbounded up
// to MaxDriveSteps and still walks to the terminal state.
func TestDriveStepsStopsAfterN(t *testing.T) {
	k, d := lineKernel(t, "", "")
	it := newItem(t, k)
	res, err := k.DriveSteps(context.Background(), it.ID, "node", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictAccept || res.Item.Status != "b" || len(res.Steps) != 1 || !res.Steps[0].Moved ||
		res.Notes != "moved a → b; stopped after 1 step(s)" {
		t.Fatalf("DriveSteps(1) = %+v", res)
	}
	if len(d.calls) != 0 {
		t.Fatalf("one transition performed %v", d.calls)
	}
	res, err = k.Drive(context.Background(), it.ID, "node")
	if err != nil || res.Verdict != VerdictAccept || res.Item.Status != "c" {
		t.Fatalf("Drive after DriveSteps = %+v, %v", res, err)
	}
}

// Drive still rejects a cycle at MaxDriveSteps.
func TestDriveRejectsAtMaxDriveSteps(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"loop\"\n")
	write(t, filepath.Join(root, "workflows", "loop.toml"), `
[states.x]
start = true
[states.y]
[states.z]
terminal = true
[[transition]]
from = ["x"]
to = "y"
[[transition]]
from = ["y"]
to = "x"
[[transition]]
from = ["y"]
to = "z"
`)
	k, err := Open(Options{Root: root, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	it := newItem(t, k)
	res, err := k.Drive(context.Background(), it.ID, "node")
	if err != nil || res.Verdict != VerdictReject || len(res.Steps) != MaxDriveSteps ||
		res.Notes != fmt.Sprintf("stopped after %d steps", MaxDriveSteps) {
		t.Fatalf("Drive on a cycle = %v steps, %q, %v", len(res.Steps), res.Notes, err)
	}
}
