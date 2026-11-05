package agentstep

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// modelSourceDispatchWF allocates "plan" to the named agent "architect" with a
// step-level model= override — the fixture the 4.1 regression tests below
// dispatch through. architect itself carries no model= (config.SelectModel's
// binding tier stays empty), so the effective source is the step tier unless
// an attempt escalates to a binding that names its own model.
var modelSourceDispatchWF = wfDoc(
	`["*"]
obligations = ["raised", "planned", "done"]
`,
	`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "architect"
model = "step-model"
skills = ["architecture-alignment"]
requires = ["raised"]

[done]
status = "done"
terminal = true
requires = ["planned"]
`)

// TestArtifactAttemptsReuseDispatchChoiceAcrossRepair pins the 4.1 fix: the
// initial AND repair attempts read the dispatch's own resolved choice
// (model=step-model, source=step) — neither re-selects against a binding
// that DispatchExecutor mutated with the resolved value, which is what
// previously made every attempt after the first read source=binding
// regardless of where the model actually came from.
func TestArtifactAttemptsReuseDispatchChoiceAcrossRepair(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{
		{out: validAttempt("draft")},
		{out: validAttempt("## AC1\nfixed\n## AC2\nfixed")},
	}}
	docs := fakeDocs{workflow: modelSourceDispatchWF, skillBody: attemptedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		switch name {
		case "architect":
			return config.AgentBinding{Command: "primary", Tools: "read_file"}, true
		case "stronger":
			return config.AgentBinding{Command: "stronger", Tools: "read_file", Model: "explicit-strong"}, true
		default:
			return config.AgentBinding{}, false
		}
	})
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		if command == "primary" {
			return primary, nil
		}
		return nil, nil
	}
	var events []attemptEvent
	g.SetTelemetry(func(_ context.Context, _, _, kind string, data map[string]any) {
		events = append(events, attemptEvent{kind: kind, data: data})
	})
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	if _, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (initial, repair): %#v", len(events), events)
	}
	for i, want := range []string{"initial", "repair"} {
		d := events[i].data
		if d["phase"] != want {
			t.Fatalf("event[%d] phase = %v, want %v", i, d["phase"], want)
		}
		if d["model"] != "step-model" || d["model_source"] != config.ModelSourceStep {
			t.Fatalf("event[%d] (%s) model=%v source=%v, want model=step-model source=step", i, want, d["model"], d["model_source"])
		}
	}
}

// TestArtifactAttemptsEscalationResolvesItsOwnBinding pins the other half of
// the 4.1 fix: an escalation to a genuinely different EscalateBinding
// resolves ITS OWN choice (here the escalate binding's explicit model=,
// source=binding) while the initial/repair attempts that ran before it keep
// the dispatch's own step-tier choice, unaffected by the escalation.
func TestArtifactAttemptsEscalationResolvesItsOwnBinding(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{
		{out: validAttempt("initial invalid")},
		{out: validAttempt("repair invalid")},
	}}
	stronger := &attemptRunner{runs: []attemptRun{{out: validAttempt("## AC1\nstrong\n## AC2\nstrong")}}}
	docs := fakeDocs{workflow: modelSourceDispatchWF, skillBody: attemptedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		switch name {
		case "architect":
			return config.AgentBinding{Command: "primary", Tools: "read_file"}, true
		case "stronger":
			return config.AgentBinding{Command: "stronger", Tools: "read_file", Model: "explicit-strong"}, true
		default:
			return config.AgentBinding{}, false
		}
	})
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		switch command {
		case "primary":
			return primary, nil
		case "stronger":
			return stronger, nil
		default:
			return nil, nil
		}
	}
	var events []attemptEvent
	g.SetTelemetry(func(_ context.Context, _, _, kind string, data map[string]any) {
		events = append(events, attemptEvent{kind: kind, data: data})
	})
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	if _, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (initial, repair, escalate): %#v", len(events), events)
	}
	initial, repair, escalate := events[0].data, events[1].data, events[2].data
	if initial["model_source"] != config.ModelSourceStep || initial["model"] != "step-model" {
		t.Fatalf("initial = %#v, want source=step model=step-model", initial)
	}
	if repair["model_source"] != config.ModelSourceStep || repair["model"] != "step-model" {
		t.Fatalf("repair = %#v, want source=step model=step-model (must NOT have drifted to binding)", repair)
	}
	if escalate["model_source"] != config.ModelSourceBinding || escalate["model"] != "explicit-strong" {
		t.Fatalf("escalate = %#v, want source=binding model=explicit-strong (its OWN binding's model=)", escalate)
	}
}

