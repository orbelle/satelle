package agentstep

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// modelEnvelope builds a `claude -p --output-format json` style envelope whose
// modelUsage names resolvedModel — the shape fakeRunner/mapRunner-style test
// doubles return so agentcli.UnwrapUsage (exercised by every reviewer/dispatch
// call, sty_87b86044) can parse both the decision AND the resolved model from
// one raw stdout string.
func modelEnvelope(t *testing.T, decision, resolvedModel string) string {
	t.Helper()
	inner, err := json.Marshal(map[string]any{"decision": decision})
	if err != nil {
		t.Fatal(err)
	}
	env, err := json.Marshal(map[string]any{
		"result": string(inner),
		"usage":  map[string]any{"input_tokens": 10, "output_tokens": 5},
		"modelUsage": map[string]any{
			resolvedModel: map[string]any{"inputTokens": 10, "outputTokens": 5, "costUSD": 0.01},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(env)
}

// modelEnvelopeText is modelEnvelope's counterpart for a plain-prose result
// (the summariser's own output shape) rather than a JSON decision.
func modelEnvelopeText(t *testing.T, text, resolvedModel string) string {
	t.Helper()
	env, err := json.Marshal(map[string]any{
		"result": text,
		"usage":  map[string]any{"input_tokens": 10, "output_tokens": 5},
		"modelUsage": map[string]any{
			resolvedModel: map[string]any{"inputTokens": 10, "outputTokens": 5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(env)
}

// TestGate_ModelResolved_CommandTransport pins AC1 at the engine level: a
// command-transport reviewer result whose modelUsage names a canonical id
// stores the configured alias (Model) and the resolved id (ModelResolved) in
// distinct fields on the decision AND on the synthesised reviewer verdict.
func TestGate_ModelResolved_CommandTransport(t *testing.T) {
	r := &fakeRunner{out: modelEnvelope(t, "accept", "claude-opus-5-5")}
	g := New(r, fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}, "/repo", "opus")
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || !dec.Accept {
		t.Fatalf("want gated accept, got %+v", dec)
	}
	if dec.Model != "opus" {
		t.Errorf("Model (alias) = %q, want opus", dec.Model)
	}
	if dec.ModelResolved != "claude-opus-5-5" {
		t.Errorf("ModelResolved = %q, want claude-opus-5-5", dec.ModelResolved)
	}
	if len(dec.Models) != 1 || dec.Models[0].ID != "claude-opus-5-5" || dec.Models[0].TokensOut != 5 {
		t.Errorf("Models = %+v", dec.Models)
	}
	if len(dec.Reviewers) != 1 || dec.Reviewers[0].ModelResolved != "claude-opus-5-5" {
		t.Fatalf("synthesised reviewer verdict missing ModelResolved: %+v", dec.Reviewers)
	}
}

// TestGate_ModelResolved_NeverEmpty pins the story's never-empty rule: a
// reviewer result with no modelUsage (a plain decision, the shape most test
// doubles and older harnesses return) still resolves to the explicit
// ModelUnavailable marker, never an empty string.
func TestGate_ModelResolved_NeverEmpty(t *testing.T) {
	g, _ := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true})
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if dec.ModelResolved != agentcli.ModelUnavailable {
		t.Errorf("ModelResolved = %q, want %q", dec.ModelResolved, agentcli.ModelUnavailable)
	}
	if len(dec.Reviewers) != 1 || dec.Reviewers[0].ModelResolved != agentcli.ModelUnavailable {
		t.Fatalf("reviewer verdict ModelResolved = %+v, want unavailable", dec.Reviewers)
	}
}

// modelMapRunner is mapRunner (engine_test.go:1058) with per-skill resolved
// models instead of per-skill decisions, so a multi-reviewer test can assert
// that EACH reviewer's own resolved model rides on its own verdict — not just
// the top-level pick.
type modelMapRunner struct {
	models map[string]string // review_skill -> resolved model id
	seen   []string
}

func (m *modelMapRunner) Name() string    { return "modelmap" }
func (m *modelMapRunner) Command() string { return "modelmap -p --append-system-prompt {system}" }
func (m *modelMapRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	var p struct {
		ReviewSkill string `json:"review_skill"`
	}
	_ = json.Unmarshal([]byte(req.Payload), &p)
	m.seen = append(m.seen, p.ReviewSkill)
	model := m.models[p.ReviewSkill]
	if model == "" {
		model = "claude-opus-5-5"
	}
	inner, _ := json.Marshal(map[string]any{"decision": "accept"})
	env, _ := json.Marshal(map[string]any{
		"result": string(inner),
		"usage":  map[string]any{"input_tokens": 1, "output_tokens": 1},
		"modelUsage": map[string]any{
			model: map[string]any{"inputTokens": 1, "outputTokens": 1},
		},
	})
	return env, nil
}

// TestGate_ModelResolved_SequentialMultiReviewer pins AC5's sequential gate
// path: each reviewer in a serial (parallel=0) multi-reviewer edge carries its
// OWN resolved model, not the first or last reviewer's.
func TestGate_ModelResolved_SequentialMultiReviewer(t *testing.T) {
	wf := spineWF("", "", "",
		"in_progress|executor",
		"done|||rev-a, rev-b|reviewer|0")
	mr := &modelMapRunner{models: map[string]string{"rev-a": "claude-opus-5-5", "rev-b": "claude-haiku-4-5"}}
	g := New(mr, fakeDocs{workflow: wf, skillBody: "rubric", skillFound: true}, "/repo", "")
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Reviewers) != 2 {
		t.Fatalf("want 2 reviewers, got %+v", dec.Reviewers)
	}
	if dec.Reviewers[0].ModelResolved != "claude-opus-5-5" {
		t.Errorf("rev-a ModelResolved = %q", dec.Reviewers[0].ModelResolved)
	}
	if dec.Reviewers[1].ModelResolved != "claude-haiku-4-5" {
		t.Errorf("rev-b ModelResolved = %q", dec.Reviewers[1].ModelResolved)
	}
	// Top-level mirrors the last-run reviewer (the serial-path convention for
	// every other top-level field).
	if dec.ModelResolved != "claude-haiku-4-5" {
		t.Errorf("top-level ModelResolved = %q, want claude-haiku-4-5 (last reviewer)", dec.ModelResolved)
	}
}

// TestGate_ModelResolved_ParallelMultiReviewer pins AC5's parallel gate path:
// each reviewer keeps its own resolved model, and the top-level pick (the last
// gated reviewer, since all accept) carries that reviewer's model too.
func TestGate_ModelResolved_ParallelMultiReviewer(t *testing.T) {
	wf := spineWF("", "", "",
		"in_progress|executor",
		"done|||rev-a, rev-b|reviewer|2")
	mr := &modelMapRunner{models: map[string]string{"rev-a": "claude-opus-5-5", "rev-b": "claude-haiku-4-5"}}
	g := New(mr, fakeDocs{workflow: wf, skillBody: "rubric", skillFound: true}, "/repo", "")
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Reviewers) != 2 {
		t.Fatalf("want 2 reviewers, got %+v", dec.Reviewers)
	}
	byModel := map[string]bool{}
	for _, rv := range dec.Reviewers {
		byModel[rv.ModelResolved] = true
	}
	if !byModel["claude-opus-5-5"] || !byModel["claude-haiku-4-5"] {
		t.Fatalf("want both resolved models represented across reviewers, got %+v", dec.Reviewers)
	}
	if dec.ModelResolved == "" {
		t.Error("top-level pick must carry a resolved model, not empty")
	}
}

// TestDispatchExecutor_ModelResolved pins AC5's dispatch-site coverage
// (engine.go's first DispatchResult construction, the coder/planner path): a
// named-agent dispatch's resolved model rides on the DispatchResult distinct
// from the configured alias.
func TestDispatchExecutor_ModelResolved(t *testing.T) {
	const plainDispatchSkill = `---
name: planning-rubric
type: skill
description: test rubric
---
Do the planning work.`
	wf := spineWF("", "", "",
		"plan|architect|planning-rubric",
		"in_progress|executor",
		"done")
	docs := fakeDocs{workflow: wf, skillBody: plainDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	r := &fakeRunner{out: modelEnvelopeText(t, "planning output", "claude-opus-5-5")}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Model: "opus", Tools: "read_file,grep,list_dir"}, true
	})
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_dispatch", Status: "backlog"}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "opus" {
		t.Errorf("Model (alias) = %q, want opus", res.Model)
	}
	if res.ModelResolved != "claude-opus-5-5" {
		t.Errorf("ModelResolved = %q, want claude-opus-5-5", res.ModelResolved)
	}
	if len(res.Models) != 1 || res.Models[0].ID != "claude-opus-5-5" {
		t.Errorf("Models = %+v", res.Models)
	}
}

