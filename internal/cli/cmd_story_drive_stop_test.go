package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// The node a drive runs on is named on every record of its stop, so the server can
// tell a remote's park from a person's (sty_aaafe4e2).

func rowPayload(t *testing.T, e ledger.Entry) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("%s payload: %v", e.Kind, err)
	}
	return p
}

// A drive that stopped short of its exit has nothing to let go of: engaging takes
// no hosted hold (sty_40853d02).
func TestStoryDriveStoppedMakesNoHoldCalls(t *testing.T) {
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