// TestArtifactAttemptsExplicitBindingModelWinsOutright pins AC6 at the
// DispatchExecutor level (not just the pure SelectModel function): a binding
// with its own model= wins outright over a DIFFERING step override, and every
// attempt — initial, repair, and a swapped escalate binding with a DIFFERENT
// explicit model of its own — reports source=binding with ITS OWN model,
// unaffected by the step's model= or by each other.
func TestArtifactAttemptsExplicitBindingModelWinsOutright(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{
		{out: validAttempt("initial invalid")},
		{out: validAttempt("repair invalid")},
	}}
	stronger := &attemptRunner{runs: []attemptRun{{out: validAttempt("## AC1\nstrong\n## AC2\nstrong")}}}
	docs := fakeDocs{workflow: modelSourceDispatchWF, skillBody: attemptedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		switch name {
		case "architect":
			// The binding's OWN model= must win outright over the step's
			// model="step-model" (modelSourceDispatchWF) — AC6.
			return config.AgentBinding{Command: "primary", Tools: "read_file", Model: "architect-own-model"}, true
		case "stronger":
			return config.AgentBinding{Command: "stronger", Tools: "read_file", Model: "stronger-own-model"}, true
		default:
			return config.AgentBinding{}, false
		}
	})
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		switch command {
		case "primary":
			return primary, nil
		case "stronger":
			return stronger, nil
		default:
			return nil, nil
		}
	}
	var events []attemptEvent
	g.SetTelemetry(func(_ context.Context, _, _, kind string, data map[string]any) {
		events = append(events, attemptEvent{kind: kind, data: data})
	})
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	res, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "architect-own-model" || res.ModelSource != config.ModelSourceBinding {
		t.Fatalf("dispatch result = model=%q source=%q, want architect-own-model/binding", res.Model, res.ModelSource)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	initial, repair, escalate := events[0].data, events[1].data, events[2].data
	if initial["model"] != "architect-own-model" || initial["model_source"] != config.ModelSourceBinding {
		t.Fatalf("initial = %#v", initial)
	}
	if repair["model"] != "architect-own-model" || repair["model_source"] != config.ModelSourceBinding {
		t.Fatalf("repair = %#v", repair)
	}
	if escalate["model"] != "stronger-own-model" || escalate["model_source"] != config.ModelSourceBinding {
		t.Fatalf("escalate = %#v, want its OWN binding's model (not architect's)", escalate)
	}
}

