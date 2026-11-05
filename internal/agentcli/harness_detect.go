package agentcli

import (
	"encoding/json"
	"strings"
)

// Harness tokens recorded in model and harness telemetry. HarnessUnknown is
// the answer for anything unrecognised — nothing is assumed to be Claude
// (satelle-agent-agnostic §3).
const (
	HarnessClaude  = "claude"
	HarnessGrok    = "grok"
	HarnessPi      = "pi"
	HarnessUnknown = "unknown"
)

// sessionMarker is one provider's in-loop environment marker. The provider's
// env-var names live here, in the adapter package, and nowhere else.
type sessionMarker struct {
	harness string
	key     string
	prefix  bool // key is a prefix rather than an exact name
	match   func(val string) bool
}

func nonEmptyNotZero(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && v != "0"
}

// sessionMarkers lists what each in-loop harness exports to its shell and hook
// children.
var sessionMarkers = []sessionMarker{
	{harness: HarnessClaude, key: "CLAUDECODE", match: func(v string) bool { return strings.TrimSpace(v) == "1" }},
	{harness: HarnessClaude, key: "CLAUDE_CODE_", prefix: true, match: func(string) bool { return true }},
	{harness: HarnessGrok, key: "GROK_AGENT", match: nonEmptyNotZero},
	{harness: HarnessPi, key: "PI_CODING_AGENT", match: nonEmptyNotZero},
	{harness: HarnessPi, key: "PI_SESSION_", prefix: true, match: func(string) bool { return true }},
	// cursor-agent exports CURSOR_AGENT=1 to its shell children and
	// CURSOR_INVOKED_AS to shell and hook children (testdata/cursor/README.md,
	// clean-6-hook-env.txt). Exact keys only: other CURSOR-bearing names in a
	// desktop environment (XCURSOR_SIZE, HYPRCURSOR_SIZE, GUM_*CURSOR*) are not
	// cursor-agent evidence, and CLAUDE_PROJECT_DIR is set by cursor's
	// Claude-compat shim, so it is evidence of neither harness.
}

// HarnessOf names the harness behind a binding's command line, for lookups keyed
// by harness (the [harness.<name>] tables): HarnessClaude, HarnessGrok,
// HarnessPi or HarnessUnknown. It is the reviewer-isolation
// classifier too: both answer through adapterOf, so there is one rule (cursor-agent
// is the executable named exactly so or cursor-agent-*, pi exactly pi or pi-*,
// which excludes pip and the like; the bare names cursor and agent are too common
// to claim, sty_7d098d50). pi was once left unknown there (sty_f141c77f);
// sty_58a9bdc8 recognises it, since preflight now reports pi's own gap.
func HarnessOf(command string) string {
	return AdapterName(command)
}

// SessionMarkerEnvNames lists the environment keys DetectSessionHarnesses
// looks at, so a caller that must neutralise harness detection (a test that
// wants a bare shell) can clear exactly what the code reads instead of
// hardcoding a list that silently drifts every time a harness is added.
func SessionMarkerEnvNames() []string {
	out := make([]string, 0, len(sessionMarkers)+len(sessionEnvKeys))
	for _, m := range sessionMarkers {
		out = append(out, m.key)
	}
	// The session-id variables change what ResolveSession returns, so a caller
	// that wants a bare shell clears them with the markers.
	return append(out, sessionEnvKeys...)
}

// DetectSessionHarnesses reports which harnesses' session markers appear in
// environ (KEY=VALUE entries). PATH is never probed.
func DetectSessionHarnesses(environ []string) map[string]bool {
	out := map[string]bool{}
	for _, e := range environ {
		key, val, _ := strings.Cut(e, "=")
		for _, m := range sessionMarkers {
			hit := key == m.key
			if m.prefix {
				hit = strings.HasPrefix(key, m.key)
			}
			if hit && m.match(val) {
				out[m.harness] = true
			}
		}
	}
	return out
}

// InLoopHarnessFromEnv reports whether environ carries any in-loop harness
// marker, and the first harness it names (claude, grok, pi order).
func InLoopHarnessFromEnv(environ []string) (string, bool) {
	found := DetectSessionHarnesses(environ)
	for _, h := range []string{HarnessClaude, HarnessGrok, HarnessPi} {
		if found[h] {
			return h, true
		}
	}
	return "", false
}

