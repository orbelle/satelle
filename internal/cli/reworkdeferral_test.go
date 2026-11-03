package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/wfroute"
)

// sty_83620b39: the consult seat defers what the coder cannot clear, and the
// relay ends converged_with_deferrals instead of holding on it.

func deferLoop(coder, consultant *scriptSess, rounds int) *reworkLoop {
	l := newReworkLoop(coder, consultant, rounds, &memLedger{}, &bytes.Buffer{})
	l.DeferOwners = map[string]bool{agentstep.DeferOwnerGrant: true, "integration-review": true}
	return l
}

func TestReworkLaterGatesFromRoute(t *testing.T) {
	route := wfroute.Route{Steps: []wfroute.Step{
		{Status: "backlog", Reviewers: []wfroute.Reviewer{{Skill: "plan-review"}}},
		{Status: "in_progress", Reviewers: []wfroute.Reviewer{{Skill: "definition-check"}, {Skill: "estimate-review"}}},
		{Status: "integration", Reviewers: []wfroute.Reviewer{{Skill: "code-review"}, {Skill: "estimate-review"}}},
		{Status: "done", Reviewers: []wfroute.Reviewer{{Skill: "estimate-review"}, {Skill: "code-review"}}},
	}}
	got := laterGates(route, "in_progress")
	want := []agentstep.LaterGate{
		{Step: "integration", Skill: "code-review"}, {Step: "integration", Skill: "estimate-review"},
		{Step: "done", Skill: "estimate-review"}, {Step: "done", Skill: "code-review"},
	}
	if len(got) != len(want) {
		t.Fatalf("laterGates = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("gate %d = %+v, want %+v (route order; the current step's own gates are excluded)", i, got[i], want[i])
		}
	}
	owners := deferOwners(got)
	for _, o := range []string{agentstep.DeferOwnerGrant, "code-review", "estimate-review"} {
		if !owners[o] {
			t.Errorf("owner %q must be accepted", o)
		}
	}
	if owners["definition-check"] || owners["plan-review"] {
		t.Error("a gate on the current or an earlier step is not a later gate and owns nothing")
	}
	if len(laterGates(route, "done")) != 0 || len(laterGates(route, "nowhere")) != 0 {
		t.Error("no later steps means no later gates")
	}
}

func TestParseConsultReplyDeferrals(t *testing.T) {
	owners := map[string]bool{"grant": true, "integration-review": true}
	for _, tc := range []struct {
		name      string
		reply     string
		ready     bool
		objection string
		deferred  []agentstep.DeferredItem
	}{
		{
			"deferral then READY", "fine\nDEFERRED integration-review: db tests never ran\nREADY",
			true, "", []agentstep.DeferredItem{{Owner: "integration-review", Objection: "db tests never ran"}},
		},
		{
			"deferral on the final line", "DEFERRED grant: needs docker\nNOT READY: DEFERRED integration-review: db tests never ran",
			true, "", []agentstep.DeferredItem{{Owner: "grant", Objection: "needs docker"}, {Owner: "integration-review", Objection: "db tests never ran"}},
		},
		{
			"real objection beside a deferral", "DEFERRED grant: needs docker\nNOT READY: AC2 untested",
			false, "AC2 untested", []agentstep.DeferredItem{{Owner: "grant", Objection: "needs docker"}},
		},
		{
			"unknown owner holds a READY", "DEFERRED nonexistent-gate: x\nREADY",
			false, "DEFERRED nonexistent-gate: x", nil,
		},
		{
			"unknown owner joins the reason", "DEFERRED nonexistent-gate: x\nNOT READY: y",
			false, "y\nDEFERRED nonexistent-gate: x", nil,
		},
		{
			"unknown owner on the final line", "NOT READY: DEFERRED bogus: x",
			false, "DEFERRED bogus: x", nil,
		},
		{"no colon is no deferral", "DEFERRED grant\nREADY", false, "DEFERRED grant", nil},
		{"mid-line mention is prose", "we could DEFERRED grant: nope\nREADY", true, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready, objection, deferred := parseConsultReply(tc.reply, owners)
			if ready != tc.ready || objection != tc.objection {
				t.Errorf("ready=%v objection=%q, want ready=%v objection=%q", ready, objection, tc.ready, tc.objection)
			}
			if len(deferred) != len(tc.deferred) {
				t.Fatalf("deferred = %+v, want %+v", deferred, tc.deferred)
			}
			for i := range tc.deferred {
				if deferred[i] != tc.deferred[i] {
					t.Errorf("deferred[%d] = %+v, want %+v", i, deferred[i], tc.deferred[i])
				}
			}
		})
	}
}

// AC2: only deferrals remain, so the first judgment ends the relay and the coder
// is never asked.
func TestReworkDeferredGateEvidenceConverges(t *testing.T) {
	consultant := newScriptSess("DEFERRED integration-review: db tests never ran\nREADY")
	coder := newScriptSess()
	res, err := deferLoop(coder, consultant, 3).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Converged || res.Outcome != outcomeConvergedDeferred || res.Rounds != 1 {
		t.Fatalf("result = %+v; want converged_with_deferrals in 1 round", res)
	}
	if len(res.Deferred) != 1 || res.Deferred[0].Owner != "integration-review" {
		t.Errorf("deferred = %+v", res.Deferred)
	}
	if len(coder.turns) != 0 {
		t.Errorf("a deferral hands nothing to the coder, got turns %q", coder.turns)
	}
}

