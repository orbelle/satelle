package agentcli

import "testing"

func TestCommandFromHookEvent(t *testing.T) {
	// Claude Code snake_case (tool_input.command).
	if got := CommandFromHookEvent([]byte(`{"tool_input":{"command":"git push"}}`)); got != "git push" {
		t.Errorf("claude CommandFromHookEvent = %q, want 'git push'", got)
	}
	// Grok camelCase (toolInput.command) — sty_0d3665ee.
	if got := CommandFromHookEvent([]byte(`{"hookEventName":"pre_tool_use","toolName":"run_terminal_command","toolInput":{"command":"git commit -m x"}}`)); got != "git commit -m x" {
		t.Errorf("grok CommandFromHookEvent = %q, want 'git commit -m x'", got)
	}
	if got := CommandFromHookEvent([]byte(`{"tool_input":{"file_path":"/a"}}`)); got != "" {
		t.Errorf("non-shell event should yield empty command, got %q", got)
	}
	if got := CommandFromHookEvent([]byte(`not json`)); got != "" {
		t.Errorf("bad event should yield empty command, got %q", got)
	}
}
