package verb

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/agentartifact"
	"github.com/bobmcallan/satelle/internal/costview"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// sty_51d0f74a: the performer figures `story cost --rounds` reports are read back
// from the row dispatchPayload writes, so this pins writer and readers to the
// same keys: the turn count, the tokens and duration at the top, and the findings
// offered and answered.
func TestDispatchPayloadRowIsReadBackByTheRoundsReport(t *testing.T) {
	turns := 9
	res := DispatchResult{
		Dispatched: true, Agent: "planner", Skill: "plan",
		TokensIn: 1200, TokensOut: 300, TokensTotal: 1500, DurationMs: 4000, UsageAvailable: true,
		Turns:           &turns,
		FindingsOffered: []string{"rev#1", "rev#2"},
		Answers:         []agentartifact.Answer{{Finding: "rev#1", Answer: agentartifact.AnswerAdopted}},
	}
	entries := []ledger.Entry{
		{ID: ledger.NewID(), Kind: ledger.KindAgentInvocation, Payload: dispatchPayload("backlog", "plan", res, "att_1")},
		{ID: ledger.NewID(), Kind: ledger.KindAgentInvocation, Payload: []byte(`{"from":"backlog","to":"plan","agent":"reviewer","skill":"rev","attempt":"att_1"}`)},
		{ID: ledger.NewID(), Kind: ledger.KindReviewReject, Payload: []byte(`{"from":"backlog","to":"plan","skill":"rev","attempt":"att_1","accept":false}`)},
	}
	p := costview.ReviewRounds("sty_a", "backlog", entries).Edges[0].Rounds[0].Performer
	if p == nil || p.Turns == nil || *p.Turns != 9 || p.TokensIn != 1200 || p.TokensOut != 300 || p.DurationMs != 4000 {
		t.Fatalf("performer = %+v, want 9 turns, 1200/300 tokens, 4s read back from the written row", p)
	}
	if p.FindingsPresented != 2 || p.Answered != 1 || p.Adopted != 1 || p.Rebutted != 0 {
		t.Errorf("findings = presented %d answered %d adopted %d rebutted %d, want 2/1/1/0",
			p.FindingsPresented, p.Answered, p.Adopted, p.Rebutted)
	}
}
