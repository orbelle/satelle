package verb_test

import (
	"context"
	"encoding/json"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
	"testing"
)

// sty_c5171eb9: a lifecycle-hook reviewer run (create_review, amend_review) is
// recorded on the ledger like an edge gate's — an agent_invocation row carrying
// executable, model, usage (or its adapter-named unavailable) and duration, then
// the review verdict row with the reviewer's notes. A rejected create persists no
// story, so its rows are repo-scoped (empty story id).

const (
	createReviewSkill = "satelle-story-create-review"
	amendReviewSkill  = "satelle-story-amend-review"
)

// lifecycleDecision is what the engine returns for a declared hook that ran an
// agent: command, model and measured usage. cost is the provider's own figure.
func lifecycleDecision(skill string, accept bool, notes string) verb.GateDecision {
	cost := 0.25
	return verb.GateDecision{
		Gated: true, Accept: accept, Skill: skill, Notes: notes,
		Command: "claude -p", Context: skill, Model: "opus", ModelResolved: "claude-opus-5-5",
		TokensIn: 1200, TokensOut: 300, TokensTotal: 1500, DurationMs: 4200,
		UsageAvailable: true, TokensInFresh: 200, TokensCacheRead: 1000,
		UsageNote: verb.UsageNote{Adapter: "claude"},
		CostUSD:   &cost,
	}
}

func entriesOf(t *testing.T, db *store.DB, storyID, kind string) []ledger.Entry {
	t.Helper()
	var out []ledger.Entry
	if err := db.Ledger.ForEachKind(context.Background(), storyID, kind, func(e ledger.Entry) error {
		out = append(out, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeJSON(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("payload %s: %v", raw, err)
	}
	return m
}

// A deterministic accept (the structure check, or a repo declaring no hook)
// invoked no agent and writes no row; a gate-less create is unchanged.
func TestCreateStructureAcceptWritesNoLifecycleRows(t *testing.T) {
	withWiring(t)
	db := wire(t)
	verb.SetCreateReviewer(createStub{dec: verb.GateDecision{Gated: true, Accept: true, Skill: "structure"}})
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "well formed", "acceptance_criteria": "1. works"}), &it)
	for _, k := range []string{ledger.KindAgentInvocation, ledger.KindReviewAccept, ledger.KindReviewReject} {
		if n := len(entriesOf(t, db, "", k)); n != 0 {
			t.Errorf("%s rows = %d, want 0 for a deterministic accept", k, n)
		}
	}
}
