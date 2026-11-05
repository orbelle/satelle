package verb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

type summariserStub struct {
	out   string
	calls int
}

func (s *summariserStub) Summarise(context.Context, workitem.Item, string, string) (verb.SummaryResult, error) {
	s.calls++
	return verb.SummaryResult{Text: s.out}, nil
}
func (s *summariserStub) MandatorySummary(context.Context, workitem.Item) bool { return false }

// edgeSummariser returns a summary naming the edge, so a chain of transitions
// deposits distinguishable step-summary docs.
type edgeSummariser struct{}

func (edgeSummariser) Summarise(_ context.Context, _ workitem.Item, from, to string) (verb.SummaryResult, error) {
	return verb.SummaryResult{Text: "summary of " + from + " → " + to}, nil
}
func (edgeSummariser) MandatorySummary(context.Context, workitem.Item) bool { return false }

type stubGater struct {
	dec verb.GateDecision
}

func (g stubGater) Gate(context.Context, workitem.Item, string) (verb.GateDecision, error) {
	return g.dec, nil
}

func dispatchRaw(t *testing.T, name string, req any) (json.RawMessage, error) {
	t.Helper()
	b, _ := json.Marshal(req)
	return verb.Dispatch(context.Background(), name, b)
}

func TestStorySetGatedRejectBlocksTransition(t *testing.T) {
	withWiring(t)
	wire(t)
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{Gated: true, Accept: false, Notes: "no acceptance criteria", Skill: "satelle-story-done-review"}})

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it)

	_, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "done"})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected reject to block the transition, got err=%v", err)
	}

	var after workitem.Item
	json.Unmarshal(call(t, "story-get", map[string]any{"id": it.ID}), &after)
	if after.Status != "in_progress" {
		t.Errorf("status changed to %q despite reject — gate did not block", after.Status)
	}
}

func TestStorySetGatedAcceptEnacts(t *testing.T) {
	withWiring(t)
	wire(t)
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{Gated: true, Accept: true, Skill: "satelle-story-done-review"}})

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it)

	var after workitem.Item
	json.Unmarshal(call(t, "story-set", map[string]any{"id": it.ID, "status": "done"}), &after)
	if after.Status != "done" {
		t.Errorf("accept should enact: status = %q, want done", after.Status)
	}
}

type createStub struct {
	dec verb.GateDecision
}

func (c createStub) ReviewCreate(context.Context, verb.CreateDraft) (verb.GateDecision, error) {
	return c.dec, nil
}

func TestStoryCreateGatedRejectBlocksPersist(t *testing.T) {
	withWiring(t)
	wire(t)
	verb.SetCreateReviewer(createStub{dec: verb.GateDecision{Gated: true, Accept: false, Notes: "add numbered acceptance criteria", Skill: "satelle-story-review"}})

	_, err := dispatchRaw(t, "story-create", map[string]any{"title": "vague"})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected create to be rejected, got err=%v", err)
	}

	var items []workitem.Item
	json.Unmarshal(call(t, "story-list", map[string]any{}), &items)
	if len(items) != 0 {
		t.Errorf("rejected draft was persisted: %d items", len(items))
	}
}

func TestStoryCreateGatedAcceptPersists(t *testing.T) {
	withWiring(t)
	wire(t)
	verb.SetCreateReviewer(createStub{dec: verb.GateDecision{Gated: true, Accept: true}})

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "well formed", "acceptance_criteria": "1. works"}), &it)
	if it.ID == "" {
		t.Error("accepted draft should persist with an id")
	}
}

func TestStorySetUngatedTransitionEnacts(t *testing.T) {
	wire(t)
	// No gater wired — the gateless baseline: transitions enact directly.
	// Use DOT-legal statuses (sty_ebd3d666): backlog→in_progress→done.
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x"}), &it)

	var after workitem.Item
	json.Unmarshal(call(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"}), &after)
	json.Unmarshal(call(t, "story-set", map[string]any{"id": it.ID, "status": "done"}), &after)
	if after.Status != "done" {
		t.Errorf("ungated transition should enact: status = %q", after.Status)
	}
}

func TestGatedTransitionRecordsAgentInvocation(t *testing.T) {
	withWiring(t)
	wire(t)
	// An LLM reviewer carries its invocation: the resolved command and the injected
	// skill/rubric file. A functional check would leave Command empty (no agent).
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated: true, Accept: true, Skill: "satelle-story-done-review",
		Reviewers: []verb.ReviewerVerdict{{
			Skill:   "satelle-story-done-review",
			Accept:  true,
			Command: "claude -p --append-system-prompt {system}",
			Context: "satelle-story-done-review",
		}},
	}})

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it)
	call(t, "story-set", map[string]any{"id": it.ID, "status": "done"})

	var entries []ledger.Entry
	json.Unmarshal(call(t, "ledger-list", map[string]any{"story_id": it.ID, "kind": ledger.KindAgentInvocation}), &entries)
	if len(entries) != 1 {
		t.Fatalf("want one agent_invocation row, got %d: %+v", len(entries), entries)
	}
	e := entries[0]
	// AC: the entry names the agent and the injected skill/rubric file.
	if !strings.Contains(e.Body, "satelle-story-done-review") || !strings.Contains(e.Body, "claude") {
		t.Errorf("invocation body missing skill or command: %q", e.Body)
	}
	var p struct {
		Agent   string `json:"agent"`
		Command string `json:"command"`
		Context string `json:"context"`
	}
	json.Unmarshal(e.Payload, &p)
	if p.Agent != "reviewer" || p.Context != "satelle-story-done-review" || p.Command == "" {
		t.Errorf("invocation payload wrong: %+v", p)
	}
}

