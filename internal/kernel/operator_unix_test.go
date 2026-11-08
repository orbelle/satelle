//go:build !windows

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
)

// operatorKernel is a workflow a -> deploy (the operator's) -> done, with
// deploy -> a and a -> deploy for re-entry, * -> cancelled, and a judged gate
// on deploy -> done so a refusal before any gate run is visible.
func operatorKernel(t *testing.T) (*Kernel, *fakeDispatcher, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"ops\"\n[items]\nprefix = { thing = \"th\" }\n")
	write(t, filepath.Join(root, "agents.toml"), "[agents.doer]\ncommand = \"claude\"\nmodel = \"big\"\ntools = [\"Read\", \"Edit\"]\n[agents.r1]\ncommand = \"claude\"\nmodel = \"m1\"\ntools = [\"Read\"]\n")
	write(t, filepath.Join(root, "skills", "do.md"), "Do the work.\n")
	write(t, filepath.Join(root, "skills", "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, "workflows", "ops.toml"), `
[states.a]
start = true
performer = { agent = "doer", skill = "do" }
[states.deploy]
performer = { operator = "deploy-record" }
[states.done]
terminal = true
[states.cancelled]
terminal = true

[gates.review]
skill = "review"
agent = "r1"

[[transition]]
from = ["a"]
to = "deploy"
[[transition]]
from = ["deploy"]
to = "a"
[[transition]]
from = ["deploy"]
to = "done"
groups = [{ mode = "sequential", gates = ["review"] }]
[[transition]]
from = ["*"]
to = "cancelled"
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"r1": accept, "doer": "did it"}}
	k.Dispatcher = d
	k.Git = fakeGit{}
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("ship it")}, "op")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(context.Background(), it.ID, "deploy", "op"); err != nil || !res.Moved {
		t.Fatalf("into deploy: %+v %v", res, err)
	}
	return k, d, it.ID
}

