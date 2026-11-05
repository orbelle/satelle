package verb_test

import (
	"context"
	"time"

	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// attachingGater attaches a story document from inside the gate window — a row
// the engine and `satelle story attach` stamp with real wall-clock time — then
// lets the transition through after a beat, so the call's start time and the
// moment the transition commits are measurably apart.
type attachingGater struct{ delay time.Duration }

func (g attachingGater) Gate(ctx context.Context, item workitem.Item, to string) (verb.GateDecision, error) {
	if _, _, err := verb.AttachItemDoc(ctx, item, "gate-note", "output", "attached mid-gate"); err != nil {
		return verb.GateDecision{}, err
	}
	time.Sleep(g.delay) // time-subject: timestamp separation, so the call's start and the commit are measurably apart (a slower clock only widens the gap)
	return verb.GateDecision{Gated: false}, nil
}
