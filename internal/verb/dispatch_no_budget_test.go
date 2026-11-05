package verb

// sty_58a7bdfe AC2: satelle manages no spend budget. A dispatch that reports
// any number of turns and input tokens prints no budget line, writes no
// budget_overrun row, and is neither reworked nor parked for spend; its measured
// turns are still recorded on its agent_invocation row (AC6).

import (
	"context"

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
