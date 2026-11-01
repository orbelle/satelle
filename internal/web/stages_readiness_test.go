package web

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// sty_a42f4add AC6: a story or epic still at its start status that has only been
// readied — review rounds on its first edge, no transition, no step above 0, no
// seat — shows a current chip whose exit gates carry those rounds' rejections
// (sty_0b1abacc); one with no review rows still shows nothing.
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
		g := stages[0].Gates
		if g == nil || g.Rejected != 2 || g.NotRun || g.Edge != "backlog → "+to {
			t.Errorf("backlog → %s: exit gates = %+v, want 2 rejections on the edge", to, g)
		}
		if want := "backlog: 1 exit gate, 2 rejections"; stages[0].Title != want {
			t.Errorf("backlog → %s: title = %q, want %q", to, stages[0].Title, want)
		}
	}
	accepted := []ledger.Entry{
		evA(ledger.KindReviewReject, "backlog", "plan", "r1"),
		evA(ledger.KindReviewAccept, "backlog", "plan", "r2"),
	}
	stages := buildStages(accepted, "backlog", false, noStep, routeSpec(), nil, nil)
	if len(stages) != 1 || stages[0].Gates == nil || stages[0].Gates.Rejected != 1 {
		t.Errorf("accepted + rejected rounds: stages = %+v, want one chip with 1 rejection", stages)
	}
	if got := buildStages(nil, "backlog", false, noStep, routeSpec(), nil, nil); len(got) != 0 {
		t.Errorf("a story with no review rows must show no chip, got %+v", got)
	}
}