// TestArtifactAttemptsEscalationBindingWithNoModelFallsThroughToStep pins the
// 4.1 escalation path (attempt.go:186) for an EscalateBinding that has NO
// model= of its own: g.selectModel resolves it against the SAME step override
// the dispatch itself carries, so the escalation attempt row lands on
// source=step (model=step-model) rather than an empty/cli-default pick —
// escalating to a model-less binding must not lose the step's instruction.
func TestArtifactAttemptsEscalationBindingWithNoModelFallsThroughToStep(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{
		{out: validAttempt("initial invalid")},
		{out: validAttempt("repair invalid")},
	}}
	stronger := &attemptRunner{runs: []attemptRun{{out: validAttempt("## AC1\nstrong\n## AC2\nstrong")}}}
	docs := fakeDocs{workflow: modelSourceDispatchWF, skillBody: attemptedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		switch name {
		case "architect":
			return config.AgentBinding{Command: "primary", Tools: "read_file"}, true
		case "stronger":
			// No model= on the escalate binding itself.
			return config.AgentBinding{Command: "stronger", Tools: "read_file"}, true
		default:
			return config.AgentBinding{}, false
		}
	})
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		switch command {
		case "primary":
			return primary, nil
		case "stronger":
			return stronger, nil
		default:
			return nil, nil
		}
	}
	var events []attemptEvent
	g.SetTelemetry(func(_ context.Context, _, _, kind string, data map[string]any) {
		events = append(events, attemptEvent{kind: kind, data: data})
	})
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	if _, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (initial, repair, escalate): %#v", len(events), events)
	}
	escalate := events[2].data
	if escalate["model_source"] != config.ModelSourceStep || escalate["model"] != "step-model" {
		t.Fatalf("escalate = %#v, want source=step model=step-model (falls through to the step override, its own binding has no model=)", escalate)
	}
}

// TestArtifactAttemptsEscalationWithNoBindingRepeatsDispatchSource pins the
// OTHER escalation shape: attempt_escalate_binding is unset entirely, so the
// escalate phase never calls g.selectModel again — it reuses the dispatch's
// own already-resolved choice unchanged (attempt.go:172-175, "current =
// choice"). The escalation attempt row must repeat the exact model/source the
// initial and repair attempts carried, and no "stronger" runner is ever built
// since there is no escalate binding to switch to.
func TestArtifactAttemptsEscalationWithNoBindingRepeatsDispatchSource(t *testing.T) {
	noEscalateBinding := strings.Replace(attemptedDispatchSkill, "attempt_escalate_binding: stronger\n", "", 1)
	primary := &attemptRunner{runs: []attemptRun{
		{out: validAttempt("initial invalid")},
		{out: validAttempt("repair invalid")},
		{out: validAttempt("## AC1\nfixed\n## AC2\nfixed")},
	}}
	stronger := &attemptRunner{}
	docs := fakeDocs{workflow: modelSourceDispatchWF, skillBody: noEscalateBinding, skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		switch name {
		case "architect":
			return config.AgentBinding{Command: "primary", Tools: "read_file"}, true
		case "stronger":
			return config.AgentBinding{Command: "stronger", Tools: "read_file", Model: "explicit-strong"}, true
		default:
			return config.AgentBinding{}, false
		}
	})
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		switch command {
		case "primary":
			return primary, nil
		case "stronger":
			return stronger, nil
		default:
			return nil, nil
		}
	}
	var events []attemptEvent
	g.SetTelemetry(func(_ context.Context, _, _, kind string, data map[string]any) {
		events = append(events, attemptEvent{kind: kind, data: data})
	})
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	if _, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan"); err != nil {
		t.Fatal(err)
	}
	if len(stronger.requests) != 0 {
		t.Fatalf("stronger runner requests = %d, want 0 (no escalate binding to switch to)", len(stronger.requests))
	}
	if len(primary.requests) != 3 {
		t.Fatalf("primary runner requests = %d, want 3 (initial, repair, escalate all on the same runner)", len(primary.requests))
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (initial, repair, escalate): %#v", len(events), events)
	}
	for i, want := range []string{"initial", "repair", "escalate"} {
		d := events[i].data
		if d["phase"] != want {
			t.Fatalf("event[%d] phase = %v, want %v", i, d["phase"], want)
		}
		if d["model"] != "step-model" || d["model_source"] != config.ModelSourceStep {
			t.Fatalf("event[%d] (%s) model=%v source=%v, want step-model/step (escalation repeats the dispatch's own choice)", i, want, d["model"], d["model_source"])
		}
	}
}

