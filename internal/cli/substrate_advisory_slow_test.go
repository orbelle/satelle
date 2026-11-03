//go:build clislow

package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/snapsync"
	"github.com/bobmcallan/satelle/internal/testutil"
)

func TestSlowEngagementAdvisoryDriftedInStepAndOffline(t *testing.T) {
	testutil.IsolateHome(t)
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	if err := config.SaveGlobalHostedServer(ts.URL); err != nil {
		t.Fatal(err)
	}
	a := skillsMachine(t, ts.URL)
	a.write("skills/x.md", "x\n")
	a.pushConfig()
	b := engagingMachine(t, ts.URL)
	b.deploy()
	b.resolvedScope(config.PersonalScope)
	// A personal scope judges every area; this test judges skills.
	b.inStepExcept(f, "skills")
	id := b.createStory()

	// In step: nothing is printed.
	if lines := advisoryLines(b.engage(id)); len(lines) != 0 {
		t.Fatalf("an in-step machine must print nothing: %v", lines)
	}

	// Drifted: another clone advanced the area and this machine never checked.
	// That clone first takes what this machine holds in step, so its push moves
	// skills alone.
	a.deploy()
	a.write("skills/x.md", "from A\n")
	a.pushConfig()
	out := b.engage(id)
	lines := advisoryLines(out)
	if len(lines) != 1 {
		t.Fatalf("want exactly one advisory line, got %v\n%s", lines, out)
	}
	for _, want := range []string{"skills behind", "satelle sync status"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("advisory %q does not name %q", lines[0], want)
		}
	}
	if strings.Contains(lines[0], "unverified") {
		t.Errorf("a live check must not be marked unverified: %q", lines[0])
	}
	if got := b.recorded()["skills"].State; got != snapsync.StateBehind {
		t.Errorf("engagement must record the state it found, got %q", got)
	}

	// Offline: the server is gone. Local and recorded state speak, marked unverified.
	ts.Close()
	out = b.engage(id)
	lines = advisoryLines(out)
	if len(lines) != 1 || !strings.Contains(lines[0], "skills behind") || !strings.Contains(lines[0], "unverified (offline/unbound)") {
		t.Fatalf("offline advisory = %v\n%s", lines, out)
	}
}
