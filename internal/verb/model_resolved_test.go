package verb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentartifact"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestReviewVerdictRowsCarryResolvedModel pins AC5: a review_accept and a
// review_reject row each carry model_resolved/model_source (and model_usage
// when present) DIRECTLY on the row's own payload — reviewerPayload
// (workitem.go:1395) — with no join to an agent_invocation entry, so the row
// stands alone after ledger compaction (sty_87b86044 / sty_7069bced). This is
// the single-reviewer path (no dec.Reviewers): GateDecision.ModelSource must
// survive the synthesis into a ReviewerVerdict before it reaches the row.
func TestReviewVerdictRowsCarryResolvedModel(t *testing.T) {
	db := wire(t)

	cases := []struct {
		name   string
		accept bool
		kind   string
	}{
		{"accept", true, ledger.KindReviewAccept},
		{"reject", false, ledger.KindReviewReject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withWiring(t)
			verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
				Gated: true, Accept: tc.accept, Skill: "satelle-story-done-review", Notes: "n",
				ModelResolved: "claude-opus-5-5", ModelSource: "inherited-in-loop",
				Models: []verb.ModelUsage{{ID: "claude-opus-5-5", TokensIn: 10, TokensOut: 5}},
			}})

			var it workitem.Item
			if err := json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it); err != nil {
				t.Fatal(err)
			}
			_, _ = dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "done"})

			entries, err := db.Ledger.ListByStory(context.Background(), it.ID, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("want 1 %s row, got %d: %+v", tc.kind, len(entries), entries)
			}
			var row struct {
				ModelResolved string            `json:"model_resolved"`
				ModelSource   string            `json:"model_source"`
				Models        []verb.ModelUsage `json:"model_usage"`
			}
			if err := json.Unmarshal(entries[0].Payload, &row); err != nil {
				t.Fatal(err)
			}
			if row.ModelResolved != "claude-opus-5-5" {
				t.Errorf("model_resolved = %q, want claude-opus-5-5", row.ModelResolved)
			}
			if row.ModelSource != "inherited-in-loop" {
				t.Errorf("model_source = %q, want inherited-in-loop", row.ModelSource)
			}
			if len(row.Models) != 1 || row.Models[0].ID != "claude-opus-5-5" || row.Models[0].TokensOut != 5 {
				t.Errorf("model_usage = %+v", row.Models)
			}
			// The row stands alone: ledger.EventTelemetry reads model_resolved and
			// model_source off THIS row's own payload, without consulting any
			// agent_invocation entry.
			tel := ledger.EventTelemetry(entries[0])
			if tel.ModelResolved != "claude-opus-5-5" {
				t.Errorf("EventTelemetry.ModelResolved = %q, want claude-opus-5-5", tel.ModelResolved)
			}
			if tel.ModelSource != "inherited-in-loop" {
				t.Errorf("EventTelemetry.ModelSource = %q, want inherited-in-loop", tel.ModelSource)
			}
		})
	}
}

// TestGateInvocationRowCarriesModelSource pins the reviewer's OWN
// agent_invocation row (invocationPayload, workitem.go:1523 — Agent
// "reviewer", written whenever a verdict carries a Command): model_source
// round-trips through ledger.EventTelemetry the same as model_resolved does,
// off the row invocationPayload stamps, not the review_accept row
// reviewerPayload stamps (two different payload writers, sty_7069bced AC5).
func TestGateInvocationRowCarriesModelSource(t *testing.T) {
	withWiring(t)
	db := wire(t)
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated: true, Accept: true, Skill: "satelle-story-done-review", Notes: "n",
		Command: "claude -p", Context: "satelle-story-done-review",
		ModelResolved: "claude-opus-5-5", ModelSource: "binding",
	}})

	var it workitem.Item
	if err := json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it); err != nil {
		t.Fatal(err)
	}
	_, _ = dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "done"})

	entries, err := db.Ledger.ListByStory(context.Background(), it.ID, ledger.KindAgentInvocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 agent_invocation row, got %d: %+v", len(entries), entries)
	}
	if entries[0].Actor != "reviewer" {
		t.Fatalf("actor = %q, want reviewer", entries[0].Actor)
	}
	tel := ledger.EventTelemetry(entries[0])
	if tel.ModelResolved != "claude-opus-5-5" {
		t.Errorf("EventTelemetry.ModelResolved = %q, want claude-opus-5-5", tel.ModelResolved)
	}
	if tel.ModelSource != "binding" {
		t.Errorf("EventTelemetry.ModelSource = %q, want binding", tel.ModelSource)
	}
}

