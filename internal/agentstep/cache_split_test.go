package agentstep

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// cacheEnvelope builds a claude JSON envelope carrying a full fresh/cache-write/
// cache-read split on both the top-level usage and a single model's modelUsage
// entry (sty_363eaf55) — so a test can drive the split through every ONE-SHOT
// copy line between the raw transport bytes and the value a caller reads back
// (setDecisionUsage, the Gate/DispatchExecutor/Retrospect/Summarise results,
// and toVerbModels' per-model mapping), each with its own non-overlapping
// number so deleting any single copy line mismatches.
func cacheEnvelope(t *testing.T, resultText, resolvedModel string, fresh, write, read int) string {
	t.Helper()
	env, err := json.Marshal(map[string]any{
		"result": resultText,
		"usage": map[string]any{
			"input_tokens": fresh, "output_tokens": 5,
			"cache_creation_input_tokens": write, "cache_read_input_tokens": read,
		},
		"modelUsage": map[string]any{
			resolvedModel: map[string]any{
				"inputTokens": fresh, "outputTokens": 5,
				"cacheCreationInputTokens": write, "cacheReadInputTokens": read,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(env)
}

// TestSetDecisionUsageCopiesCacheSplit pins the ONE-SHOT copy inside
// setDecisionUsage (engine.go): a caller handed a UsageResult with a
// fresh/cache-write/cache-read split must see that split on the returned
// GateDecision, not just the pre-existing InputTokens total.
func TestSetDecisionUsageCopiesCacheSplit(t *testing.T) {
	g := New(&fakeRunner{}, fakeDocs{}, "/repo", "engine-default")
	u := agentcli.UsageResult{
		Available: true, InputTokens: 100, OutputTokens: 10, TotalTokens: 110,
		FreshInputTokens: 55, CacheCreationInputTokens: 33, CacheReadInputTokens: 12,
	}
	var dec verb.GateDecision
	g.setDecisionUsage(&dec, u, "claude-sonnet-5")
	if dec.TokensInFresh != 55 || dec.TokensCacheWrite != 33 || dec.TokensCacheRead != 12 {
		t.Fatalf("cache split = fresh=%d write=%d read=%d, want 55/33/12",
			dec.TokensInFresh, dec.TokensCacheWrite, dec.TokensCacheRead)
	}
}

// TestToVerbModelsCopiesPerModelCacheSplit pins toVerbModels' per-model
// mapping (engine.go): agentcli.ModelUsage's CacheCreationInputTokens/
// CacheReadInputTokens must land on verb.ModelUsage's TokensCacheWrite/
// TokensCacheRead, distinct from that model's plain InputTokens/OutputTokens.
func TestToVerbModelsCopiesPerModelCacheSplit(t *testing.T) {
	u := agentcli.UsageResult{
		ModelResolved: "claude-sonnet-5",
		Models: []agentcli.ModelUsage{
			{ID: "claude-sonnet-5", InputTokens: 10, OutputTokens: 5, CacheCreationInputTokens: 7, CacheReadInputTokens: 3},
		},
	}
	_, models := toVerbModels(u)
	if len(models) != 1 || models[0].TokensCacheWrite != 7 || models[0].TokensCacheRead != 3 {
		t.Fatalf("models = %+v, want one entry with TokensCacheWrite=7 TokensCacheRead=3", models)
	}
}

// TestGateCarriesCacheSplit pins the Gate result end-to-end: a reviewer
// transport reporting a cache split must have that split survive onto the
// returned GateDecision (both the top-level split and the per-model split).
func TestGateCarriesCacheSplit(t *testing.T) {
	inner, err := json.Marshal(map[string]any{"decision": "accept"})
	if err != nil {
		t.Fatal(err)
	}
	env := cacheEnvelope(t, string(inner), "claude-opus-5-5", 41, 17, 23)
	r := &fakeRunner{out: env}
	g := New(r, fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}, "/repo", "opus")
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if dec.TokensInFresh != 41 || dec.TokensCacheWrite != 17 || dec.TokensCacheRead != 23 {
		t.Fatalf("cache split = fresh=%d write=%d read=%d, want 41/17/23",
			dec.TokensInFresh, dec.TokensCacheWrite, dec.TokensCacheRead)
	}
	if len(dec.Models) != 1 || dec.Models[0].TokensCacheWrite != 17 || dec.Models[0].TokensCacheRead != 23 {
		t.Fatalf("per-model cache split = %+v, want write=17 read=23", dec.Models)
	}
	if len(dec.Reviewers) != 1 || dec.Reviewers[0].TokensCacheWrite != 17 || dec.Reviewers[0].TokensCacheRead != 23 {
		t.Fatalf("synthesised reviewer verdict cache split = %+v, want write=17 read=23", dec.Reviewers)
	}
}

// TestDispatchExecutorCarriesCacheSplit pins DispatchExecutor's result:
// a dispatch's cache split rides on the returned DispatchResult, top-level
// and per-model.
func TestDispatchExecutorCarriesCacheSplit(t *testing.T) {
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
	env := cacheEnvelope(t, "planning output", "claude-opus-5-5", 41, 17, 23)
	r := &fakeRunner{out: env}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Model: "opus", Tools: "read_file,grep,list_dir"}, true
	})
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_dispatch", Status: "backlog"}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if res.TokensInFresh != 41 || res.TokensCacheWrite != 17 || res.TokensCacheRead != 23 {
		t.Fatalf("cache split = fresh=%d write=%d read=%d, want 41/17/23",
			res.TokensInFresh, res.TokensCacheWrite, res.TokensCacheRead)
	}
	if len(res.Models) != 1 || res.Models[0].TokensCacheWrite != 17 || res.Models[0].TokensCacheRead != 23 {
		t.Fatalf("per-model cache split = %+v, want write=17 read=23", res.Models)
	}
}

