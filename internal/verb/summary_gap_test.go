package verb_test

import (
	"context"

	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// stubSummariser is a fixed StepSummariser for the resummarise verb test.
type stubSummariser struct {
	out       string
	err       error
	mandatory bool
}

func (s stubSummariser) Summarise(_ context.Context, _ workitem.Item, _, _ string) (verb.SummaryResult, error) {
	return verb.SummaryResult{Text: s.out}, s.err
}
func (s stubSummariser) MandatorySummary(_ context.Context, _ workitem.Item) bool { return s.mandatory }
