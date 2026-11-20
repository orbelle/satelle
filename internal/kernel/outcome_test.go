package kernel

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func performerEvent(t *testing.T, itemID string, pr PerformerRun) ledger.Event {
	t.Helper()
	b, err := json.Marshal(pr)
	if err != nil {
		t.Fatal(err)
	}
	return ledger.Event{Item: itemID, Kind: KindPerformerRun, Payload: b}
}

func TestItemOutcomeGateRoundsAndTerminalState(t *testing.T) {
	it := item.Item{ID: "sto_11111111", Workflow: "wf", Status: "finished"}
	events := []ledger.Event{
		judgeEvent(t, it.ID, 0, gate.Result{Transition: "s1->s2", Gate: "chk", Result: gate.Accept, Attempt: "a1", Seq: 1}),
		judgeEvent(t, it.ID, 1, gate.Result{Transition: "s1->s2", Gate: "rev", Binding: "j1", Command: "claude", Model: "m-reject", Result: gate.Reject, Attempt: "a1", Seq: 2}),
		judgeEvent(t, it.ID, 2, gate.Result{Transition: "s1->s2", Gate: "rev", Result: gate.Skipped}),
		judgeEvent(t, it.ID, 3, gate.Result{Transition: "s1->s2", Gate: "rev", Result: gate.Carried}),
		judgeEvent(t, it.ID, 4, gate.Result{
			Transition: "s1->s2", Gate: "pan", Binding: "p1,p2", Result: gate.Accept, Attempt: "a2", Seq: 3,
			Members: []gate.Member{
				{Binding: "p1", Command: "claude", Model: "m-p1", Result: gate.Accept},
				{Binding: "p2", Command: "codex", Model: "m-p2", Result: gate.Accept},
			},
		}),
		judgeEvent(t, it.ID, 5, gate.Result{Transition: "s1->s2", Gate: "rev", Binding: "j2", Command: "pi", Model: "m-accept", Result: gate.Accept, Attempt: "a2", Seq: 2}),
		judgeEvent(t, it.ID, 6, gate.Result{Transition: "s2->finished", Gate: "rev", Binding: "j2", Command: "pi", Model: "m-second-edge", Result: gate.Accept, Attempt: "a3", Seq: 1}),
	}
	got := itemOutcome(it, events, true)
	if got.Item != it.ID || got.Workflow != "wf" || got.Status != "finished" || !got.Terminal {
		t.Fatalf("header = %+v, want terminal finished on wf", got)
	}
	want := []ItemOutcomeRow{
		{Kind: OutcomeGate, Step: "rev", Transition: "s1->s2", Round: 1, Result: gate.Reject, Binding: "j1", Command: "claude", Model: "m-reject"},
		{Kind: OutcomeGate, Step: "rev", Transition: "s1->s2", Round: 2, Result: gate.Accept, Binding: "j2", Command: "pi", Model: "m-accept"},
		{Kind: OutcomeGate, Step: "pan", Transition: "s1->s2", Round: 1, Result: gate.Accept, Binding: "p1", Command: "claude", Model: "m-p1"},
		{Kind: OutcomeGate, Step: "pan", Transition: "s1->s2", Round: 1, Result: gate.Accept, Binding: "p2", Command: "codex", Model: "m-p2"},
		{Kind: OutcomeGate, Step: "rev", Transition: "s2->finished", Round: 1, Result: gate.Accept, Binding: "j2", Command: "pi", Model: "m-second-edge"},
	}
	if !slices.Equal(got.Rows, want) {
		t.Fatalf("rows =\n%+v\nwant\n%+v", got.Rows, want)
	}

	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	created, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("terminal check")}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	out, err := k.ItemOutcome(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Terminal || out.Status != created.Status || out.Rows == nil || len(out.Rows) != 0 {
		t.Fatalf("initial outcome = %+v, want non-terminal with empty non-nil rows", out)
	}
	if _, err := k.Transition(context.Background(), created.ID, "done", "tester"); err != nil {
		t.Fatal(err)
	}
	out, err = k.ItemOutcome(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Terminal || out.Status != "done" {
		t.Fatalf("terminal outcome = %+v, want terminal done", out)
	}
	delete(k.Workflows, created.Workflow)
	out, err = k.ItemOutcome(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Terminal {
		t.Fatalf("missing workflow reported terminal: %+v", out)
	}
	if _, err := k.ItemOutcome("../bad"); err == nil {
		t.Error("invalid item id passed")
	}
	if _, err := k.ItemOutcome("sto_00000000"); err == nil {
		t.Error("missing item id passed")
	}
}

func TestItemOutcomePerformerRuns(t *testing.T) {
	it := item.Item{ID: "sto_22222222", Workflow: "wf", Status: "s2"}
	events := []ledger.Event{
		performerEvent(t, it.ID, PerformerRun{State: "s1", Binding: "p-a", Skill: "sk1", Command: "cursor-agent", Model: "m-1"}),
		judgeEvent(t, it.ID, 1, gate.Result{Transition: "s1->s2", Gate: "g", Binding: "j", Command: "claude", Model: "m-j", Result: gate.Reject}),
		performerEvent(t, it.ID, PerformerRun{State: "s1", Binding: "p-a", Skill: "sk1", Command: "cursor-agent", Model: "m-1", Error: "timed out"}),
		performerEvent(t, it.ID, PerformerRun{State: "s1", Binding: "p-a", Skill: "sk1", Command: "cursor-agent", Model: "m-2"}),
		judgeEvent(t, it.ID, 2, gate.Result{Transition: "s1->s2", Gate: "g", Binding: "j", Command: "claude", Model: "m-j", Result: gate.Accept}),
		performerEvent(t, it.ID, PerformerRun{State: "s2", Binding: "p-b", Skill: "sk2", Command: "codex", Model: "m-3"}),
	}
	got := itemOutcome(it, events, false)
	if got.Terminal {
		t.Fatalf("unexpected terminal outcome: %+v", got)
	}
	want := []ItemOutcomeRow{
		{Kind: OutcomePerformer, Step: "s1", Round: 1, Result: "ok", Binding: "p-a", Command: "cursor-agent", Model: "m-1"},
		{Kind: OutcomeGate, Step: "g", Transition: "s1->s2", Round: 1, Result: gate.Reject, Binding: "j", Command: "claude", Model: "m-j"},
		{Kind: OutcomePerformer, Step: "s1", Round: 2, Result: gate.Error, Binding: "p-a", Command: "cursor-agent", Model: "m-1"},
		{Kind: OutcomePerformer, Step: "s1", Round: 3, Result: "ok", Binding: "p-a", Command: "cursor-agent", Model: "m-2"},
		{Kind: OutcomeGate, Step: "g", Transition: "s1->s2", Round: 2, Result: gate.Accept, Binding: "j", Command: "claude", Model: "m-j"},
		{Kind: OutcomePerformer, Step: "s2", Round: 1, Result: "ok", Binding: "p-b", Command: "codex", Model: "m-3"},
	}
	if !slices.Equal(got.Rows, want) {
		t.Fatalf("rows =\n%+v\nwant\n%+v", got.Rows, want)
	}
}

func TestItemOutcomeMissingModelIsUnknown(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "agents.toml"), `
[agents.perf]
command = "claude"
model = "live-perf"
tools = ["Read"]
[agents.j1]
command = "claude"
model = "live-j1"
tools = ["Read"]
[agents.j2]
command = "claude"
model = "live-j2"
tools = ["Read"]
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("legacy")}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []struct {
		kind    string
		payload any
	}{
		{KindPerformerRun, PerformerRun{State: "s1", Binding: "perf", Skill: "sk"}},
		{gate.KindRun, gate.Result{Transition: "s1->s2", Gate: "g1", Binding: "j1", Result: gate.Accept}},
		{gate.KindRun, gate.Result{
			Transition: "s1->s2", Gate: "g2", Binding: "j1,j2", Result: gate.Accept,
			Members: []gate.Member{
				{Binding: "j1", Result: gate.Accept},
				{Binding: "j2", Command: "pi", Model: "recorded-j2", Result: gate.Accept},
			},
		}},
	} {
		if _, err := k.Ledger.Append(it.ID, ev.kind, "tester", ev.payload); err != nil {
			t.Fatal(err)
		}
	}
	out, err := k.ItemOutcome(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []ItemOutcomeRow{
		{Kind: OutcomePerformer, Step: "s1", Round: 1, Result: "ok", Binding: "perf", Model: UnknownModel},
		{Kind: OutcomeGate, Step: "g1", Transition: "s1->s2", Round: 1, Result: gate.Accept, Binding: "j1", Model: UnknownModel},
		{Kind: OutcomeGate, Step: "g2", Transition: "s1->s2", Round: 1, Result: gate.Accept, Binding: "j1", Model: UnknownModel},
		{Kind: OutcomeGate, Step: "g2", Transition: "s1->s2", Round: 1, Result: gate.Accept, Binding: "j2", Command: "pi", Model: "recorded-j2"},
	}
	if !slices.Equal(out.Rows, want) {
		t.Fatalf("rows =\n%+v\nwant\n%+v", out.Rows, want)
	}
}