// TestArtifactAttemptsDispatchDoesNotMutateCallerBinding pins the caller-facing
// half of the 4.1 fix (engine.go:1468): DispatchExecutor computes modelChoice
// from the resolved "architect" binding but never writes the resolved model
// back onto it. Dispatching TWICE through the SAME namedAgents resolver still
// resolves source=step (not source=binding, which is what a leaked mutation
// from the first dispatch would produce on the second), and a direct call to
// the resolver after both dispatches still reports an empty model=.
func TestArtifactAttemptsDispatchDoesNotMutateCallerBinding(t *testing.T) {
	runners := []*attemptRunner{
		{runs: []attemptRun{{out: validAttempt("## AC1\nok\n## AC2\nok")}}},
		{runs: []attemptRun{{out: validAttempt("## AC1\nok\n## AC2\nok")}}},
	}
	docs := fakeDocs{workflow: modelSourceDispatchWF, skillBody: attemptedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	architectCalls := 0
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		switch name {
		case "architect":
			architectCalls++
			return config.AgentBinding{Command: "primary", Tools: "read_file"}, true
		case "stronger":
			return config.AgentBinding{Command: "stronger", Tools: "read_file", Model: "explicit-strong"}, true
		default:
			return config.AgentBinding{}, false
		}
	})
	runnerCalls := 0
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		if command != "primary" {
			return nil, nil
		}
		r := runners[runnerCalls]
		runnerCalls++
		return r, nil
	}
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	for i := 0; i < 2; i++ {
		res, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan")
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
		if res.ModelSource != config.ModelSourceStep || res.Model != "step-model" {
			t.Fatalf("dispatch %d = model=%q source=%q, want step-model/step (a leaked mutation would drift to source=binding)", i, res.Model, res.ModelSource)
		}
	}
	if architectCalls != 2 {
		t.Fatalf("architect resolver calls = %d, want 2", architectCalls)
	}
	if b, ok := g.namedAgents("architect"); !ok || b.Model != "" {
		t.Fatalf("architect binding.Model = %q after dispatch, want empty (no mutation leaked onto the resolver's own binding)", b.Model)
	}
}

// modelSourceDispatchWFNoOverride is modelSourceDispatchWF without the step's
// own model= — the fixture the inherited/creator/cli-default tests below
// dispatch through, so config.SelectModel's session-model tiers (not the step
// tier) are what decide the outcome.
var modelSourceDispatchWFNoOverride = wfDoc(
	`["*"]
obligations = ["raised", "planned", "done"]
`,
	`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "architect"
skills = ["architecture-alignment"]
requires = ["raised"]

[done]
status = "done"
terminal = true
requires = ["planned"]
`)

// modelSlotNamedAgents resolves "architect"/"stronger" to bindings that carry
// a {model} slot and NO model= of their own — the shape config.SelectModel's
// inherited/creator tiers need a real slot to fill (AC6's binding-wins-
// outright case is pinned above with a plain "primary"/"stronger" command
// that has no slot at all).
func modelSlotNamedAgents(name string) (config.AgentBinding, bool) {
	switch name {
	case "architect":
		return config.AgentBinding{Command: "primary --model {model}", Tools: "read_file"}, true
	case "stronger":
		return config.AgentBinding{Command: "stronger --model {model}", Tools: "read_file"}, true
	default:
		return config.AgentBinding{}, false
	}
}