func TestReworkDeferredOnFinalLine(t *testing.T) {
	consultant := newScriptSess("NOT READY: DEFERRED grant: needs docker")
	coder := newScriptSess()
	res, err := deferLoop(coder, consultant, 3).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Converged || res.Outcome != outcomeConvergedDeferred || len(res.Deferred) != 1 || len(coder.turns) != 0 {
		t.Fatalf("result = %+v coder turns %q; want converged_with_deferrals with no coder turn", res, coder.turns)
	}
}

func TestReworkDeferralsAccumulate(t *testing.T) {
	consultant := newScriptSess(
		"DEFERRED grant: needs docker\nNOT READY: AC2 untested",
		"DEFERRED grant: Needs  docker\nDEFERRED integration-review: db tests never ran\nREADY",
	)
	coder := newScriptSess("added the AC2 test")
	res, err := deferLoop(coder, consultant, 3).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != outcomeConvergedDeferred || res.Rounds != 2 || !res.Converged {
		t.Fatalf("result = %+v; want converged_with_deferrals in 2 rounds", res)
	}
	if len(res.Deferred) != 2 {
		t.Errorf("deferred = %+v; the repeated grant deferral must be de-duplicated", res.Deferred)
	}
	if len(coder.turns) != 1 || coder.turns[0] != "AC2 untested" {
		t.Errorf("coder turns = %q; only the real objection is relayed", coder.turns)
	}
}

func TestReworkPlainConvergenceKeepsItsOutcome(t *testing.T) {
	res, err := deferLoop(newScriptSess(), newScriptSess("READY"), 3).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != outcomeConverged || len(res.Deferred) != 0 {
		t.Errorf("result = %+v; a relay with no deferrals is plain converged", res)
	}
}

// AC4: a DEFERRED line naming an unknown owner is an ordinary objection.
func TestReworkUnknownDeferOwnerHolds(t *testing.T) {
	consultant := newScriptSess("DEFERRED nonexistent-gate: x\nNOT READY: y", "READY")
	coder := newScriptSess("done")
	res, err := deferLoop(coder, consultant, 3).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(coder.turns) != 1 || !strings.Contains(coder.turns[0], "y") || !strings.Contains(coder.turns[0], "DEFERRED nonexistent-gate: x") {
		t.Fatalf("coder turns = %q; the objection must carry both the reason and the unknown-owner line", coder.turns)
	}
	if res.Outcome != outcomeConverged || len(res.Deferred) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestReworkUnknownOwnerOnlyHolds(t *testing.T) {
	consultant := newScriptSess("NOT READY: DEFERRED bogus: x", "NOT READY: DEFERRED bogus: x")
	coder := newScriptSess("a", "b")
	res, err := deferLoop(coder, consultant, 2).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Converged || res.Outcome != outcomeBudget || res.Rounds != 2 || len(res.Deferred) != 0 {
		t.Fatalf("result = %+v; want the budget spent on an undeferred objection", res)
	}
	if len(coder.turns) != 2 || coder.turns[0] != "DEFERRED bogus: x" {
		t.Errorf("coder turns = %q; the objection is relayed", coder.turns)
	}
}

func TestReworkUndeferredObjectionStillStopsOnNoProgress(t *testing.T) {
	consultant := newScriptSess("DEFERRED grant: needs docker\nNOT READY: x", "DEFERRED grant: needs docker\nNOT READY: x", "NOT READY: x")
	coder := newScriptSess("still waiting", "still waiting")
	l := deferLoop(coder, consultant, 5)
	l.Evidence = func(context.Context) (string, error) { return "same", nil }
	res, err := l.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != outcomeNoProgress || res.Rounds != 2 || res.LastObjection != "x" {
		t.Fatalf("result = %+v; want no_progress exactly as before", res)
	}
}

// seedEchoPeer is a fake stream-json agent that answers each user turn with the
// turn's own text followed by the next scripted reply, so a test can read what
// the session was handed from `satelle story messages`.
func seedEchoPeer(t *testing.T, path, replyEnv string) {
	t.Helper()
	script := `#!/usr/bin/env python3
import json, os, sys
replies = os.environ[` + "\"" + replyEnv + "\"" + `].split("|")
n = 0
def send(o):
    sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()
while True:
    line = sys.stdin.readline()
    if not line:
        break
    msg = json.loads(line)
    if msg.get("type") != "user":
        continue
    content = msg.get("message", {}).get("content", "")
    if isinstance(content, list):
        content = "".join(b.get("text", "") for b in content if isinstance(b, dict))
    body = replies[n] if n < len(replies) else "(exhausted)"
    n += 1
    text = "SEED-ECHO " + content + "\n" + body
    send({"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":text}]}})
    send({"type":"result","result":text})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
