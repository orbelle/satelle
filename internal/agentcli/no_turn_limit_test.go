package agentcli

import (
	"strings"
	"testing"
)

// sty_58a7bdfe AC3: no adapter command receives a turn limit from satelle. Every
// command template satelle ships renders an argv with no turn flag and no
// {max_turns}, whatever the request carries, and the capability table has no
// turn-budget column.
func TestShippedCommandsCarryNoTurnLimit(t *testing.T) {
	shipped := map[string]string{
		"claude":        DefaultClaudeCommand,
		"claude stream": DefaultClaudeStreamCommand,
		"grok":          DefaultGrokCommand,
		"cursor":        CursorCommand,
		"pi rpc":        PiRPCCommand,
	}
	for _, cli := range []string{CLIClaude, CLIGrok} {
		r, err := NewRunner(cli)
		if err != nil {
			t.Fatalf("NewRunner(%s): %v", cli, err)
		}
		shipped["preset "+cli] = r.Command()
	}
	req := Request{SystemPrompt: "s", Payload: "p", AllowedTools: "Read", Model: "m", Effort: "high", Settings: "{}"}
	for name, cmd := range shipped {
		if strings.Contains(cmd, "max_turns") || strings.Contains(cmd, "max-turns") {
			t.Errorf("%s template carries a turn limit: %q", name, cmd)
		}
		for _, a := range buildArgs(strings.Fields(cmd)[1:], req) {
			if strings.Contains(a, "max-turns") || strings.Contains(a, "max_turns") {
				t.Errorf("%s argv carries a turn limit: %q", name, a)
			}
		}
	}
	for _, col := range capabilityColumns {
		if strings.Contains(col, "turn") {
			t.Errorf("capability table still has a %q column", col)
		}
	}
	if md := CapabilityTableMarkdown(); strings.Contains(strings.ToLower(md), "turn budget") {
		t.Errorf("rendered capability table names a turn budget:\n%s", md)
	}
}
