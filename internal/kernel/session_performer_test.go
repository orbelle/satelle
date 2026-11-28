package kernel

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func openSessionPerformerKernel(t *testing.T, gatePasses bool) *Kernel {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "agents.toml"), `
[agents.sess]
command = "session"
[agents.disp]
command = "claude"
model = "m-disp"
tools = ["Read"]
`)
	write(t, filepath.Join(root, "skills", "work.md"), "---\nname: work\n---\nDo work.\n")
	checkBody := "---\ntype: check\n---\n```sh\nexit 0\n```\n"
	if !gatePasses {
		checkBody = "---\ntype: check\n---\n```sh\necho blocked\nexit 1\n```\n"
	}
	write(t, filepath.Join(root, "skills", "chk.md"), checkBody)
	write(t, filepath.Join(root, "workflows", "sessflow.toml"), `
name = "sessflow"
[states.st-start]
start = true
[states.st-session]
performer = { agent = "sess", skill = "work" }
[states.st-dispatched]
performer = { agent = "disp", skill = "work" }
[states.st-operator]
performer = { operator = "decision" }
[states.st-park]
park = true
[states.st-done]
terminal = true

[gates.g1]
skill = "chk"

[[transition]]
from = ["st-start"]
to = "st-session"
[[transition]]
from = ["st-start"]
to = "st-dispatched"
[[transition]]
from = ["st-start"]
to = "st-operator"
[[transition]]
from = ["st-session"]
to = "st-done"
groups = [{ gates = ["g1"] }]
[[transition]]
from = ["st-session"]
to = "st-park"
[[transition]]
from = ["st-dispatched"]
to = "st-done"
[[transition]]
from = ["st-operator"]
to = "st-done"
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

func performerRuns(t *testing.T, k *Kernel, id string) ([]ledger.Event, []PerformerRun) {
	t.Helper()
	evs, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	var runs []PerformerRun
	for _, e := range evs {
		if e.Kind != KindPerformerRun {
			continue
		}
		var pr PerformerRun
		if err := e.Decode(&pr); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, pr)
	}
	return evs, runs
}

func TestSessionPerformerRunRecordsModelAndOrder(t *testing.T) {
	k := openSessionPerformerKernel(t, true)
	k.SessionMeta = func(session string) (string, string) {
		if session == "s-cur" {
			return "cursor", "gemini-3.8-flash-high"
		}
		return "", ""
	}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("s"), Workflow: ptr("sessflow")}, "s-cur")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Seat(it.ID, "s-cur", LocalLocation(k.Dir), "", "s-cur"); err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(context.Background(), it.ID, "st-session", "s-cur"); err != nil || !res.Moved {
		t.Fatalf("to st-session: %+v %v", res, err)
	}
	if _, runs := performerRuns(t, k, it.ID); len(runs) != 0 {
		t.Fatalf("leaving state with no performer appended %+v", runs)
	}
	if res, err := k.Transition(context.Background(), it.ID, "st-done", "s-cur"); err != nil || !res.Moved {
		t.Fatalf("to st-done: %+v %v", res, err)
	}
	evs, runs := performerRuns(t, k, it.ID)
	if len(runs) != 1 {
		t.Fatalf("got %d performer.run events, want 1: %+v", len(runs), runs)
	}
	want := PerformerRun{
		State:   "st-session",
		Binding: "sess",
		Skill:   "work",
		Command: agent.PresetSession,
		Harness: "cursor",
		Model:   "gemini-3.8-flash-high",
		ModelID: "gemini-3-8-flash-high",
	}
	if runs[0] != want {
		t.Fatalf("performer.run = %+v, want %+v", runs[0], want)
	}
	moved := -1
	for i, e := range evs {
		if e.Kind == item.KindTransitioned {
			moved = i
		}
	}
	if moved < 1 || evs[moved-1].Kind != KindPerformerRun || evs[moved-1].Actor != "s-cur" {
		t.Fatalf("expected performer.run immediately before the last item.transitioned; events = %+v", evs)
	}
	var rel item.SeatRelease
	if last := evs[len(evs)-1]; last.Kind != item.KindSeatReleased || last.Decode(&rel) != nil || rel.Session != "s-cur" {
		t.Fatalf("the move into terminal st-done should end the chain in seat.released for s-cur; last = %+v", last)
	}
	if err := k.Ledger.Verify(); err != nil {
		t.Fatalf("ledger verify: %v", err)
	}
}

func TestSessionPerformerRunUnavailableModelPerHarness(t *testing.T) {
	cases := []struct {
		name        string
		meta        func(string) (string, string)
		wantHarness string
		wantModel   string
	}{
		{name: "grok", meta: func(string) (string, string) { return "grok", "" }, wantHarness: "grok", wantModel: "unavailable (grok)"},
		{name: "claude", meta: func(string) (string, string) { return "claude", "" }, wantHarness: "claude", wantModel: "unavailable (claude)"},
		{name: "pi", meta: func(string) (string, string) { return "pi", "" }, wantHarness: "pi", wantModel: "unavailable (pi)"},
		{name: "cursor", meta: func(string) (string, string) { return "cursor", "" }, wantHarness: "cursor", wantModel: "unavailable (cursor)"},
		{name: "empty", meta: func(string) (string, string) { return "", "" }, wantHarness: UnknownModel, wantModel: "unavailable (unknown)"},
		{name: "nil hook", meta: nil, wantHarness: UnknownModel, wantModel: "unavailable (unknown)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := openSessionPerformerKernel(t, true)
			k.SessionMeta = tc.meta
			it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr(tc.name), Workflow: ptr("sessflow")}, "s-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := k.Transition(context.Background(), it.ID, "st-session", "s-1"); err != nil {
				t.Fatal(err)
			}
			if _, err := k.Seat(it.ID, "s-1", LocalLocation(k.Dir), "", "s-1"); err != nil {
				t.Fatal(err)
			}
			if res, err := k.Transition(context.Background(), it.ID, "st-done", "s-1"); err != nil || !res.Moved {
				t.Fatalf("transition: %+v %v", res, err)
			}
			_, runs := performerRuns(t, k, it.ID)
			if len(runs) != 1 {
				t.Fatalf("got %d performer.run events, want 1", len(runs))
			}
			if runs[0].Command != agent.PresetSession || runs[0].Harness != tc.wantHarness || runs[0].Model != tc.wantModel || runs[0].ModelID != "" {
				t.Errorf("got %+v, want command=session harness=%q model=%q", runs[0], tc.wantHarness, tc.wantModel)
			}
		})
	}
}

func TestSessionPerformerRunNegativeCases(t *testing.T) {
	meta := func(string) (string, string) { return "cursor", "gemini-3.8-flash-high" }

	t.Run("unseated", func(t *testing.T) {
		k := openSessionPerformerKernel(t, true)
		k.SessionMeta = meta
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Workflow: ptr("sessflow")}, "s-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Transition(context.Background(), it.ID, "st-session", "s-1"); err != nil {
			t.Fatal(err)
		}
		if res, err := k.Transition(context.Background(), it.ID, "st-done", "s-1"); err != nil || !res.Moved {
			t.Fatalf("transition: %+v %v", res, err)
		}
		if _, runs := performerRuns(t, k, it.ID); len(runs) != 0 {
			t.Fatalf("unseated move appended %+v", runs)
		}
	})

	t.Run("other session holds seat", func(t *testing.T) {
		k := openSessionPerformerKernel(t, true)
		k.SessionMeta = meta
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Workflow: ptr("sessflow")}, "s-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Transition(context.Background(), it.ID, "st-session", "s-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := k.Seat(it.ID, "s-other", LocalLocation(k.Dir), "", "s-other"); err != nil {
			t.Fatal(err)
		}
		if res, err := k.Transition(context.Background(), it.ID, "st-done", "s-1"); err != nil || !res.Moved {
			t.Fatalf("transition: %+v %v", res, err)
		}
		if _, runs := performerRuns(t, k, it.ID); len(runs) != 0 {
			t.Fatalf("move by non-seated actor appended %+v", runs)
		}
	})

	t.Run("rejected gate", func(t *testing.T) {
		k := openSessionPerformerKernel(t, false)
		k.SessionMeta = meta
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Workflow: ptr("sessflow")}, "s-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Transition(context.Background(), it.ID, "st-session", "s-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := k.Seat(it.ID, "s-1", LocalLocation(k.Dir), "", "s-1"); err != nil {
			t.Fatal(err)
		}
		if res, err := k.Transition(context.Background(), it.ID, "st-done", "s-1"); err != nil || res.Moved {
			t.Fatalf("rejected transition: %+v %v", res, err)
		}
		if _, runs := performerRuns(t, k, it.ID); len(runs) != 0 {
			t.Fatalf("rejected gate appended %+v", runs)
		}
	})

	t.Run("park transition", func(t *testing.T) {
		k := openSessionPerformerKernel(t, true)
		k.SessionMeta = meta
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Workflow: ptr("sessflow")}, "s-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Transition(context.Background(), it.ID, "st-session", "s-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := k.Seat(it.ID, "s-1", LocalLocation(k.Dir), "", "s-1"); err != nil {
			t.Fatal(err)
		}
		if res, err := k.Transition(context.Background(), it.ID, "st-park", "s-1"); err != nil || !res.Moved {
			t.Fatalf("park transition: %+v %v", res, err)
		}
		if _, runs := performerRuns(t, k, it.ID); len(runs) != 0 {
			t.Fatalf("park transition appended %+v", runs)
		}
	})

	t.Run("hold transition", func(t *testing.T) {
		k := openSessionPerformerKernel(t, true)
		k.SessionMeta = meta
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Workflow: ptr("sessflow")}, "s-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Transition(context.Background(), it.ID, "st-session", "s-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := k.Seat(it.ID, "s-1", LocalLocation(k.Dir), "", "s-1"); err != nil {
			t.Fatal(err)
		}
		hold := &item.Hold{State: "st-session", Rounds: 3}
		if res, err := k.transition(context.Background(), it.ID, "st-done", "s-1", hold); err != nil || !res.Moved {
			t.Fatalf("hold transition: %+v %v", res, err)
		}
		if _, runs := performerRuns(t, k, it.ID); len(runs) != 0 {
			t.Fatalf("hold transition appended %+v", runs)
		}
	})

	t.Run("dispatched performer", func(t *testing.T) {
		k := openSessionPerformerKernel(t, true)
		k.SessionMeta = meta
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Workflow: ptr("sessflow")}, "s-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Transition(context.Background(), it.ID, "st-dispatched", "s-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := k.Seat(it.ID, "s-1", LocalLocation(k.Dir), "", "s-1"); err != nil {
			t.Fatal(err)
		}
		if res, err := k.Transition(context.Background(), it.ID, "st-done", "s-1"); err != nil || !res.Moved {
			t.Fatalf("dispatched state transition: %+v %v", res, err)
		}
		if _, runs := performerRuns(t, k, it.ID); len(runs) != 0 {
			t.Fatalf("dispatched performer state appended %+v", runs)
		}
	})

	t.Run("operator state", func(t *testing.T) {
		k := openSessionPerformerKernel(t, true)
		k.SessionMeta = meta
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Workflow: ptr("sessflow")}, "s-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Transition(context.Background(), it.ID, "st-operator", "s-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := k.Seat(it.ID, "s-1", LocalLocation(k.Dir), "", "s-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := k.RecordOperator(it.ID, "decision", []byte("approved"), "text/markdown", "op", ""); err != nil {
			t.Fatal(err)
		}
		if res, err := k.Transition(context.Background(), it.ID, "st-done", "s-1"); err != nil || !res.Moved {
			t.Fatalf("operator state transition: %+v %v", res, err)
		}
		if _, runs := performerRuns(t, k, it.ID); len(runs) != 0 {
			t.Fatalf("operator state appended %+v", runs)
		}
	})
}
