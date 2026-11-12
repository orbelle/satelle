package hook

import "testing"

// CallerFrom tells the calling harness from its session markers. The marker
// names are those each harness's own binary carries (checked on cursor-agent
// 2026.11.11, pi 1.5.2 and grok, 2026-11-12; CLAUDECODE observed set to 1 in a
// Claude Code shell). The cursor, pi and grok rows were not observed in a
// live session of those harnesses.
func TestCallerFrom(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"claude alone", []string{"CLAUDECODE=1"}, Claude},
		{"cursor agent", []string{"CURSOR_AGENT=1"}, Cursor},
		{"cursor invoked-as", []string{"CURSOR_INVOKED_AS=cursor-agent"}, Cursor},
		{"pi", []string{"PI_CODING_AGENT=true"}, Pi},
		{"pi session name", []string{"PI_SESSION_ID=abc"}, Pi},
		{"grok", []string{"GROK_AGENT=1"}, Grok},
		{"cursor inside claude", []string{"CLAUDECODE=1", "CURSOR_INVOKED_AS=cursor-agent"}, Cursor},
		{"grok inside claude", []string{"CLAUDECODE=1", "GROK_AGENT=1"}, Grok},
		{"pi inside claude", []string{"PI_SESSION_FILE=/tmp/s", "CLAUDECODE=1"}, Pi},
		{"cursor and grok", []string{"CURSOR_AGENT=1", "GROK_AGENT=1"}, ""},
		{"cursor, grok and claude", []string{"CURSOR_AGENT=1", "GROK_AGENT=1", "CLAUDECODE=1"}, ""},
		{"no marker", []string{"HOME=/h", "PATH=/bin"}, ""},
		{"empty value is unset", []string{"CLAUDECODE=", "GROK_AGENT=  "}, ""},
		{"look-alikes are not markers", []string{"CURSOR_VERSION=1", "CURSOR_PROJECT_DIR=/p", "CURSOR_CONFIG_DIR=/c", "XCURSOR_SIZE=24", "CLAUDE_PROJECT_DIR=/p", "CLAUDE_CODE_OAUTH_TOKEN=x", "GROK_AGENT_ID=7"}, ""},
	} {
		if got := CallerFrom(c.env); got != c.want {
			t.Errorf("%s: CallerFrom(%v) = %q, want %q", c.name, c.env, got, c.want)
		}
	}
}

// CallerMarkers names every marker CallerFrom reads, so a test harness that
// clears them leaves no caller.
func TestCallerMarkersCoverCallerFrom(t *testing.T) {
	names, prefixes := CallerMarkers()
	for _, n := range names {
		if CallerFrom([]string{n + "=1"}) == "" {
			t.Errorf("%s is listed as a marker but names no harness", n)
		}
	}
	for _, p := range prefixes {
		if CallerFrom([]string{p + "X=1"}) == "" {
			t.Errorf("prefix %s is listed but names no harness", p)
		}
	}
}
