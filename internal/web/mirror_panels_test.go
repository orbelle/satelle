package web

import (
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// Engagement counts each item a seat is bound to once; a legacy row counts
// only as a non-stale story seat.
func TestEngagedItems(t *testing.T) {
	got := engagedItems([]mirror.Seat{
		{ID: "sto_a/s1", Item: "sto_a", Session: "s1"},
		{ID: "sto_a/s2", Item: "sto_a", Session: "s2"},
		{ID: "tas_b/s1", Item: "tas_b", Session: "s1"},
		{ID: "sty_live", StorySeat: true},
		{ID: "sty_stale", StorySeat: true, Stale: true},
		{ID: "sty_other"},
	})
	if want := []string{"sto_a", "sty_live", "tas_b"}; !slices.Equal(got, want) {
		t.Fatalf("engaged = %v, want %v", got, want)
	}
	if n := len(engagedItems(nil)); n != 0 {
		t.Fatalf("nil seats = %d", n)
	}
}
