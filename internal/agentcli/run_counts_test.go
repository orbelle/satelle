package agentcli

import (
	"errors"
	"strings"
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

func TestTurns_PlainGrokAndAcpAreUnavailableWithAdapterReasons(t *testing.T) {
	_, u := UnwrapUsage(usageFixture(t, "grok_max_turns.txt"))
	if u.TurnsAvailable {
		t.Fatalf("plain grok output carries no turn count: %+v", u)
	}
	for _, c := range []struct {
		name, iface, command, want string
	}{
		{"grok plain command", "command", DefaultGrokCommand, "grok command: the run's output carried no turn count"},
		{"grok acp", "acp", "grok agent stdio", "grok acp: the session protocol reports no turn count"},
		{"cloud", "cloud", "claude", "cloud: a cloud session is not spawned from a command template"},
		{"cursor command", "command", CursorCommand, "cursor command: the run's output carried no turn count"},
		{"pi command", "command", "pi -p {payload}", "pi command: the run's output carried no turn count"},
	} {
		if got := TurnsUnavailableReason(c.iface, c.command); !strings.Contains(got, c.want) {
			t.Errorf("%s: reason = %q, want it to contain %q", c.name, got, c.want)
		}
	}
}

// Tool calls are counted only from a transport that emits tool events; every
// other transport has the reason, so a zero is only ever a measured zero.
func TestToolCalls_AvailabilityIsPerTransport(t *testing.T) {
	for _, c := range []struct {
		name, iface, command string
		reports              bool
	}{
		{"claude one-shot json", "command", DefaultClaudeCommand, false},
		{"claude stream-json", "stream", DefaultClaudeStreamCommand, true},
		{"grok json (default reviewer)", "command", DefaultGrokCommand, false},
		{"grok json", "command", "grok -p {payload} --output-format json", false},
		{"grok streaming-json", "command", "grok -p {payload} --output-format streaming-json", true},
		{"grok streaming fused flag", "command", "grok -p {payload} --output-format=streaming-json", true},
		{"grok acp", "acp", "grok agent stdio", true},
		{"cursor command", "command", CursorCommand, false},
		{"pi command", "command", "pi -p {payload}", false},
		{"cloud", "cloud", "claude", false},
		{"in-loop", "command", "in-loop", false},
	} {
		if got := ReportsToolEvents(c.iface, c.command); got != c.reports {
			t.Errorf("%s: ReportsToolEvents = %v, want %v", c.name, got, c.reports)
		}
		if !c.reports {
			reason := ToolCallsUnavailableReason(c.iface, c.command)
			if reason == "" || !strings.Contains(reason, "tool") {
				t.Errorf("%s: reason = %q, want an adapter-named tool-call reason", c.name, reason)
			}
		}
	}
	if got := ToolCallsUnavailableReason("command", DefaultGrokCommand); !strings.HasPrefix(got, "grok command:") {
		t.Errorf("reason = %q, want it to name the adapter", got)
	}
	if got := ToolCallsUnavailableReason("cloud", "claude"); !strings.HasPrefix(got, "cloud:") {
		t.Errorf("cloud reason = %q", got)
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
