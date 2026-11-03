//go:build clislow

package cli

import (
	"strings"
	"testing"
)

// AC6: a child the placement rule places remote is refused, the refusal names
// the placement and the way forward, and it is ledgered; status does not move.
func TestSlowStoryReworkRefusesARemotePlacedChild(t *testing.T) {
	id := placedReworkRepo(t)
	signIn(t)

	out, err := runRoot(t, "story", "rework", id)
	if err == nil {
		t.Fatalf("rework must be refused for a remote-placed child:\n%s", out)
	}
	for _, want := range []string{id, "placed remote", "coder-cloud", "re-present the edge"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must contain %q, got: %v", want, err)
		}
	}
	if got := storyStatus(t, id); got != "in_progress" {
		t.Errorf("status = %q; a refusal must not move the story", got)
	}
	led, lerr := runRoot(t, "ledger", "list", "--story", id)
	if lerr != nil {
		t.Fatalf("ledger: %v\n%s", lerr, led)
	}
	if !strings.Contains(led, `"kind": "rework_refused"`) || !strings.Contains(led, `"placement": "remote"`) {
		t.Errorf("the refusal must be on the ledger:\n%s", led)
	}
	if rows := reworkMessages(t, id); len(rows) != 0 {
		t.Errorf("a refused relay must exchange no turns, got %+v", rows)
	}
}

// AC6: the same step's child that is not signed in — the not-signed-in
// fallback — is performed locally, so the relay still opens.
func TestSlowStoryReworkOpensForANotSignedInChild(t *testing.T) {
	id := placedReworkRepo(t)

	out, err := runRoot(t, "story", "rework", id)
	if err != nil {
		t.Fatalf("a locally placed child must open the relay: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"converged":true`) {
		t.Errorf("relay result missing:\n%s", out)
	}
}

// AC6: a child carrying a local tag is placed local even when signed in.
func TestSlowStoryReworkOpensForALocalTaggedChild(t *testing.T) {
	id := placedReworkRepo(t, "lane:trunk")
	signIn(t)

	out, err := runRoot(t, "story", "rework", id)
	if err != nil {
		t.Fatalf("a local-tagged child must open the relay: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"converged":true`) {
		t.Errorf("relay result missing:\n%s", out)
	}
}
