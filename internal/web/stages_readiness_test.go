package web

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// sty_a42f4add AC6: a story or epic still at its start status that has only been
// readied — review rounds on its first edge, no transition, no step above 0, no
// seat — shows a current chip carrying those rounds; one with no review rows still
// shows nothing.
func TestStagesReadinessRoundsOnAnUnstartedStory(t *testing.T) {
	for _, to := range []string{"plan", "ready"} {
		rounds := []ledger.Entry{
			evA(ledger.KindReviewReject, "backlog", to, "r1"),
			evA(ledger.KindReviewReject, "backlog", to, "r2"),
		}
		stages := buildStages(rounds, "backlog", false, noStep, routeSpec(), nil, nil)
		if len(stages) != 1 || stages[0].State != "current" || stages[0].Name != "backlog" {
			t.Fatalf("backlog → %s: stages = %+v, want one current backlog chip", to, stages)
		}
		p := stages[0].out
		if p == nil || p.rejected != 2 || p.accepted != 0 || p.Edge != "backlog → "+to {
			t.Errorf("backlog → %s: pending = %+v, want ✗2 on the edge", to, p)
		}
	}
	accepted := []ledger.Entry{
		evA(ledger.KindReviewReject, "backlog", "plan", "r1"),
		evA(ledger.KindReviewAccept, "backlog", "plan", "r2"),
	}
	stages := buildStages(accepted, "backlog", false, noStep, routeSpec(), nil, nil)
	if len(stages) != 1 || stages[0].out == nil || stages[0].out.accepted != 1 || stages[0].out.rejected != 1 {
		t.Errorf("accepted + rejected rounds: stages = %+v, want one chip with ✓1 ✗1 pending", stages)
	}
	if got := buildStages(nil, "backlog", false, noStep, routeSpec(), nil, nil); len(got) != 0 {
		t.Errorf("a story with no review rows must show no chip, got %+v", got)
	}
}