// TestGateInvocationRowCarriesPromptByteCounts pins sty_363eaf55 AC3: the
// reviewer's own agent_invocation row (invocationPayload) records the byte
// LENGTHS of the system prompt and payload satelle sent — never the text
// itself — so the ledger row is auditable without ever storing a prompt.
func TestGateInvocationRowCarriesPromptByteCounts(t *testing.T) {
	withWiring(t)
	db := wire(t)
	const fakeSystemPrompt = "## You are an isolated satelle reviewer — judge only, never mutate"
	const fakePayload = `{"story":{"id":"sty_1"},"from":"in_progress","to":"done"}`
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated: true, Accept: true, Skill: "satelle-story-done-review", Notes: "n",
		Command: "claude -p", Context: "satelle-story-done-review",
		SystemPromptBytes: len(fakeSystemPrompt), PayloadBytes: len(fakePayload),
	}})

	var it workitem.Item
	if err := json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it); err != nil {
		t.Fatal(err)
	}
	_, _ = dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "done"})

	entries, err := db.Ledger.ListByStory(context.Background(), it.ID, ledger.KindAgentInvocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 agent_invocation row, got %d: %+v", len(entries), entries)
	}
	if strings.Contains(string(entries[0].Payload), fakeSystemPrompt) || strings.Contains(string(entries[0].Payload), fakePayload) {
		t.Fatalf("ledger row must never carry prompt/payload TEXT, only byte counts: %s", entries[0].Payload)
	}
	var row struct {
		SystemPromptBytes int `json:"system_prompt_bytes"`
		PayloadBytes      int `json:"payload_bytes"`
	}
	if err := json.Unmarshal(entries[0].Payload, &row); err != nil {
		t.Fatal(err)
	}
	if row.SystemPromptBytes != len(fakeSystemPrompt) {
		t.Errorf("system_prompt_bytes = %d, want %d", row.SystemPromptBytes, len(fakeSystemPrompt))
	}
	if row.PayloadBytes != len(fakePayload) {
		t.Errorf("payload_bytes = %d, want %d", row.PayloadBytes, len(fakePayload))
	}
}

// TestDispatchInvocationRowCarriesModelSource pins a named-executor dispatch's
// agent_invocation row (dispatchPayload, workitem.go:1463 — Actor "executor"):
// model_source round-trips through a real ledger.Store and
// ledger.EventTelemetry the same as the reviewer/summariser rows do
// (sty_7069bced AC5).
func TestDispatchInvocationRowCarriesModelSource(t *testing.T) {
	withWiring(t)
	db := wire(t)
	d := &dispatcherStub{res: verb.DispatchResult{
		Dispatched: true, Agent: "architect", Command: "fake {system}",
		Model: "m", ModelResolved: "claude-sonnet-5", ModelSource: "step",
	}}
	verb.SetExecutorDispatcher(d)

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "backlog"}), &it)
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"}); err != nil {
		t.Fatalf("dispatch success must enact: %v", err)
	}

	entries, err := db.Ledger.ListByStory(context.Background(), it.ID, ledger.KindAgentInvocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 agent_invocation row, got %d: %+v", len(entries), entries)
	}
	if entries[0].Actor != "executor" {
		t.Fatalf("actor = %q, want executor", entries[0].Actor)
	}
	tel := ledger.EventTelemetry(entries[0])
	if tel.ModelResolved != "claude-sonnet-5" {
		t.Errorf("EventTelemetry.ModelResolved = %q, want claude-sonnet-5", tel.ModelResolved)
	}
	if tel.ModelSource != "step" {
		t.Errorf("EventTelemetry.ModelSource = %q, want step", tel.ModelSource)
	}
}