// claudeToolNames are tool names only Claude Code emits (Grok uses camelCase
// envelopes).
var claudeToolNames = map[string]bool{
	"Edit": true, "MultiEdit": true, "Write": true, "Read": true, "NotebookEdit": true,
}

// HarnessFromHookEvent classifies a hook event envelope, each adapter
// fingerprinting its own shape:
//
//   - grok: any of its own camelCase-only field names — hookEventName,
//     workspaceRoot, transcriptPath, permissionMode, a camelCase sessionId
//     with no snake_case session_id, or a camelCase toolInput with no
//     snake_case tool_input — or a transcript_path under /.grok/. Checked
//     before the claude fingerprint below because real grok hook payloads
//     carry Claude-compatible snake_case aliases (session_id,
//     hook_event_name, permission_mode, transcript_path, tool_input)
//     alongside grok's own camelCase keys — captured 2026-09-27 from a real
//     grok CLI hook firing in this repo, sty_719c4a7b AC1/AC3; see
//     testdata/hooks/README.md for exact provenance. So a snake_case alias
//     riding alongside a camelCase key is never claude evidence.
//   - claude: a snake_case envelope with none of the grok fingerprints above,
//     carrying a .claude transcript_path or a Claude-only tool name.
//     permission_mode alone is NOT sufficient — Grok's Claude-compat shim can
//     echo that key too, so a bare {permission_mode} envelope is unknown.
//   - anything else: unknown. Never claude by default.
//
// This is a sniff for unflagged invocations; the scaffolded wrapper's
// --harness flag stays authoritative.
func HarnessFromHookEvent(raw []byte) string {
	var top struct {
		ToolInputSnake      json.RawMessage `json:"tool_input"`
		ToolInputCamel      json.RawMessage `json:"toolInput"`
		Transcript          string          `json:"transcript_path"`
		ToolName            string          `json:"tool_name"`
		SessionIDSnake      json.RawMessage `json:"session_id"`
		SessionIDCamel      json.RawMessage `json:"sessionId"`
		HookEventNameCamel  json.RawMessage `json:"hookEventName"`
		WorkspaceRoot       json.RawMessage `json:"workspaceRoot"`
		TranscriptPathCamel json.RawMessage `json:"transcriptPath"`
		PermissionModeCamel json.RawMessage `json:"permissionMode"`
	}
	if json.Unmarshal(raw, &top) != nil {
		return HarnessUnknown
	}
	present := func(m json.RawMessage) bool {
		s := strings.TrimSpace(string(m))
		return s != "" && s != "null"
	}

	toolInputSnake, toolInputCamel := present(top.ToolInputSnake), present(top.ToolInputCamel)
	sessionIDSnake, sessionIDCamel := present(top.SessionIDSnake), present(top.SessionIDCamel)
	if present(top.HookEventNameCamel) || present(top.WorkspaceRoot) ||
		present(top.TranscriptPathCamel) || present(top.PermissionModeCamel) ||
		(sessionIDCamel && !sessionIDSnake) || (toolInputCamel && !toolInputSnake) ||
		strings.Contains(top.Transcript, "/.grok/") {
		return HarnessGrok
	}
	if toolInputCamel && toolInputSnake {
		// Both present with none of grok's other fingerprints above: cannot
		// tell (sty_5e4bc568's ambiguous case).
		return HarnessUnknown
	}
	if strings.Contains(top.Transcript, "/.claude/") || claudeToolNames[top.ToolName] {
		return HarnessClaude
	}
	return HarnessUnknown
}

// CommandFromHookEvent pulls the shell command out of a PreToolUse event.
// Accepts Claude Code's snake_case envelope (tool_input.command) AND Grok's
// camelCase envelope (toolInput.command) — both harnesses fire the same hook
// (epic:scoped-sync order:9 / sty_0d3665ee). Prefer the first non-empty value.
// Empty when the event carries no command (a non-shell tool, or unparseable).
func CommandFromHookEvent(raw []byte) string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return ""
	}
	for _, key := range []string{"tool_input", "toolInput"} {
		rawTI, ok := top[key]
		if !ok || len(rawTI) == 0 {
			continue
		}
		var ti map[string]json.RawMessage
		if err := json.Unmarshal(rawTI, &ti); err != nil {
			continue
		}
		cmdRaw, ok := ti["command"]
		if !ok || len(cmdRaw) == 0 {
			continue
		}
		// String form (Claude / Grok).
		var s string
		if err := json.Unmarshal(cmdRaw, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}
