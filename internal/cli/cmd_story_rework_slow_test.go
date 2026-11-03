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

// TestSlowStoryReworkE2EConvergesAndLeavesStatusAlone covers AC2 (two live
// sessions, relayed turns, `satelle story messages` in order with the real
// roles) and AC3 (converged result printed and ledgered; status identical
// before and after).
func TestSlowStoryReworkE2EConvergesAndLeavesStatusAlone(t *testing.T) {
	_, id := reworkRepo(t, "3",
		"AC1 has no test.\nNOT READY: AC1 has no test|Looks right now.\nREADY",
		"added TestAC1 in internal/x; done")

	before := storyStatus(t, id)
	out, err := runRoot(t, "story", "rework", id)
	if err != nil {
		t.Fatalf("rework: %v\n%s", err, out)
	}
	after := storyStatus(t, id)
	if before != after || after != "in_progress" {
		t.Fatalf("status moved: %q → %q (the relay must never set status)", before, after)
	}

	// The result is on stdout as JSON.
	if !strings.Contains(out, `"converged":true`) || !strings.Contains(out, `"rounds":2`) {
		t.Fatalf("result line missing from stdout:\n%s", out)
	}
	// ...and on the ledger, so the orchestrator can read it either way.
	led, err := runRoot(t, "ledger", "list", "--story", id)
	if err != nil {
		t.Fatalf("ledger: %v\n%s", err, led)
	}
	if !strings.Contains(led, "rework outcome=converged converged=true rounds=2/3") {
		t.Fatalf("ledger missing the relay outcome row:\n%s", led)
	}

	// AC2: the conversation reads as itself, in order, with the real roles.
	rows := reworkMessages(t, id)
	if len(rows) != 3 {
		t.Fatalf("message rows = %d, want 3:\n%+v", len(rows), rows)
	}
	wantDir := [][2]string{{"consultant", "coder"}, {"coder", "consultant"}, {"consultant", "coder"}}
	for i, w := range wantDir {
		if rows[i].From != w[0] || rows[i].To != w[1] {
			t.Errorf("row %d = %s -> %s, want %s -> %s", i, rows[i].From, rows[i].To, w[0], w[1])
		}
		if rows[i].Cc != "*" {
			t.Errorf("row %d cc = %q, want * so the transcript reaches the edge reviewer", i, rows[i].Cc)
		}
	}
	if !strings.Contains(rows[0].Body, "NOT READY: AC1 has no test") {
		t.Errorf("first row = %q; want the consultant's whole reply", rows[0].Body)
	}
	if !strings.Contains(rows[1].Body, "added TestAC1") {
		t.Errorf("second row = %q; want the coder's reply", rows[1].Body)
	}
}

// TestSlowStoryReworkE2ESpendsTheBudgetAndParksNothing covers AC3's other end: the
// budget is spent, converged is false with the last objection captured, and
// status is STILL untouched — parking is the orchestrator's decision, not the
// relay's.
func TestSlowStoryReworkE2ESpendsTheBudgetAndParksNothing(t *testing.T) {
	_, id := reworkRepo(t, "2",
		"NOT READY: first thing|NOT READY: second thing|NOT READY: third thing",
		"tried a|tried b|tried c")

	out, err := runRoot(t, "story", "rework", id)
	if err != nil {
		t.Fatalf("rework: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"converged":false`) || !strings.Contains(out, `"rounds":2`) {
		t.Fatalf("result line wrong:\n%s", out)
	}
	if !strings.Contains(out, `"last_objection":"second thing"`) {
		t.Fatalf("last objection not captured:\n%s", out)
	}
	if got := storyStatus(t, id); got != "in_progress" {
		t.Fatalf("status = %q; a spent budget must not move or park the story", got)
	}
}

// TestSlowStoryReworkRoundsFlagMayOnlyLower: the budget is configuration.
func TestSlowStoryReworkRoundsFlagMayOnlyLower(t *testing.T) {
	_, id := reworkRepo(t, "2", "NOT READY: a|NOT READY: b", "fix a|fix b")

	if out, err := runRoot(t, "story", "rework", id, "--rounds", "5"); err == nil {
		t.Fatalf("--rounds above the authored budget must refuse:\n%s", out)
	} else if !strings.Contains(err.Error(), "authored budget") {
		t.Errorf("refusal should name the authored budget, got: %v", err)
	}

	out, err := runRoot(t, "story", "rework", id, "--rounds", "1")
	if err != nil {
		t.Fatalf("rework --rounds 1: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"rounds":1`) {
		t.Fatalf("--rounds 1 should have spent exactly one round:\n%s", out)
	}
}
