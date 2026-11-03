//go:build clislow

package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// TestSlowStoryReworkRefusesAStepWithNoLoop: the story is at a status the route
// declares no rework on, so the command says so rather than inventing a loop.
func TestSlowStoryReworkRefusesAStepWithNoLoop(t *testing.T) {
	_, id := reworkRepo(t, "2", "READY", "done")
	// backlog declares no rework; move nothing, just ask about the wrong step by
	// driving the story forward to the terminal-adjacent state.
	if out, err := runRoot(t, "story", "set", id, "--status", "done"); err != nil {
		t.Fatalf("advance: %v\n%s", err, out)
	}
	if out, err := runRoot(t, "story", "rework", id); err == nil {
		t.Fatalf("want a refusal at a step with no rework key:\n%s", out)
	} else if !strings.Contains(err.Error(), "declares no rework loop") {
		t.Errorf("refusal should name the missing key, got: %v", err)
	}
}

// TestSlowStoryReworkExportsLeaseSessionAndRelayMarkerOnlyOnCoder drives the real
// verb (sty_7567f047 AC1/AC3): lease stamped sess-lease at engage, process
// SATELLE_SESSION switched to sess-other before rework, env-echo peers prove
// the coder spawn got relay=coder + session=sess-lease and the consultant got
// an empty relay marker.
func TestSlowStoryReworkExportsLeaseSessionAndRelayMarkerOnlyOnCoder(t *testing.T) {
	_, id := reworkRepoWith(t, "1",
		"NOT READY: need env proof",
		"fixed under adopted seat",
		reworkEnvEchoPeer,
		"sess-lease",
	)
	t.Setenv(config.SessionEnv, "sess-other")

	out, err := runRoot(t, "story", "rework", id)
	if err != nil {
		t.Fatalf("rework: %v\n%s", err, out)
	}

	rows := reworkMessages(t, id)
	var coderBody, consultBody string
	for _, r := range rows {
		switch r.From {
		case "coder":
			coderBody = r.Body
		case "consultant":
			consultBody = r.Body
		}
	}
	if coderBody == "" || consultBody == "" {
		t.Fatalf("want both roles in messages, got: %+v", rows)
	}
	if !strings.Contains(coderBody, "relay=coder") || !strings.Contains(coderBody, "session=sess-lease") {
		t.Fatalf("coder must inherit relay marker and adopted lease session, got: %q", coderBody)
	}
	if !strings.Contains(consultBody, "relay= ") {
		t.Fatalf("consultant must show empty relay marker (relay= ), got: %q", consultBody)
	}
	if strings.Contains(consultBody, "relay=coder") {
		t.Fatalf("consultant must not inherit the coder relay marker, got: %q", consultBody)
	}
}
