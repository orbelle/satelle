//go:build clislow

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// AC1–AC3 end to end: the exit gate rejects, the orchestrator leaves the coder a
// message, and the same-status set dispatches the coder once with the gate's
// finding and the message in its payload; the next presentation's gate is told
// the finding was answered. A repeated set dispatches nothing, and status never
// moves on either.
func TestSlowSameStatusSetRedispatchesACommandCoderWithTheExitFindings(t *testing.T) {
	dir, id := redispatchRepo(t)
	if n := len(coderRuns(t, dir)); n != 1 {
		t.Fatalf("the coder runs once on entry, ran %d times", n)
	}
	if out, err := runRoot(t, "story", "set", id, "--status", "integration"); err == nil {
		t.Fatalf("the exit gate must reject its first presentation:\n%s", out)
	}
	if out, err := runRoot(t, "story", "message", id, "--from", "orchestrator", "--to", "coder",
		"--body", "ORCH-DECISION-MARKER: keep the guard"); err != nil {
		t.Fatalf("message: %v\n%s", err, out)
	}

	if out, err := runRoot(t, "story", "set", id, "--status", "in_progress"); err != nil {
		t.Fatalf("same-status set: %v\n%s", err, out)
	}
	runs := coderRuns(t, dir)
	if len(runs) != 2 {
		t.Fatalf("the same-status set must dispatch the coder exactly once more, ran %d times", len(runs))
	}
	for _, want := range []string{`"findings_to_answer"`, `"rd-gate#1"`, "REJECT-NOTE-MARKER", `"findings_edge_to":"integration"`, "ORCH-DECISION-MARKER"} {
		if !strings.Contains(runs[1], want) {
			t.Errorf("the re-dispatched payload must carry %s:\n%s", want, runs[1])
		}
	}
	if got := storyStatus(t, id); got != "in_progress" {
		t.Fatalf("status = %q, the re-dispatch never moves status", got)
	}

	if out, err := runRoot(t, "story", "set", id, "--status", "in_progress"); err != nil {
		t.Fatalf("repeated set: %v\n%s", err, out)
	}
	if n := len(coderRuns(t, dir)); n != 2 {
		t.Errorf("a repeated set with no newer reject must dispatch nothing, coder ran %d times", n)
	}
	if got := storyStatus(t, id); got != "in_progress" {
		t.Fatalf("status = %q after a repeated set", got)
	}

	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	inv, err := db.Ledger.ListByStory(context.Background(), id, ledger.KindAgentInvocation)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	steps := 0
	for _, e := range inv {
		var p struct{ From, To string }
		if json.Unmarshal(e.Payload, &p) == nil && p.From == "in_progress" && p.To == "in_progress" {
			steps++
		}
	}
	if steps != 1 {
		t.Errorf("in_progress→in_progress agent_invocation rows = %d, want 1", steps)
	}

	if out, err := runRoot(t, "story", "set", id, "--status", "integration"); err != nil {
		t.Fatalf("re-present the exit: %v\n%s", err, out)
	}
	gate, err := os.ReadFile(filepath.Join(dir, "gate-2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		FindingAnswers *struct {
			Answered []struct {
				Finding string `json:"finding"`
			} `json:"answered"`
			Missing []string `json:"missing"`
		} `json:"finding_answers"`
	}
	if err := json.Unmarshal(gate, &payload); err != nil {
		t.Fatalf("gate payload: %v\n%s", err, gate)
	}
	if fa := payload.FindingAnswers; fa == nil || len(fa.Answered) != 1 || fa.Answered[0].Finding != "rd-gate#1" || len(fa.Missing) != 0 {
		t.Errorf("the exit gate must be told rd-gate#1 was answered: %+v\n%s", fa, gate)
	}
}
