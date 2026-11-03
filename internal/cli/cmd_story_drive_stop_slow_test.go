//go:build clislow

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
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

// A drive that stopped short of its exit has nothing to let go of: engaging takes
// no hosted hold (sty_40853d02).
func TestSlowStoryDriveStoppedMakesNoHoldCalls(t *testing.T) {
	fx := newHoldFixture(t, holdNoGit)
	script := filepath.Join(t.TempDir(), "executor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\necho 'cannot go on' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	agents := "[executor]\nrole = \"agent\"\ncommand = \"" + script + " -p {system}\"\ntools = \"Read,Grep,Glob,Edit,Write,Bash(satelle:*)\"\n\n" +
		"[orchestrator]\nrole = \"agent\"\ncommand = \"in-loop\"\n"
	if err := os.WriteFile(filepath.Join(fx.repo, ".satelle", "workflows", config.AgentsConfigName), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(fx.repo, ".satelle", config.AgentsConfigName))
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	id := createStoryID(t, "stops under a drive")

	// The fixture's server fails the test on any hold route, so a drive that
	// engaged, stopped and let go of a hold would be caught here.
	out, err := runRoot(t, "story", "drive", id)
	if ExitCodeOf(err) != 1 || !strings.Contains(out, id+" stopped at in_progress: step in_progress, performer executor:") {
		t.Fatalf("want a stop at in_progress (exit 1), got %d: %v\n%s", ExitCodeOf(err), err, out)
	}
}