func countKind(t *testing.T, k *Kernel, id, kind string) int {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// AC1: an operator state dispatches nothing: Perform refuses and Drive stops.
func TestOperatorStateDispatchesNothing(t *testing.T) {
	k, d, id := operatorKernel(t)
	ctx := context.Background()
	if _, err := k.Perform(ctx, id, "op"); err == nil || !strings.Contains(err.Error(), "is the operator's: nothing is dispatched") {
		t.Fatalf("Perform on an operator state: %v", err)
	}
	if n := countKind(t, k, id, KindPerformerRun); n != 0 {
		t.Fatalf("performer.run recorded %d times", n)
	}
	res, err := k.Drive(ctx, id, "op")
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictReject || !strings.Contains(res.Notes, "state deploy is the operator's: it needs the operator's deploy-record record") || res.Item.Status != "deploy" {
		t.Fatalf("Drive: %+v", res)
	}
	if len(d.calls) != 0 {
		t.Fatalf("dispatched %v", d.calls)
	}
}

// AC2: leaving an operator state is refused until the operator has recorded
// an attachment of the declared type since the item entered it.
func TestOperatorRecordGatesTheExit(t *testing.T) {
	k, d, id := operatorKernel(t)
	ctx := context.Background()
	before := countKind(t, k, id, gate.KindRun)

	_, err := k.Transition(ctx, id, "done", "op")
	var ore *OperatorRecordError
	if !errors.As(err, &ore) || ore.State != "deploy" || ore.Type != "deploy-record" || !strings.Contains(err.Error(), "satelle thing record "+id) {
		t.Fatalf("no record: %v", err)
	}
	if countKind(t, k, id, gate.KindRun) != before || countKind(t, k, id, item.KindTransitioned) != 1 || len(d.calls) != 0 {
		t.Fatal("a refused exit ran a gate or moved")
	}
	// The agent and MCP shape: a typed attachment without the operator mark.
	if _, err := k.Attach(id, "deploy-record", []byte("done"), "text/markdown; type=deploy-record", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Transition(ctx, id, "done", "op"); !errors.As(err, &ore) {
		t.Fatalf("a plain attachment let it leave: %v", err)
	}
	// Every exit, * -> cancelled included.
	if _, err := k.Transition(ctx, id, "cancelled", "op"); !errors.As(err, &ore) {
		t.Fatalf("cancel without a record: %v", err)
	}
	a, err := k.RecordOperator(id, "", "a", []byte("deployed v1"), "", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Operator || a.Name != "deploy-record" || a.MediaType != "text/markdown; type=deploy-record" || a.Actor != "bob" {
		t.Fatalf("record %+v", a)
	}
	// The record from this visit does not survive a re-entry.
	if res, err := k.Transition(ctx, id, "a", "op"); err != nil || !res.Moved {
		t.Fatalf("deploy -> a: %+v %v", res, err)
	}
	if res, err := k.Transition(ctx, id, "deploy", "op"); err != nil || !res.Moved {
		t.Fatalf("a -> deploy: %+v %v", res, err)
	}
	if _, err := k.Transition(ctx, id, "done", "op"); !errors.As(err, &ore) {
		t.Fatalf("an earlier visit's record let it leave: %v", err)
	}
	if _, err := k.RecordOperator(id, "", "done", []byte("deployed v2"), "", "bob", ""); err != nil {
		t.Fatal(err)
	}
	res, err := k.Transition(ctx, id, "done", "op")
	if err != nil || !res.Moved || res.Item.Status != "done" {
		t.Fatalf("with a record: %+v %v", res, err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("the exit's gate should run once it may leave: %v", d.calls)
	}
}

// AC3: an agent session cannot record the operator's word.
func TestOperatorRecordRefusesASession(t *testing.T) {
	k, _, id := operatorKernel(t)
	if _, err := k.RecordOperator(id, "", "done", []byte("x"), "", "s-1", "s-1"); err == nil || !strings.Contains(err.Error(), "an agent session (s-1) may not record the operator's word") {
		t.Fatalf("session record: %v", err)
	}
	if n := countKind(t, k, id, item.KindAttachmentAdd); n != 0 {
		t.Fatalf("attachment.added recorded %d times", n)
	}
	var ore *OperatorRecordError
	if _, err := k.Transition(context.Background(), id, "done", "op"); !errors.As(err, &ore) {
		t.Fatalf("still refused: %v", err)
	}
	// Not in an operator state: nothing to record.
	if _, err := k.Transition(context.Background(), id, "a", "op"); !errors.As(err, &ore) {
		t.Fatalf("deploy -> a without a record: %v", err)
	}
	other, _ := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("other")}, "op")
	if _, err := k.RecordOperator(other.ID, "", "done", []byte("x"), "", "bob", ""); err == nil || !strings.Contains(err.Error(), "not the operator's") {
		t.Fatalf("record outside an operator state: %v", err)
	}
}

func TestOperatorTypeValidated(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "workflows", "x.toml"), `
[states.a]
start = true
[states.op]
performer = { operator = "bad type" }
[states.z]
terminal = true
[[transition]]
from = ["a"]
to = "op"
[[transition]]
from = ["op"]
to = "z"
`)
	joined := ""
	for _, p := range Validate(root) {
		joined += p.Error() + "\n"
	}
	if !strings.Contains(joined, `state "op" operator type "bad type" is not a valid attachment type`) {
		t.Fatalf("bad type accepted:\n%s", joined)
	}
}