func TestDispatchExecutor_RejectVerdictParks(t *testing.T) {
	const rubric = `---
name: ready-review
type: skill
description: test rubric
---
Judge premise.`
	wf := spineWF("", "", "",
		"ready|ready-reviewer|ready-review",
		"plan|executor",
		"done")
	docs := fakeDocs{workflow: wf, skillBody: rubric, skillFound: true}
	reject := &fakeRunner{out: modelEnvelopeText(t, `{"decision":"reject","notes":"body claims scratch.go:40 is 64 bytes; it is 67"}`, "claude-opus-5-5")}
	g, _ := newEngine(t, "", docs)
	g.newRunner = func(string, string) (agentcli.Runner, error) { return reject, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Model: "opus", Tools: "read_file,grep,list_dir"}, true
	})
	_, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_ready", Status: "backlog"}, "ready")
	var rej *verb.PerformerReject
	if !errors.As(err, &rej) {
		t.Fatalf("reject verdict must be a PerformerReject, got %v", err)
	}
	if !strings.Contains(rej.Notes, "scratch.go:40") {
		t.Fatalf("notes = %q", rej.Notes)
	}
}

// A performer whose output carries no decision object, or an accept, is not a
// reject: the step proceeds as before (sty_b8a0d062).
func TestDispatchExecutor_NonRejectOutputProceeds(t *testing.T) {
	const rubric = `---
name: ready-review
type: skill
description: test rubric
---
Judge premise.`
	for name, out := range map[string]string{
		"artifact only": `{"applied":"none","premise":"holds"}`,
		"accept":        `{"decision":"accept","notes":"ok"}`,
	} {
		t.Run(name, func(t *testing.T) {
			wf := spineWF("", "", "",
				"ready|ready-reviewer|ready-review",
				"plan|executor",
				"done")
			docs := fakeDocs{workflow: wf, skillBody: rubric, skillFound: true}
			g, _ := newEngine(t, "", docs)
			g.newRunner = func(string, string) (agentcli.Runner, error) {
				return &fakeRunner{out: modelEnvelopeText(t, out, "claude-opus-5-5")}, nil
			}
			g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
				return config.AgentBinding{Command: "fake -p {system}", Model: "opus", Tools: "read_file,grep,list_dir"}, true
			})
			_, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_ready", Status: "backlog"}, "ready")
			var rej *verb.PerformerReject
			if errors.As(err, &rej) {
				t.Fatalf("must not be a reject: %v", err)
			}
		})
	}
}

