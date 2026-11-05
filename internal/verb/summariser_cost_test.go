package verb_test

import (
	"context"

	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// costSummariser returns a recap that carries its OWN billable invocation
// (Command + usage set), so recordStepSummary takes the branch that folds the
// summariser's cost into an agent_invocation row (AC3, sty_b73c3236).
type costSummariser struct{}

func (costSummariser) Summarise(_ context.Context, _ workitem.Item, _, _ string) (verb.SummaryResult, error) {
	return verb.SummaryResult{
		Text: "the recap", Command: "claude -p", Context: "satelle-step-summary", Model: "sonnet",
		TokensIn: 1200, TokensOut: 800, TokensTotal: 2000, DurationMs: 4200,
		UsageAvailable: true, // transport reported usage (sty_56aae77a)
	}, nil
}
func (costSummariser) MandatorySummary(_ context.Context, _ workitem.Item) bool { return true }

// costUSDSummariser is costSummariser plus a dollar figure, to pin that
// SummaryResult.CostUSD (sty_c4df7376) rides all the way onto the
// agent_invocation ledger row's cost_usd field, and into story cost totals.
type costUSDSummariser struct{}

func (costUSDSummariser) Summarise(_ context.Context, _ workitem.Item, _, _ string) (verb.SummaryResult, error) {
	cost := 0.0095
	return verb.SummaryResult{
		Text: "the recap", Command: "grok agent stdio", Context: "satelle-step-summary", Model: "grok-4.7",
		TokensIn: 100, TokensOut: 50, TokensTotal: 150, DurationMs: 1000,
		UsageAvailable: true, CostUSD: &cost,
	}, nil
}
func (costUSDSummariser) MandatorySummary(_ context.Context, _ workitem.Item) bool { return true }
