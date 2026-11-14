package hook

import (
	"os"
	"strings"
)

// The session markers a harness sets in the environment of the commands its
// agent runs. A marker is the exact name, or a name prefix where so noted.
// Each cursor, pi and grok marker was captured set in a live session
// (internal/hook/testdata/caller-*.json): cursor-agent 2026.11.13-eacade1,
// pi 1.5.2 and grok 1.0.77. grok sets GROK_AGENT for its shell tool only, not
// for its hook commands. CLAUDECODE was observed set to 1 in a Claude Code
// shell.
// Names that look alike are not markers: CURSOR_VERSION, CURSOR_PROJECT_DIR,
// CURSOR_CONFIG_DIR and XCURSOR_SIZE are set outside a cursor agent's shell,
// CLAUDE_PROJECT_DIR is set by cursor too, and CLAUDE_CODE_OAUTH_TOKEN is a
// credential, not a session.
var (
	cursorMarkers    = []string{"CURSOR_AGENT", "CURSOR_INVOKED_AS"}
	piMarkers        = []string{"PI_CODING_AGENT"}
	piMarkerPrefix   = "PI_SESSION_"
	grokMarkers      = []string{"GROK_AGENT"}
	claudeMarkers    = []string{"CLAUDECODE"}
	callerOverClaude = []string{Cursor, Pi, Grok}
)

// CallerFrom tells which harness's agent is running this process from its
// environment (KEY=value entries), or "" when the environment does not say.
// A marker with an empty or blank value is unset.
//
// CLAUDECODE is inherited by everything started from a Claude Code shell, so
// beside another harness's marker it is the outer session: exactly one of
// cursor, pi and grok decides over claude. With none of those three, CLAUDECODE
// is claude. Two or more of the three is unknown: nothing in the environment
// says which is innermost.
func CallerFrom(environ []string) string {
	set := map[string]bool{}
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if strings.TrimSpace(value) == "" {
			continue
		}
		switch {
		case contains(cursorMarkers, name):
			set[Cursor] = true
		case contains(piMarkers, name) || strings.HasPrefix(name, piMarkerPrefix):
			set[Pi] = true
		case contains(grokMarkers, name):
			set[Grok] = true
		case contains(claudeMarkers, name):
			set[Claude] = true
		}
	}
	found := ""
	for _, h := range callerOverClaude {
		if !set[h] {
			continue
		}
		if found != "" {
			return ""
		}
		found = h
	}
	if found == "" && set[Claude] {
		return Claude
	}
	return found
}

// Caller is CallerFrom this process's environment.
func Caller() string { return CallerFrom(os.Environ()) }

// CallerMarkers lists the environment names CallerFrom reads, and the name
// prefixes, for a test harness that must clear them.
func CallerMarkers() (names, prefixes []string) {
	for _, l := range [][]string{cursorMarkers, piMarkers, grokMarkers, claudeMarkers} {
		names = append(names, l...)
	}
	return names, []string{piMarkerPrefix}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
