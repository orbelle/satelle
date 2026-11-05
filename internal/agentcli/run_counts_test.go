package agentcli

import (
	"errors"
	"testing"
)

// sty_46e950ab AC1: a reviewer row records its turns when the harness reports
// them, and an adapter-named reason when it does not — never a bare zero. The
// adapter half is proven here, one case per harness and transport.

func TestTurns_ClaudeJSONEnvelopeReportsNumTurns(t *testing.T) {
	text, u := UnwrapUsage(usageFixture(t, "claude_json_turns.json"))
	if string(text) != "ok" {
		t.Errorf("text = %q, want the envelope's result", text)
	}
	if !u.TurnsAvailable || u.Turns != 1 {
		t.Fatalf("turns = %d available=%v, want 1 true", u.Turns, u.TurnsAvailable)
	}
}

func TestTurns_GrokEnvelopesReportNumTurns(t *testing.T) {
	_, u := UnwrapUsage(usageFixture(t, "grok_json.json"))
	if !u.TurnsAvailable || u.Turns != 1 {
		t.Errorf("grok json: turns = %d available=%v, want 1 true", u.Turns, u.TurnsAvailable)
	}
	got, ok := jsonlUsage(usageFixture(t, "grok_streaming.jsonl"))
	if !ok {
		t.Fatal("grok streaming fixture carries usage")
	}
	if !got.TurnsAvailable || got.Turns < 1 {
		t.Errorf("grok streaming: turns = %d available=%v, want the capture's num_turns", got.Turns, got.TurnsAvailable)
	}
}

// sty_46e950ab AC2: the turn-cap stop is detected from each provider's own
// spelling, and a claude error_max_turns envelope still yields its usage and turns.
func TestDetectMaxTurns(t *testing.T) {
	if !DetectMaxTurns(usageFixture(t, "grok_max_turns.txt")) {
		t.Error("the grok cap-exhausted output must be detected")
	}
	if !DetectMaxTurns(usageFixture(t, "claude_max_turns.json")) {
		t.Error("claude's error_max_turns envelope must be detected")
	}
	for _, name := range []string{"claude_json_turns.json", "grok_json.json", "claude_result.json"} {
		if DetectMaxTurns(usageFixture(t, name)) {
			t.Errorf("%s did not stop at the cap", name)
		}
	}
	if !errors.Is(errors.Join(ErrMaxTurns, errors.New("exit status 1")), ErrMaxTurns) {
		t.Error("ErrMaxTurns must be matchable through a wrapped error")
	}
}

func TestClaudeErrorEnvelopeKeepsUsageAndTurns(t *testing.T) {
	raw := usageFixture(t, "claude_max_turns.json")
	text, u := UnwrapUsage(raw)
	if string(text) != string(raw) {
		t.Errorf("an error envelope has no result: stdout must come back verbatim, got %q", text)
	}
	if !u.Available || u.OutputTokens != 1893 {
		t.Errorf("usage = %+v, want the envelope's tokens", u)
	}
	if !u.TurnsAvailable || u.Turns != 9 {
		t.Errorf("turns = %d available=%v, want 9 true", u.Turns, u.TurnsAvailable)
	}
}