// TestDispatchInvocationRowCarriesPromptByteCounts pins sty_363eaf55 AC3: a
// named-executor dispatch's agent_invocation row (dispatchPayload,
// workitem.go:1465 — Actor "executor") records the byte LENGTHS of the system
// prompt and payload satelle sent — never the text itself.
func TestDispatchInvocationRowCarriesPromptByteCounts(t *testing.T) {
	withWiring(t)
	db := wire(t)
	const fakeSystemPrompt = "## You are architect, dispatched to plan this story"
	const fakePayload = `{"story":{"id":"sty_1"},"from":"backlog","to":"in_progress"}`
	d := &dispatcherStub{res: verb.DispatchResult{
		Dispatched: true, Agent: "architect", Command: "fake {system}",
		SystemPromptBytes: len(fakeSystemPrompt), PayloadBytes: len(fakePayload),
	}}
	verb.SetExecutorDispatcher(d)

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "backlog"}), &it)
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"}); err != nil {
		t.Fatalf("dispatch success must enact: %v", err)
	}

	entries, err := db.Ledger.ListByStory(context.Background(), it.ID, ledger.KindAgentInvocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 agent_invocation row, got %d: %+v", len(entries), entries)
	}
	if strings.Contains(string(entries[0].Payload), fakeSystemPrompt) || strings.Contains(string(entries[0].Payload), fakePayload) {
		t.Fatalf("ledger row must never carry prompt/payload TEXT, only byte counts: %s", entries[0].Payload)
	}
	var row struct {
		SystemPromptBytes int `json:"system_prompt_bytes"`
		PayloadBytes      int `json:"payload_bytes"`
	}
	if err := json.Unmarshal(entries[0].Payload, &row); err != nil {
		t.Fatal(err)
	}
	if row.SystemPromptBytes != len(fakeSystemPrompt) {
		t.Errorf("system_prompt_bytes = %d, want %d", row.SystemPromptBytes, len(fakeSystemPrompt))
	}
	if row.PayloadBytes != len(fakePayload) {
		t.Errorf("payload_bytes = %d, want %d", row.PayloadBytes, len(fakePayload))
	}
}

// modelSourceSummariser is a StepSummariser whose result carries its own
// billable invocation (Command set) plus a ModelSource, so recordStepSummary
// takes the branch that writes an agent_invocation row via
// summariserInvocationPayload (workitem.go:1555).
type modelSourceSummariser struct{}

func (modelSourceSummariser) Summarise(context.Context, workitem.Item, string, string) (verb.SummaryResult, error) {
	return verb.SummaryResult{
		Text: "the recap", Command: "claude -p", Context: "satelle-step-summary",
		Model: "sonnet", ModelResolved: "claude-sonnet-5", ModelSource: "creator",
		UsageAvailable: true,
	}, nil
}
func (modelSourceSummariser) MandatorySummary(context.Context, workitem.Item) bool { return true }

// byteSummariser is a StepSummariser whose result carries a billable
// invocation with known system-prompt/payload byte counts.
type byteSummariser struct {
	systemPromptBytes, payloadBytes int
}

func (b byteSummariser) Summarise(context.Context, workitem.Item, string, string) (verb.SummaryResult, error) {
	return verb.SummaryResult{
		Text: "the recap", Command: "claude -p", Context: "satelle-step-summary",
		SystemPromptBytes: b.systemPromptBytes, PayloadBytes: b.payloadBytes,
	}, nil
}
func (byteSummariser) MandatorySummary(context.Context, workitem.Item) bool { return true }

// TestReviewVerdictRowCarriesModelCacheSplit pins reviewerPayload's per-model
// cache split (sty_363eaf55): the single-reviewer synthesis at workitem.go
// (workItemSet) copies dec.Models onto the synthesised ReviewerVerdict, and
// reviewerPayload copies that verdict's Models onto the review_accept row —
// a model's TokensCacheWrite/TokensCacheRead must survive both hops, not just
// its plain TokensOut.
func TestReviewVerdictRowCarriesModelCacheSplit(t *testing.T) {
	withWiring(t)
	db := wire(t)
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated: true, Accept: true, Skill: "satelle-story-done-review", Notes: "n",
		ModelResolved: "claude-opus-5-5", ModelSource: "inherited-in-loop",
		Models: []verb.ModelUsage{{ID: "claude-opus-5-5", TokensIn: 41, TokensOut: 5, TokensCacheWrite: 17, TokensCacheRead: 23}},
	}})

	var it workitem.Item
	if err := json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it); err != nil {
		t.Fatal(err)
	}
	_, _ = dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "done"})

	entries, err := db.Ledger.ListByStory(context.Background(), it.ID, ledger.KindReviewAccept)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 review_accept row, got %d: %+v", len(entries), entries)
	}
	var row struct {
		Models []verb.ModelUsage `json:"model_usage"`
	}
	if err := json.Unmarshal(entries[0].Payload, &row); err != nil {
		t.Fatal(err)
	}
	if len(row.Models) != 1 || row.Models[0].TokensCacheWrite != 17 || row.Models[0].TokensCacheRead != 23 {
		t.Fatalf("model_usage cache split = %+v, want write=17 read=23", row.Models)
	}
}

