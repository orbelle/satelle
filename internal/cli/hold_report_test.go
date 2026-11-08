package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func TestReportTransitionHold(t *testing.T) {
	res := kernel.TransitionResult{
		Item: item.Item{ID: "sto_1", Status: "parked"}, From: "drafting", To: "building",
		Gates: []gate.Result{{Gate: "review", Binding: "r1,r2", Result: gate.Reject, Notes: "panel (all): 1 of 2 accept"}},
		Held: &item.Hold{State: "drafting", Rounds: 3,
			Dissent:  []item.Dissent{{Gate: "review", Binding: "r2", Rejected: 3, Class: "AC3", ClassRounds: 2}},
			Accepted: []string{"scope/r1", "review/r1"}},
	}
	var out bytes.Buffer
	err := reportTransition(&out, res, false)
	if ExitCodeOf(err) != ExitBlocked {
		t.Fatalf("exit %d: %v", ExitCodeOf(err), err)
	}
	msg := err.Error()
	for _, want := range []string{
		"sto_1 held at parked after 3 rejected rounds leaving drafting",
		"dissent: review/r2 rejected 3 of 3 rounds; class AC3 in 2",
		"accepted: scope/r1, review/r1",
		"held for the operator: the driving session reports this hold and stops",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("hold report lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "--status") {
		t.Errorf("hold report hands the session a status command:\n%s", msg)
	}

	// Without a hold the report has no hold block.
	res.Held = nil
	if err := reportTransition(&out, res, false); strings.Contains(err.Error(), "held") {
		t.Errorf("an unheld rejection reports a hold:\n%s", err)
	}
}

// A gate whose judge could not be asked is reported as an error, not as the
// judge's verdict, and still blocks.
func TestReportTransitionError(t *testing.T) {
	res := kernel.TransitionResult{
		Item: item.Item{ID: "sto_1", Status: "plan"}, From: "plan", To: "build",
		Gates: []gate.Result{{Gate: "scope", Binding: "r1", Result: gate.Error, Notes: "dispatch failed: argument list too long"}},
	}
	var out bytes.Buffer
	err := reportTransition(&out, res, false)
	if ExitCodeOf(err) != ExitBlocked {
		t.Fatalf("exit %d: %v", ExitCodeOf(err), err)
	}
	if line := out.String(); !strings.Contains(line, "gate scope") || !strings.Contains(line, " error ") {
		t.Errorf("gate line: %q", line)
	}
	msg := err.Error()
	if strings.Contains(msg, "rejected by") || !strings.Contains(msg, "could not judge (error)\n  gate scope (r1):\n    dispatch failed: argument list too long") {
		t.Errorf("refusal:\n%s", msg)
	}
	res.Gates = append(res.Gates, gate.Result{Gate: "plan", Binding: "r2", Result: gate.Reject, Notes: "AC2 unmet"})
	msg = reportTransition(&out, res, false).Error()
	if !strings.Contains(msg, "rejected by\n  gate plan (r2)") || !strings.Contains(msg, "could not judge (error)\n  gate scope (r1)") {
		t.Errorf("mixed refusal:\n%s", msg)
	}
}