// confirmKernel is a repo whose item reaches confirm, the operator's state
// (type confirmation), from in_progress; confirm moves on to approved (which
// enqueues into the release queue) or back to in_progress.
func confirmKernel(t *testing.T) (*Kernel, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"flow\"\n[[workflows.rule]]\nworkflow = \"runbook\"\nkind = \"execution\"\n")
	write(t, filepath.Join(root, "workflows", "runbook.toml"), "name = \"runbook\"\n[states.backlog]\nstart = true\n[states.done]\nterminal = true\n[[transition]]\nfrom = [\"backlog\"]\nto = \"done\"\n")
	write(t, filepath.Join(root, "workflows", "flow.toml"), `name = "flow"
[states.backlog]
start = true
[states.in_progress]
[states.confirm]
performer = { operator = "confirmation" }
[states.approved]
enqueue = ["release"]
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "confirm"
[[transition]]
from = ["confirm"]
to = "approved"
[[transition]]
from = ["confirm"]
to = "in_progress"
[[transition]]
from = ["approved"]
to = "done"
`)
	write(t, filepath.Join(root, "queues", "release.toml"), "title = \"release\"\n[trigger]\nworkflow = \"runbook\"\nmin_size = 0\n")
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("ship it")}, "op")
	if err != nil {
		t.Fatal(err)
	}
	move(t, k, it.ID, "in_progress", "confirm")
	return k, it.ID
}

// AC3: a move out of an operator state is refused unless the newest record
// since entry names it; a refused move appends nothing.
func TestOperatorRecordBindsTheMove(t *testing.T) {
	k, id := confirmKernel(t)
	ctx := context.Background()
	var ore *OperatorRecordError
	if _, err := k.Transition(ctx, id, "approved", "op"); !errors.As(err, &ore) {
		t.Fatalf("no record: %v", err)
	}
	if _, err := k.RecordOperator(id, "", "in_progress", []byte("the how-to fails"), "", "bob", ""); err != nil {
		t.Fatal(err)
	}
	before, _ := k.Ledger.Read(id)
	_, err := k.Transition(ctx, id, "approved", "op")
	var ome *OperatorMoveError
	if !errors.As(err, &ome) || ome.Recorded != "in_progress" || ome.Asked != "approved" || !strings.Contains(err.Error(), "names the move to in_progress") {
		t.Fatalf("the unnamed move: %v", err)
	}
	if after, _ := k.Ledger.Read(id); len(after) != len(before) {
		t.Fatalf("a refused move appended %d events", len(after)-len(before))
	}
	if res, err := k.Transition(ctx, id, "in_progress", "op"); err != nil || !res.Moved {
		t.Fatalf("the named move: %+v %v", res, err)
	}
	move(t, k, id, "confirm")
	// The send-back's record does not carry into the next visit.
	if _, err := k.Transition(ctx, id, "in_progress", "op"); !errors.As(err, &ore) {
		t.Fatalf("an earlier visit's record let it leave: %v", err)
	}
	if _, err := k.RecordOperator(id, "", "approved", []byte("tried it, works"), "", "bob", ""); err != nil {
		t.Fatal(err)
	}
	res, err := k.Transition(ctx, id, "approved", "op")
	if err != nil || !res.Moved || res.Item.Status != "approved" || len(res.Queued) != 1 {
		t.Fatalf("confirmed: %+v %v", res, err)
	}
}

// AC4: a send-back records the reason and reopens the item, and the reason is
// in the performer's record folder.
func TestSendBackReopensWithTheReason(t *testing.T) {
	k, id := confirmKernel(t)
	const reason = "the how-to fails at step 2"
	if _, err := k.RecordOperator(id, "", "in_progress", []byte(""), "", "bob", ""); err == nil {
		t.Fatal("an empty send-back was recorded")
	}
	if _, err := k.RecordOperator(id, "", "in_progress", []byte(reason), "", "bob", ""); err != nil {
		t.Fatal(err)
	}
	res, err := k.Transition(context.Background(), id, "in_progress", "op")
	if err != nil || !res.Moved || res.Item.Status != "in_progress" {
		t.Fatalf("send-back: %+v %v", res, err)
	}
	it, events, err := k.load(id)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := k.record(it, events, true)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	b, err := os.ReadFile(filepath.Join(rec.Dir, "attachments", "confirmation"))
	if err != nil || string(b) != reason {
		t.Fatalf("record folder confirmation: %q %v", b, err)
	}
}
