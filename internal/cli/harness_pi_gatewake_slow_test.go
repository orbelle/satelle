//go:build clislow

package cli

import (
	"strings"
	"testing"
)

// AC1+AC2: a gate still running when the agent settles does not re-prompt: the
// stop is allowed, and the verdict is sent as ONE user message when the handle
// finishes. A later settle does not repeat it, and the next prompt's
// additionalContext does not carry it again.
func TestSlowPiTrace_RunningGateIsSentOnceWhenItFinishes(t *testing.T) {
	r := newPiRig(t)
	store := r.useRealSatelle("1s")
	g := runningGate(t, store, "sty_slow")
	settled := r.finishGateOnConnect(store, g.ID, "accepted plan→in_progress")

	out := r.drive(true,
		// The settle waits its one bound (1s), finds the gate still going, allows
		// the stop and arms the waiter.
		piStep{Event: "agent_settled", Ctx: map[string]any{"idle": true, "hasUI": true}},
		// The gate finishes only now, after that settle has returned.
		piStep{Connect: settled},
		piStep{WaitMessages: one(1), TimeoutMS: 20000},
		piStep{Event: "agent_settled", Ctx: map[string]any{"idle": true, "hasUI": true}},
		piStep{Event: "before_agent_start", Arg: map[string]any{"prompt": "next", "systemPrompt": "SYS"}},
	)
	r.trace(out)

	if len(out.Messages) != 1 {
		t.Fatalf("the verdict must be sent exactly once, and nothing before it: %+v", out.Messages)
	}
	m := out.Messages[0]
	if !strings.Contains(m.Text, g.ID) || !strings.Contains(m.Text, "accepted plan→in_progress") || strings.Contains(m.Text, "still running") {
		t.Errorf("the one message is not the finished verdict: %q", m.Text)
	}
	if m.Opts != nil {
		t.Errorf("an idle session starts a turn with a plain user message: %+v", m.Opts)
	}
	var pending bool
	for _, n := range out.Notices {
		pending = pending || strings.Contains(n.Msg, "still running")
	}
	if !pending {
		t.Errorf("the pending gate was not noted to the operator: %+v", out.Notices)
	}
	prompt, _ := out.Results[4].Result["systemPrompt"].(string)
	if strings.Contains(prompt, g.ID) {
		t.Errorf("the prompt catch-up repeated a verdict already sent as a user message:\n%s", prompt)
	}
	if !store.Delivered(g.ID) {
		t.Error("the handle was never claimed")
	}
}