// TestArtifactAttemptsResolveWithoutChatSession pins sty_6f9ba7ca AC3 at the
// engine level: with no story chat (there is no orchestrator tier to capture
// into), a binding with no model= and a {model} slot resolves the in-loop
// session's model ahead of [model_order], and falls to [model_order] when
// there is no in-loop model either.
func TestArtifactAttemptsResolveWithoutChatSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		inLoop     config.SessionModel
		wantModel  string
		wantSource string
	}{
		{"in-loop session wins", config.SessionModel{Model: "claude-sonnet-5", Executable: "primary"}, "claude-sonnet-5", config.ModelSourceInheritedInLoop},
		{"no session falls to order", config.SessionModel{}, "opus", config.ModelSourceOrder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := &attemptRunner{runs: []attemptRun{{out: validAttempt("## AC1\nok\n## AC2\nok")}}}
			docs := fakeDocs{workflow: modelSourceDispatchWFNoOverride, skillBody: attemptedDispatchSkill, skillFound: true}
			g, _ := newEngine(t, "", docs)
			g.SetNamedAgents(modelSlotNamedAgents)
			g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
				if command == "primary --model {model}" {
					return primary, nil
				}
				return nil, nil
			}
			g.SetModelOrder(func(string) []config.ModelRank {
				return []config.ModelRank{{"opus"}}
			})
			g.SetSessionModelsResolver(func(context.Context, string) (inLoop, creator config.SessionModel) {
				return tc.inLoop, config.SessionModel{}
			})
			g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
				return "design", "design-note", nil
			})
			res, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan")
			if err != nil {
				t.Fatal(err)
			}
			if res.Model != tc.wantModel || res.ModelSource != tc.wantSource {
				t.Fatalf("dispatch result = model=%q source=%q, want %s/%s", res.Model, res.ModelSource, tc.wantModel, tc.wantSource)
			}
		})
	}
}

// TestArtifactAttemptsInheritedModelReachesACPRequest pins sty_bb92973f: an
// ACP binding has no {model} slot, yet the inherited in-loop model
// (same executable) is selected and lands on the agentcli.Request the ACP
// session configuration is built from.
func TestArtifactAttemptsInheritedModelReachesACPRequest(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{{out: validAttempt("## AC1\nok\n## AC2\nok")}}}
	docs := fakeDocs{workflow: modelSourceDispatchWFNoOverride, skillBody: attemptedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name == "architect" {
			return config.AgentBinding{Command: "primary stdio", Interface: config.InterfaceACP, Tools: "read_file"}, true
		}
		return config.AgentBinding{}, false
	})
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		if command == "primary stdio" {
			return primary, nil
		}
		return nil, nil
	}
	g.SetSessionModelsResolver(func(context.Context, string) (inLoop, creator config.SessionModel) {
		return config.SessionModel{Model: "grok-4.5", Executable: "primary"}, config.SessionModel{}
	})
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	res, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "grok-4.5" || res.ModelSource != config.ModelSourceInheritedInLoop {
		t.Fatalf("dispatch result = model=%q source=%q, want grok-4.5/inherited-in-loop", res.Model, res.ModelSource)
	}
	if len(primary.requests) != 1 || primary.requests[0].Model != "grok-4.5" {
		t.Fatalf("ACP runner requests = %+v, want one request carrying Model grok-4.5", primary.requests)
	}
}

// TestDispatchModelOrderTier pins sty_4fde0a50 at the engine level: with no
// pin and no inherited model, the dispatch takes the first entry of its OWN
// executable's order and records source=order; a list keyed to another
// executable is never applied.
func TestDispatchModelOrderTier(t *testing.T) {
	for _, tc := range []struct {
		name      string
		order     map[string][]config.ModelRank
		wantModel string
		wantSrc   string
	}{
		{"own executable", map[string][]config.ModelRank{"primary": {{"m-first"}, {"m-second"}}}, "m-first", config.ModelSourceOrder},
		{"other executable only", map[string][]config.ModelRank{"claude": {{"opus"}}}, "", config.ModelSourceCLIDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := &attemptRunner{runs: []attemptRun{{out: validAttempt("## AC1\nok\n## AC2\nok")}}}
			docs := fakeDocs{workflow: modelSourceDispatchWFNoOverride, skillBody: attemptedDispatchSkill, skillFound: true}
			g, _ := newEngine(t, "", docs)
			g.SetNamedAgents(modelSlotNamedAgents)
			g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
				if command == "primary --model {model}" {
					return primary, nil
				}
				return nil, nil
			}
			cfg := config.AgentsConfig{ModelOrder: tc.order}
			g.SetModelOrder(cfg.OrderFor)
			g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
				return "design", "design-note", nil
			})
			res, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan")
			if err != nil {
				t.Fatal(err)
			}
			if res.Model != tc.wantModel || res.ModelSource != tc.wantSrc {
				t.Fatalf("model=%q source=%q, want %q/%q", res.Model, res.ModelSource, tc.wantModel, tc.wantSrc)
			}
		})
	}
}

