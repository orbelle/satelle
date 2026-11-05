package verb_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
)

// sty_bef97244 AC4: a verdict's waived checks are recorded on its review_accept row.
func TestVerdictRowRecordsTheWaivedChecks(t *testing.T) {
	lg, id := bundleTransition(t, verb.GateDecision{
		Gated: true, Accept: true, Skill: "satelle-story-release-review",
		Reviewers: []verb.ReviewerVerdict{
			{Skill: "satelle-story-release-review", Accept: true, Notes: "ok", Waived: []string{"check_plan_consumed"}, Command: "claude -p"},
		},
	})
	accepts, err := lg.ListByStory(context.Background(), id, ledger.KindReviewAccept)
	if err != nil || len(accepts) != 1 {
		t.Fatalf("accepts = %+v, %v", accepts, err)
	}
	var row struct {
		Waived []string `json:"waived"`
	}
	if err := json.Unmarshal(accepts[0].Payload, &row); err != nil {
		t.Fatal(err)
	}
	if len(row.Waived) != 1 || row.Waived[0] != "check_plan_consumed" {
		t.Errorf("review_accept row waived = %v, want [check_plan_consumed]", row.Waived)
	}
}