// envelopeAttemptCache is envelopeAttempt (model_resolved_test.go) with a
// fresh/cache-write/cache-read split on the top-level usage, so an attempt
// test can pin recordArtifactAttempt's own copy of the split (attempt.go)
// distinctly from the plain input/output totals.
func envelopeAttemptCache(t *testing.T, body, resolvedModel string, fresh, write, read int) string {
	t.Helper()
	env, err := json.Marshal(map[string]any{
		"result": validAttempt(body),
		"usage": map[string]any{
			"input_tokens": fresh, "output_tokens": 5,
			"cache_creation_input_tokens": write, "cache_read_input_tokens": read,
		},
		"modelUsage": map[string]any{
			resolvedModel: map[string]any{"inputTokens": fresh, "outputTokens": 5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(env)
}

// TestArtifactAttemptCacheSplitOnRow pins recordArtifactAttempt's own
// telemetry copy of the cache split (attempt.go): a single valid attempt's
// agent-attempt row must carry tokens_in_fresh/tokens_cache_write/
// tokens_cache_read matching the transport's reported split.
func TestArtifactAttemptCacheSplitOnRow(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{
		{out: envelopeAttemptCache(t, "## AC1\nok\n## AC2\nok", "claude-opus-5-5", 41, 17, 23)},
	}}
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
	fresh, _ := data["tokens_in_fresh"].(int)
	write, _ := data["tokens_cache_write"].(int)
	read, _ := data["tokens_cache_read"].(int)
	if fresh != 41 || write != 17 || read != 23 {
		t.Fatalf("attempt row cache split = fresh=%v write=%v read=%v, want 41/17/23",
			data["tokens_in_fresh"], data["tokens_cache_write"], data["tokens_cache_read"])
	}
}

// TestArtifactAttemptCacheSplitSumsAcrossAttempts pins attempt.go's totalUsage
// accumulation: an initial attempt that fails validation (triggering a
// repair) and a repair that succeeds must have their cache splits SUMMED —
// not overwritten — on the final DispatchResult (which flows through
// runArtifactAttempts' returned usage into DispatchExecutor's construction,
// engine.go).
func TestArtifactAttemptCacheSplitSumsAcrossAttempts(t *testing.T) {
	primary := &attemptRunner{runs: []attemptRun{
		{out: envelopeAttemptCache(t, "draft", "claude-opus-5-5", 10, 5, 3)},                        // missing AC2 -> repair
		{out: envelopeAttemptCache(t, "## AC1\nfixed\n## AC2\nfixed", "claude-opus-5-5", 20, 8, 4)}, // repair succeeds
	}}
	stronger := &attemptRunner{}
	g, events := attemptedEngine(t, attemptedDispatchSkill, primary, stronger)
	g.SetArtifactAttacher(func(_ context.Context, _ workitem.Item, name, typ, _ string) (string, string, error) {
		return name, typ, nil
	})
	res, err := g.DispatchExecutor(context.Background(), attemptedItem(), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(*events) != 2 {
		t.Fatalf("events = %#v", *events)
	}
	const wantFresh, wantWrite, wantRead = 30, 13, 7
	if res.TokensInFresh != wantFresh || res.TokensCacheWrite != wantWrite || res.TokensCacheRead != wantRead {
		t.Fatalf("summed cache split = fresh=%d write=%d read=%d, want %d/%d/%d",
			res.TokensInFresh, res.TokensCacheWrite, res.TokensCacheRead, wantFresh, wantWrite, wantRead)
	}
}