// TestArtifactAttemptsInheritedInLoopModel pins the same wiring for the
// IN-LOOP session's model (the orchestrator's is unknown), and — the specific
// gap this test closes — that the choice applies uniformly to the dispatch
// result, the INITIAL attempt row, AND a REPAIR attempt row alike, not just
// the top-level pick.
func TestArtifactAttemptsInheritedInLoopModel(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{
		{out: validAttempt("initial invalid")},
		{out: validAttempt("## AC1\nfixed\n## AC2\nfixed")},
	}}
	docs := fakeDocs{workflow: modelSourceDispatchWFNoOverride, skillBody: attemptedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(modelSlotNamedAgents)
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		if command == "primary --model {model}" {
			return primary, nil
		}
		return nil, nil
	}
	g.SetSessionModelsResolver(func(context.Context, string) (inLoop, creator config.SessionModel) {
		return config.SessionModel{Model: "claude-sonnet-5", Executable: "primary"}, config.SessionModel{}
	})
	var events []attemptEvent
	g.SetTelemetry(func(_ context.Context, _, _, kind string, data map[string]any) {
		events = append(events, attemptEvent{kind: kind, data: data})
	})
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	res, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "claude-sonnet-5" || res.ModelSource != config.ModelSourceInheritedInLoop {
		t.Fatalf("dispatch result = model=%q source=%q, want claude-sonnet-5/inherited-in-loop", res.Model, res.ModelSource)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (initial, repair): %#v", len(events), events)
	}
	for i, want := range []string{"initial", "repair"} {
		d := events[i].data
		if d["model"] != "claude-sonnet-5" || d["model_source"] != config.ModelSourceInheritedInLoop {
			t.Fatalf("event[%d] (%s) model=%v source=%v, want claude-sonnet-5/inherited-in-loop", i, want, d["model"], d["model_source"])
		}
	}
}

// TestArtifactAttemptsCreatorModel pins the creator tier: with both
// orchestrator and in-loop unknown, the story-creating session's model
// resolves on the dispatch result.
func TestArtifactAttemptsCreatorModel(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{{out: validAttempt("## AC1\nok\n## AC2\nok")}}}
	docs := fakeDocs{workflow: modelSourceDispatchWFNoOverride, skillBody: attemptedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(modelSlotNamedAgents)
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		if command == "primary --model {model}" {
			return primary, nil
		}
		return nil, nil
	}
	g.SetSessionModelsResolver(func(context.Context, string) (inLoop, creator config.SessionModel) {
		return config.SessionModel{}, config.SessionModel{Model: "claude-haiku-4-5", Executable: "primary"}
	})
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	res, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "claude-haiku-4-5" || res.ModelSource != config.ModelSourceCreator {
		t.Fatalf("dispatch result = model=%q source=%q, want claude-haiku-4-5/creator", res.Model, res.ModelSource)
	}
}

// TestArtifactAttemptsCLIDefaultModel pins the floor: with no binding model=,
// no step/agent override, and no session model resolved for any role (no
// resolver wired at all — the nil-safe default), the dispatch result AND its
// initial attempt row both carry an empty model and source=cli-default.
func TestArtifactAttemptsCLIDefaultModel(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{{out: validAttempt("## AC1\nok\n## AC2\nok")}}}
	docs := fakeDocs{workflow: modelSourceDispatchWFNoOverride, skillBody: attemptedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(modelSlotNamedAgents)
	g.newRunner = func(_iface, command string) (agentcli.Runner, error) {
		if command == "primary --model {model}" {
			return primary, nil
		}
		return nil, nil
	}
	var events []attemptEvent
	g.SetTelemetry(func(_ context.Context, _, _, kind string, data map[string]any) {
		events = append(events, attemptEvent{kind: kind, data: data})
	})
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		return "design", "design-note", nil
	})
	res, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "" || res.ModelSource != config.ModelSourceCLIDefault {
		t.Fatalf("dispatch result = model=%q source=%q, want empty/cli-default", res.Model, res.ModelSource)
	}
	if len(events) != 1 || events[0].data["model"] != "" || events[0].data["model_source"] != config.ModelSourceCLIDefault {
		t.Fatalf("initial attempt event = %#v", events)
	}
}

