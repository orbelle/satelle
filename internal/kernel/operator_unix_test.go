//go:build !windows

package kernel

import (
	"context"
	"errors"
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
	a, err := k.RecordOperator(id, "", []byte("deployed v1"), "", "bob", "")
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
	if _, err := k.RecordOperator(id, "", []byte("deployed v2"), "", "bob", ""); err != nil {
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
	if _, err := k.RecordOperator(id, "", []byte("x"), "", "s-1", "s-1"); err == nil || !strings.Contains(err.Error(), "an agent session (s-1) may not record the operator's word") {
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
	if _, err := k.RecordOperator(other.ID, "", []byte("x"), "", "bob", ""); err == nil || !strings.Contains(err.Error(), "not the operator's") {
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