// TestMultiRejectNamesAllAndLedgersAll (sty_4f0a15db AC2): when Gate returns
// mixed accept/reject verdicts (parallel path), every row is ledgered and the
// refuse error names every rejecting reviewer.
func TestMultiRejectNamesAllAndLedgersAll(t *testing.T) {
	withWiring(t)
	wire(t)
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated:  true,
		Accept: false,
		Skill:  "rev-b",
		Reviewers: []verb.ReviewerVerdict{
			{Skill: "rev-a", Accept: true, Notes: "ok-a", Command: "fake", Context: "rev-a"},
			{Skill: "rev-b", Accept: false, Notes: "no-b", Command: "fake", Context: "rev-b"},
			{Skill: "rev-c", Accept: false, Notes: "no-c", Command: "fake", Context: "rev-c"},
		},
	}})

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "multi-reject", "status": "in_progress"}), &it)
	_, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "done"})
	if err == nil {
		t.Fatal("want refuse on multi-reject")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rev-b") || !strings.Contains(msg, "no-b") ||
		!strings.Contains(msg, "rev-c") || !strings.Contains(msg, "no-c") {
		t.Errorf("error must name both rejecters: %s", msg)
	}
	// All three verdicts ledgered: 1 accept + 2 reject
	var accepts []ledger.Entry
	json.Unmarshal(call(t, "ledger-list", map[string]any{"story_id": it.ID, "kind": ledger.KindReviewAccept}), &accepts)
	var rejects []ledger.Entry
	json.Unmarshal(call(t, "ledger-list", map[string]any{"story_id": it.ID, "kind": ledger.KindReviewReject}), &rejects)
	if len(accepts) < 1 {
		t.Errorf("want ≥1 accept ledger row, got %d", len(accepts))
	}
	if len(rejects) < 2 {
		t.Errorf("want ≥2 reject ledger rows, got %d", len(rejects))
	}
}

func TestFunctionalCheckRecordsNoInvocation(t *testing.T) {
	withWiring(t)
	wire(t)
	// A deterministic functional-check gate invokes no agent (Command empty), so no
	// agent_invocation row is written — only LLM agent invocations are recorded.
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated: true, Accept: true, Skill: "satelle-integration-check",
		Reviewers: []verb.ReviewerVerdict{{Skill: "satelle-integration-check", Accept: true}},
	}})

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it)
	call(t, "story-set", map[string]any{"id": it.ID, "status": "done"})

	var entries []ledger.Entry
	json.Unmarshal(call(t, "ledger-list", map[string]any{"story_id": it.ID, "kind": ledger.KindAgentInvocation}), &entries)
	if len(entries) != 0 {
		t.Errorf("functional check should record no agent_invocation, got %d", len(entries))
	}
}

// TestStorySetSurfacesDecisionNotesReasoning pins AC8 (sty_e21cbc08): reject
// errors carry decision=, notes=, and reasoning=; accept enacts with the same
// fields on the review_accept ledger body (stderr is the live session notice).
func TestStorySetSurfacesDecisionNotesReasoning(t *testing.T) {
	withWiring(t)
	wire(t)

	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated: true, Accept: false, Skill: "satelle-story-done-review",
		Notes: "missing AC", Reasoning: "body lacks numbered criteria",
	}})

	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": "in_progress"}), &it)
	_, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "done"})
	if err == nil {
		t.Fatal("expected reject")
	}
	msg := err.Error()
	for _, want := range []string{"decision=reject", "notes=missing AC", "reasoning=body lacks numbered criteria"} {
		if !strings.Contains(msg, want) {
			t.Errorf("reject error missing %q: %s", want, msg)
		}
	}

	// Accept path: status enacts; acceptBody format is covered by the reject shape
	// and the pure formatReasoningSuffix internal test in workitem_internal_test.
	// Park the first story first so the single-story seat does not block engage.
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated: true, Accept: true, Skill: "satelle-story-done-review",
		Notes: "close", Reasoning: "park",
	}})
	call(t, "story-set", map[string]any{"id": it.ID, "status": "done"})
	verb.SetTransitionGater(stubGater{dec: verb.GateDecision{
		Gated: true, Accept: true, Skill: "satelle-story-done-review",
		Notes: "all good", Reasoning: "ACs met in tree",
	}})
	var it2 workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "y", "status": "in_progress"}), &it2)
	var after workitem.Item
	json.Unmarshal(call(t, "story-set", map[string]any{"id": it2.ID, "status": "done"}), &after)
	if after.Status != "done" {
		t.Fatalf("accept should enact, status=%q", after.Status)
	}
	var accepts []ledger.Entry
	json.Unmarshal(call(t, "ledger-list", map[string]any{"story_id": it2.ID, "kind": ledger.KindReviewAccept}), &accepts)
	if len(accepts) == 0 {
		t.Fatal("expected a review_accept ledger row")
	}
	body := accepts[0].Body
	for _, want := range []string{"decision=accept", "notes=all good", "reasoning=ACs met in tree"} {
		if !strings.Contains(body, want) {
			t.Errorf("accept ledger body missing %q: %s", want, body)
		}
	}
}
