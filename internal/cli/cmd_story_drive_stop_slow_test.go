//go:build clislow

package cli

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// AC6: the CLI fills the drive's location from hosted.KnownLocationID, on the park
// comment and on the drive-stopped row alike.
func TestSlowStoryDrivePassesLocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts driveOpts
		kind string
	}{
		{"park comment", driveOpts{gateRejects: 999, rejectBudget: 1}, ledger.KindComment},
		{"drive-stopped row", driveOpts{gateRejects: 999, rejectBudget: 1, noPark: true}, ledger.KindDriveStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDriveFixture(t, tc.opts)
			loc, err := hosted.LocationID(f.repo)
			if err != nil {
				t.Fatal(err)
			}
			id := f.create()
			_, _ = runRoot(t, "story", "drive", id)
			var found bool
			for _, e := range f.rows(id) {
				p := rowPayload(t, e)
				if e.Kind != tc.kind || p["source"] != "drive" {
					continue
				}
				found = true
				if p["location_id"] != loc || hosted.KnownLocationID(f.repo) != loc || p["location_label"] != hosted.LocationLabel(f.repo) {
					t.Errorf("%s payload = %v, want location %q labelled %q", e.Kind, p, loc, hosted.LocationLabel(f.repo))
				}
			}
			if !found {
				t.Fatalf("no %s row from the drive", tc.kind)
			}
		})
	}
}
