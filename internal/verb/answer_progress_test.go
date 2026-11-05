package verb_test

import (
	"github.com/bobmcallan/satelle/internal/verb"
)

// sty_73224b86: a propose step that enables answer_progress refuses a
// re-presentation whose performer left the prior blocking findings unprogressed,
// before any gate runs. The fixture performer reports the progress fact itself;
// computing it is internal/agentstep's job (findings_test.go).

// twoSeatReject is a presentation where both seats of a panel reject.
func twoSeatReject() verb.GateDecision {
	return verb.GateDecision{Gated: true, Accept: false, Skill: "gate-a", Reviewers: []verb.ReviewerVerdict{
		{Skill: "gate-a", Accept: false, Notes: "seat one objects"},
		{Skill: "gate-b", Accept: false, Notes: "seat two objects"},
	}}
}

func offered() []string { return []string{"gate-a/seat-1#1", "gate-b/seat-2#1"} }
