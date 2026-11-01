package verb

// sty_58a7bdfe AC1: satelle manages no context budget, so engaging a story gives
// no fresh-session advice and records no session_advisory row — not even when
// the driving session already drove another story, the case the removed
// advisory used to fire on.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

type engageHarness struct {
	t     *testing.T
	db    *store.DB
	lines []string
}

func newEngageHarness(t *testing.T, session string) *engageHarness {
	t.Helper()
	withWiring(t)
	h := &engageHarness{t: t, db: wireDU(t)}
	if session != "" {
		t.Setenv(config.SessionEnv, session)
		t.Setenv("CLAUDECODE", "1")
	}
	stubSnapshotter(t, agentcli.DriverSnapshot{Available: true, Model: "claude-opus-5-5",
		FreshInputTokens: 100, CacheReadInputTokens: 50, CacheCreationInputTokens: 10, OutputTokens: 10, Turns: 1})
	SetVerdictRecorder(func(s string) { h.lines = append(h.lines, s) })
	return h
}

func (h *engageHarness) dispatch(name string, req map[string]any) workitem.Item {
	h.t.Helper()
	b, _ := json.Marshal(req)
	resp, err := Dispatch(context.Background(), name, b)
	if err != nil {
		h.t.Fatalf("%s %v: %v", name, req, err)
	}
	var it workitem.Item
	if err := json.Unmarshal(resp, &it); err != nil {
		h.t.Fatal(err)
	}
	return it
}

func (h *engageHarness) create(title string) workitem.Item {
	return h.dispatch("story-create", map[string]any{"title": title, "status": "backlog",
		"tags": []string{"estimate-minutes:10", "estimate-tokens:1000"}})
}

func (h *engageHarness) set(id, status string) workitem.Item {
	it := h.dispatch("story-set", map[string]any{"id": id, "status": status})
	if it.Status != status {
		h.t.Fatalf("story-set %s → %s: status = %q", id, status, it.Status)
	}
	return it
}

func TestEngageGivesNoFreshSessionAdvice(t *testing.T) {
	h := newEngageHarness(t, "sess-no-advice")
	first := h.create("first story")
	h.set(first.ID, "in_progress")
	h.set(first.ID, "blocked")
	second := h.create("second story")
	h.lines = nil
	h.set(second.ID, "in_progress") // asserts the engage succeeded

	for _, id := range []string{first.ID, second.ID} {
		rows, err := h.db.Ledger.ListByStory(context.Background(), id, "session_advisory")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Errorf("%s: session_advisory rows = %d, want none", id, len(rows))
		}
	}
	for _, l := range h.lines {
		if strings.Contains(l, "fresh session") || strings.Contains(l, "context_budget") {
			t.Errorf("engage printed budget advice: %q", l)
		}
	}
}
