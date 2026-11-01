package verb

// sty_58a7bdfe AC2: satelle manages no spend budget. A dispatch that reports
// any number of turns and input tokens prints no budget line, writes no
// budget_overrun row, and is neither reworked nor parked for spend; its measured
// turns are still recorded on its agent_invocation row (AC6).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// spendDispatcher is a scripted ExecutorDispatcher whose run reports a large spend.
type spendDispatcher struct{}

func (spendDispatcher) DispatchExecutor(_ context.Context, _ workitem.Item, to string) (DispatchResult, error) {
	if to != "in_progress" {
		return DispatchResult{}, nil
	}
	turns := 500
	return DispatchResult{Dispatched: true, Agent: "coder", Command: "fake", Skill: "coder",
		TokensIn: 9_000_000, UsageAvailable: true, Turns: &turns}, nil
}

func TestDispatchIsNeverBudgeted(t *testing.T) {
	h := newEngageHarness(t, "")
	SetExecutorDispatcher(spendDispatcher{})
	it := h.create("spendy")
	h.lines = nil
	h.set(it.ID, "in_progress") // asserts the transition enacted and was not parked

	got, err := h.db.Stories.Get(context.Background(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "in_progress" {
		t.Fatalf("status = %q, want in_progress (never parked for spend)", got.Status)
	}
	for _, l := range h.lines {
		if strings.Contains(l, "budget") || strings.Contains(l, "satelle story rework") {
			t.Errorf("dispatch printed a budget line: %q", l)
		}
	}
	if rows, _ := h.db.Ledger.ListByStory(context.Background(), it.ID, "budget_overrun"); len(rows) != 0 {
		t.Errorf("budget_overrun rows = %d, want none", len(rows))
	}
	rows, err := h.db.Ledger.ListByStory(context.Background(), it.ID, ledger.KindAgentInvocation)
	if err != nil {
		t.Fatal(err)
	}
	var recorded bool
	for _, e := range rows {
		var p map[string]any
		if json.Unmarshal(e.Payload, &p) != nil || p["agent"] != "coder" {
			continue
		}
		if _, ok := p["budget"]; ok {
			t.Errorf("the invocation row carries a budget report: %s", e.Payload)
		}
		recorded = recorded || p["turns"] == float64(500)
	}
	if !recorded {
		t.Errorf("the run's measured turns must be recorded on its agent_invocation row: %v", rows)
	}
}