// TestGate_InheritedInLoopModelSource pins the gate path: a default
// [reviewer] binding with no model= and a {model} slot resolves the in-loop
// session's model when the orchestrator's is unknown, and the source rides on
// BOTH the top-level GateDecision and the synthesised ReviewerVerdict
// (runReviewerWith's own selectModel call, distinct from a named dispatch's).
func TestGate_InheritedInLoopModelSource(t *testing.T) {
	docs := fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}
	g, _ := newEngine(t, `{"decision":"accept"}`, docs)
	g.SetReviewerBinding(config.AgentBinding{Command: "fake --model {model}", Tools: "Read,Grep,Glob"})
	g.SetSessionModelsResolver(func(context.Context, string) (inLoop, creator config.SessionModel) {
		return config.SessionModel{Model: "claude-sonnet-5", Executable: "fake"}, config.SessionModel{}
	})
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Accept {
		t.Fatalf("want accept, got %+v", dec)
	}
	if dec.Model != "claude-sonnet-5" || dec.ModelSource != config.ModelSourceInheritedInLoop {
		t.Fatalf("dec = model=%q source=%q, want claude-sonnet-5/inherited-in-loop", dec.Model, dec.ModelSource)
	}
	if len(dec.Reviewers) != 1 || dec.Reviewers[0].ModelSource != config.ModelSourceInheritedInLoop {
		t.Fatalf("reviewer verdict = %+v, want inherited-in-loop", dec.Reviewers)
	}
}

// TestOpenSessionAsWithModelRecordsLiveInvocationRows pins 4.2: a live
// session ledgers its own open (model_resolved=unavailable — nothing has run
// yet) and close (model_resolved from the session's last EventUsage) as real
// agent_invocation rows through the wired recorder, so a live dispatch shows
// up in `satelle story cost` and the web timeline exactly like a one-shot one.
func TestOpenSessionAsWithModelRecordsLiveInvocationRows(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "orchestrator" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Interface: "stream", Tools: "Read,Grep,Glob,Bash(satelle:*)", Command: "claude -p {tools}"}, true
	})
	var rows []map[string]any
	g.SetInvocationRecorder(func(_ context.Context, itemID string, payload map[string]any) error {
		if itemID != "sty_live" {
			t.Errorf("itemID = %q, want sty_live", itemID)
		}
		rows = append(rows, payload)
		return nil
	})
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			if req.OnEvent != nil {
				req.OnEvent(agentcli.Event{Kind: agentcli.EventUsage, Usage: &agentcli.UsageResult{
					Available: true, InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
					ModelResolved: "claude-sonnet-5",
				}})
			}
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAsWithModel(context.Background(), "orchestrator", SessionRoleDriving,
		workitem.Item{ID: "sty_live", Status: "in_progress"}, nil, nil, "y")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (open, close): %#v", len(rows), rows)
	}
	open, closeRow := rows[0], rows[1]
	if open["phase"] != "open" || open["model"] != "y" || open["model_source"] != config.ModelSourceAgent ||
		open["model_resolved"] != agentcli.ModelUnavailable {
		t.Fatalf("open row = %#v", open)
	}
	if closeRow["phase"] != "close" || closeRow["model"] != "y" || closeRow["model_source"] != config.ModelSourceAgent ||
		closeRow["model_resolved"] != "claude-sonnet-5" || closeRow["usage_available"] != true {
		t.Fatalf("close row = %#v", closeRow)
	}
	// The open row carries the byte lengths of what satelle actually sent
	// (sty_363eaf55 AC3) — never a measured zero for a real prompt/payload.
	if b, ok := open["system_prompt_bytes"].(int); !ok || b <= 0 {
		t.Fatalf("open row system_prompt_bytes = %#v, want a positive int", open["system_prompt_bytes"])
	}
	if b, ok := open["payload_bytes"].(int); !ok || b <= 0 {
		t.Fatalf("open row payload_bytes = %#v, want a positive int", open["payload_bytes"])
	}
}