// TestGateInvocationRowCarriesCacheSplit pins invocationPayload's cache split
// (sty_363eaf55): the reviewer's own agent_invocation row must carry
// tokens_in_fresh/tokens_cache_write/tokens_cache_read — both the top-level
// split (copied from the single-reviewer GateDecision synthesis) and the
// per-model split.
func TestGateInvocationRowCarriesCacheSplit(t *testing.T) {
	withWiring(t)
	db := wire(t)
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated: true, Accept: true, Skill: "satelle-story-done-review", Notes: "n",
		Command: "claude -p", Context: "satelle-story-done-review",
		TokensInFresh: 41, TokensCacheWrite: 17, TokensCacheRead: 23,
		Models: []verb.ModelUsage{{ID: "claude-opus-5-5", TokensCacheWrite: 17, TokensCacheRead: 23}},
	}})

	var it workitem.Item
	if err := json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it); err != nil {
		t.Fatal(err)
	}
	_, _ = dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "done"})

	entries, err := db.Ledger.ListByStory(context.Background(), it.ID, ledger.KindAgentInvocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 agent_invocation row, got %d: %+v", len(entries), entries)
	}
	var row struct {
		TokensInFresh    int               `json:"tokens_in_fresh"`
		TokensCacheWrite int               `json:"tokens_cache_write"`
		TokensCacheRead  int               `json:"tokens_cache_read"`
		Models           []verb.ModelUsage `json:"model_usage"`
	}
	if err := json.Unmarshal(entries[0].Payload, &row); err != nil {
		t.Fatal(err)
	}
	if row.TokensInFresh != 41 || row.TokensCacheWrite != 17 || row.TokensCacheRead != 23 {
		t.Fatalf("cache split = fresh=%d write=%d read=%d, want 41/17/23", row.TokensInFresh, row.TokensCacheWrite, row.TokensCacheRead)
	}
	if len(row.Models) != 1 || row.Models[0].TokensCacheWrite != 17 || row.Models[0].TokensCacheRead != 23 {
		t.Fatalf("model_usage cache split = %+v, want write=17 read=23", row.Models)
	}
}

// TestDispatchInvocationRowCarriesCacheSplit pins dispatchPayload's cache
// split (sty_363eaf55): a named-executor dispatch's agent_invocation row must
// carry tokens_in_fresh/tokens_cache_write/tokens_cache_read, top-level and
// per-model.
func TestDispatchInvocationRowCarriesCacheSplit(t *testing.T) {
	withWiring(t)
	db := wire(t)
	d := &dispatcherStub{res: verb.DispatchResult{
		Dispatched: true, Agent: "architect", Command: "fake {system}",
		TokensInFresh: 41, TokensCacheWrite: 17, TokensCacheRead: 23,
		Models: []verb.ModelUsage{{ID: "claude-sonnet-5", TokensCacheWrite: 17, TokensCacheRead: 23}},
	}}
	verb.SetExecutorDispatcher(d)

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "backlog"}), &it)
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"}); err != nil {
		t.Fatalf("dispatch success must enact: %v", err)
	}

	entries, err := db.Ledger.ListByStory(context.Background(), it.ID, ledger.KindAgentInvocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 agent_invocation row, got %d: %+v", len(entries), entries)
	}
	var row struct {
		TokensInFresh    int               `json:"tokens_in_fresh"`
		TokensCacheWrite int               `json:"tokens_cache_write"`
		TokensCacheRead  int               `json:"tokens_cache_read"`
		Models           []verb.ModelUsage `json:"model_usage"`
	}
	if err := json.Unmarshal(entries[0].Payload, &row); err != nil {
		t.Fatal(err)
	}
	if row.TokensInFresh != 41 || row.TokensCacheWrite != 17 || row.TokensCacheRead != 23 {
		t.Fatalf("cache split = fresh=%d write=%d read=%d, want 41/17/23", row.TokensInFresh, row.TokensCacheWrite, row.TokensCacheRead)
	}
	if len(row.Models) != 1 || row.Models[0].TokensCacheWrite != 17 || row.Models[0].TokensCacheRead != 23 {
		t.Fatalf("model_usage cache split = %+v, want write=17 read=23", row.Models)
	}
}

