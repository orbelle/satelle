package verb_test

import (
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
)

// sty_4cf2c585: a rejection returns to the session that requested the edge and the
// story stays in its SOURCE status; the destination step's reject_budget bounds
// the loop. The fixture is a TERMINAL destination (release → done), the edge the
// story is about: a terminal state is never re-entered, so a reject holds the
// story in release and the retry is admitted into the same edge.

const rejectBudgetDoneToml = `["*"]
obligations = ["raised", "coded", "released", "closed"]
park = { state = "blocked", gate = "park-gate" }
`

func rejectBudgetStepToml(closedKnobs string) string {
	return `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[released]
status = "release"
agent = "executor"
requires = ["coded"]

[closed]
status = "done"
reviewers = ["gate-a", "gate-b"]
terminal = true
requires = ["released"]
` + closedKnobs
}

func rejectBudgetWF(closedKnobs string) map[string]string {
	return routeHalves(rejectBudgetDoneToml, rejectBudgetStepToml(closedKnobs))
}

// twoReviewersReject is one presentation where both reviewers of the edge reject,
// with distinctive multi-line notes and reasoning that a summary would lose.
func twoReviewersReject(n int) verb.GateDecision {
	notes := func(skill string) string {
		return `round ` + string(rune('0'+n)) + ` ` + skill + `: "AC2" is unmet —
	fixture missing under tests/  (two spaces, a tab, a newline)`
	}
	return verb.GateDecision{Gated: true, Accept: false, Skill: "gate-a", Reviewers: []verb.ReviewerVerdict{
		{Skill: "gate-a", Accept: false, Notes: notes("gate-a"), Reasoning: "reasoning-a-" + string(rune('0'+n))},
		{Skill: "gate-b", Accept: false, Notes: notes("gate-b")},
	}}
}

func ledgerEntries(t *testing.T, id string) []ledger.Entry {
	t.Helper()
	var all []ledger.Entry
	if err := json.Unmarshal(call(t, "ledger-list", map[string]any{"story_id": id}), &all); err != nil {
		t.Fatal(err)
	}
	return all
}