// TestOpenSessionAsWithModelSumsMultiTurnUsage pins sty_363eaf55 AC2: a
// Claude stream-json `result` usage is PER TURN, not cumulative, so the close
// row must carry the SUM across every turn (not just the last one) — three
// EventUsage events, including cache fields, must add up rather than overwrite.
func TestOpenSessionAsWithModelSumsMultiTurnUsage(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "orchestrator" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Interface: "stream", Tools: "Read,Grep,Glob,Bash(satelle:*)", Command: "claude -p {tools}"}, true
	})
	var rows []map[string]any
	g.SetInvocationRecorder(func(_ context.Context, _ string, payload map[string]any) error {
		rows = append(rows, payload)
		return nil
	})
	turns := []agentcli.UsageResult{
		{Available: true, InputTokens: 100, FreshInputTokens: 50, CacheCreationInputTokens: 30, CacheReadInputTokens: 20,
			OutputTokens: 10, TotalTokens: 110, ModelResolved: "claude-sonnet-5"},
		{Available: true, InputTokens: 200, FreshInputTokens: 60, CacheCreationInputTokens: 40, CacheReadInputTokens: 100,
			OutputTokens: 20, TotalTokens: 220, ModelResolved: "claude-sonnet-5"},
		{Available: true, InputTokens: 300, FreshInputTokens: 70, CacheCreationInputTokens: 130, CacheReadInputTokens: 100,
			OutputTokens: 30, TotalTokens: 330, ModelResolved: "claude-opus-5-5"},
	}
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			for _, u := range turns {
				u := u
				if req.OnEvent != nil {
					req.OnEvent(agentcli.Event{Kind: agentcli.EventUsage, Usage: &u})
				}
			}
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAsWithModel(context.Background(), "orchestrator", SessionRoleDriving,
		workitem.Item{ID: "sty_multi", Status: "in_progress"}, nil, nil, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (open, close): %#v", len(rows), rows)
	}
	closeRow := rows[1]
	wantIn, wantOut, wantTotal := 600, 60, 660
	wantFresh, wantCacheWrite, wantCacheRead := 180, 200, 220
	if closeRow["tokens_in"] != wantIn || closeRow["tokens_out"] != wantOut || closeRow["tokens_total"] != wantTotal {
		t.Fatalf("close row tokens = in=%v out=%v total=%v, want %d/%d/%d",
			closeRow["tokens_in"], closeRow["tokens_out"], closeRow["tokens_total"], wantIn, wantOut, wantTotal)
	}
	if closeRow["tokens_in_fresh"] != wantFresh || closeRow["tokens_cache_write"] != wantCacheWrite || closeRow["tokens_cache_read"] != wantCacheRead {
		t.Fatalf("close row cache split = fresh=%v write=%v read=%v, want %d/%d/%d",
			closeRow["tokens_in_fresh"], closeRow["tokens_cache_write"], closeRow["tokens_cache_read"],
			wantFresh, wantCacheWrite, wantCacheRead)
	}
	if closeRow["turns"] != 3 {
		t.Fatalf("close row turns = %v, want 3", closeRow["turns"])
	}
	// The latest turn's resolved model wins (sty_363eaf55) — model_usage.go's
	// primary-selection rule applies at parse time, not here.
	if closeRow["model_resolved"] != "claude-opus-5-5" {
		t.Fatalf("close row model_resolved = %v, want claude-opus-5-5 (last turn)", closeRow["model_resolved"])
	}
}