// TestDispatchPayloadCarriesFindingAnswers pins that a named-executor dispatch's
// agent_invocation row records the findings the performer was offered and its
// per-finding answers (sty_f948d345), which the gate payload reads back.
func TestDispatchPayloadCarriesFindingAnswers(t *testing.T) {
	withWiring(t)
	db := wire(t)
	d := &dispatcherStub{res: verb.DispatchResult{
		Dispatched: true, Agent: "planner", Command: "fake {system}",
		FindingsOffered: []string{"arch#1", "plan-review#1"},
		Answers:         []agentartifact.Answer{{Finding: "arch#1", Answer: agentartifact.AnswerRebutted, Evidence: "engine.go:1336"}},
	}}
	verb.SetExecutorDispatcher(d)

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "backlog"}), &it)
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"}); err != nil {
		t.Fatalf("dispatch success must enact: %v", err)
	}

	entries, err := db.Ledger.ListByStory(context.Background(), it.ID, ledger.KindAgentInvocation)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want 1 agent_invocation row, got %d err=%v", len(entries), err)
	}
	var row struct {
		FindingsOffered []string               `json:"findings_offered"`
		FindingAnswers  []agentartifact.Answer `json:"finding_answers"`
	}
	if err := json.Unmarshal(entries[0].Payload, &row); err != nil {
		t.Fatal(err)
	}
	if len(row.FindingsOffered) != 2 || row.FindingsOffered[1] != "plan-review#1" {
		t.Errorf("findings_offered = %v", row.FindingsOffered)
	}
	if len(row.FindingAnswers) != 1 || row.FindingAnswers[0].Finding != "arch#1" || row.FindingAnswers[0].Answer != "rebutted" {
		t.Errorf("finding_answers = %+v", row.FindingAnswers)
	}
}

// cacheSplitSummariser is a StepSummariser whose result carries a known
// cache split, for TestSummariserInvocationRowCarriesCacheSplit.
type cacheSplitSummariser struct{}

func (cacheSplitSummariser) Summarise(context.Context, workitem.Item, string, string) (verb.SummaryResult, error) {
	return verb.SummaryResult{
		Text: "the recap", Command: "claude -p", Context: "satelle-step-summary",
		TokensInFresh: 41, TokensCacheWrite: 17, TokensCacheRead: 23,
		Models: []verb.ModelUsage{{ID: "claude-sonnet-5", TokensCacheWrite: 17, TokensCacheRead: 23}},
	}, nil
}
func (cacheSplitSummariser) MandatorySummary(context.Context, workitem.Item) bool { return true }

// TestReviewVerdictRowCarriesAliasWhenUnresolved pins AC6: when the gate
// reports a configured alias but no resolved id, review_accept/review_reject
// rows still carry the alias (reviewerPayload's "model" field, workitem.go),
// so ledger.ModelLabel renders "<alias> (unknown)" rather than a bare
// "unknown" — the web chip must not lose the alias just because resolution
// failed (sty_87b86044).
func TestReviewVerdictRowCarriesAliasWhenUnresolved(t *testing.T) {
	db := wire(t)

	cases := []struct {
		name   string
		accept bool
		kind   string
	}{
		{"accept", true, ledger.KindReviewAccept},
		{"reject", false, ledger.KindReviewReject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withWiring(t)
			verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
				Gated: true, Accept: tc.accept, Skill: "satelle-story-done-review", Notes: "n",
				Model: "opus",
			}})

			var it workitem.Item
			if err := json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it); err != nil {
				t.Fatal(err)
			}
			_, _ = dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "done"})

			entries, err := db.Ledger.ListByStory(context.Background(), it.ID, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("want 1 %s row, got %d: %+v", tc.kind, len(entries), entries)
			}
			var row struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(entries[0].Payload, &row); err != nil {
				t.Fatal(err)
			}
			if row.Model != "opus" {
				t.Errorf("model = %q, want opus", row.Model)
			}
			tel := ledger.EventTelemetry(entries[0])
			if got := ledger.ModelLabel(tel.Model, tel.ModelResolved); got != "opus (unknown)" {
				t.Errorf("ModelLabel = %q, want %q", got, "opus (unknown)")
			}
		})
	}
}

// TestReviewVerdictRowLegacyRendersUnknown pins AC7 for the verdict-row
// surface: a review_accept row written before this change (no model_resolved
// key at all) decodes without error and renders "unknown" — never
// back-filled, never an error.
func TestReviewVerdictRowLegacyRendersUnknown(t *testing.T) {
	legacy := ledger.Entry{
		Kind:    ledger.KindReviewAccept,
		Payload: json.RawMessage(`{"from":"plan","to":"in_progress","skill":"satelle-story-plan-review","accept":true}`),
	}
	tel := ledger.EventTelemetry(legacy)
	if tel.ModelResolved != "" {
		t.Fatalf("legacy row should decode with ModelResolved empty, got %q", tel.ModelResolved)
	}
	if got := ledger.ModelLabel(tel.Model, tel.ModelResolved); got != "unknown" {
		t.Errorf("ModelLabel(legacy) = %q, want unknown", got)
	}
	// Byte-identical: rendering must never rewrite the stored row.
	want := `{"from":"plan","to":"in_progress","skill":"satelle-story-plan-review","accept":true}`
	if string(legacy.Payload) != want {
		t.Fatalf("legacy payload mutated: %s", legacy.Payload)
	}
}
