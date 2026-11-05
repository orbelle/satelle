package verb

import (
	"context"
	"time"
)

// SeatPolicyFor exposes the full seat claim (key + co-hold flags).
var SeatPolicyFor = seatPolicyFor

// RefuseSecondEngagingStoryDerived exposes the committed-status seat scan.
var RefuseSecondEngagingStoryDerived = refuseSecondEngagingStoryDerived

// ForceStatus moves a story's status behind a writer's back, as a racing writer
// would, so a compare-and-set write against the status it read must fail.
func ForceStatus(ctx context.Context, id, status string) error {
	s, err := requireWorkItem()
	if err != nil {
		return err
	}
	_, err = s.SetStatus(ctx, id, status, time.Now())
	return err
}