// envelopeAttempt wraps a valid artifact draft in a command-transport envelope
// carrying modelUsage — reusing attempt_test.go's validAttempt for the inner
// artifact shape, so recordArtifactAttempt's telemetry can be checked for the
// resolved model on an initial/repair/escalation attempt (AC5).
func envelopeAttempt(t *testing.T, body, resolvedModel string) string {
	t.Helper()
	env, err := json.Marshal(map[string]any{
		"result": validAttempt(body),
		"usage":  map[string]any{"input_tokens": 10, "output_tokens": 5},
		"modelUsage": map[string]any{
			resolvedModel: map[string]any{"inputTokens": 10, "outputTokens": 5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(env)
}

// TestArtifactAttempt_ModelResolved pins AC5's escalation/attempt telemetry:
// an attempt whose transport reports a resolved model carries model_resolved
// (and model_usage) directly on the agent-attempt telemetry payload.
func TestArtifactAttempt_ModelResolved(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{{out: envelopeAttempt(t, "## AC1\nok\n## AC2\nok", "claude-opus-5-5")}}}
	stronger := &attemptRunner{}
	g, events := attemptedEngine(t, attemptedDispatchSkill, primary, stronger)
	g.SetArtifactAttacher(func(_ context.Context, _ workitem.Item, name, typ, _ string) (string, string, error) {
		return name, typ, nil
	})
	if _, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan"); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %#v", *events)
	}
	data := (*events)[0].data
	if data["model_resolved"] != "claude-opus-5-5" {
		t.Errorf("model_resolved = %v, want claude-opus-5-5", data["model_resolved"])
	}
	// The attempt row must use the verb-owned, JSON-tagged shape ({id,
	// tokens_in, tokens_out, cost_usd}) that every other model_usage-carrying
	// row uses — not agentcli.ModelUsage, which has no JSON tags at all and
	// would serialize as {"ID","InputTokens","OutputTokens","CostUSD"}.
	models, ok := data["model_usage"].([]verb.ModelUsage)
	if !ok || len(models) != 1 || models[0].ID != "claude-opus-5-5" || models[0].TokensOut != 5 {
		t.Errorf("model_usage = %#v, want []verb.ModelUsage{{ID: claude-opus-5-5, TokensOut: 5}}", data["model_usage"])
	}
}

// TestArtifactAttempt_ModelResolved_Escalation extends
// TestArtifactAttemptsEscalatesOnlyAfterRepairFails with the resolved-model
// assertion: the escalation attempt (a different binding/model) carries ITS
// OWN resolved model, not the primary's.
func TestArtifactAttempt_ModelResolved_Escalation(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{
		{out: validAttempt("initial invalid")},
		{out: validAttempt("repair invalid")},
	}}
	stronger := &attemptRunner{runs: []attemptRun{{out: envelopeAttempt(t, "## AC1\nstrong\n## AC2\nstrong", "claude-opus-5-5")}}}
	g, events := attemptedEngine(t, attemptedDispatchSkill, primary, stronger)
	g.SetArtifactAttacher(func(_ context.Context, _ workitem.Item, name, typ, _ string) (string, string, error) {
		return name, typ, nil
	})
	if _, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan"); err != nil {
		t.Fatal(err)
	}
	// events[0]/[1] are the primary's initial+repair attempts, over plain
	// (non-envelope) output — never empty, normalized to unavailable.
	if (*events)[0].data["model_resolved"] != agentcli.ModelUnavailable {
		t.Errorf("initial attempt model_resolved = %v, want unavailable", (*events)[0].data["model_resolved"])
	}
	// events[2] is the escalation, which DID resolve a model.
	last := (*events)[2].data
	if last["phase"] != "escalate" || last["model_resolved"] != "claude-opus-5-5" {
		t.Fatalf("escalation event = %#v", last)
	}
}
