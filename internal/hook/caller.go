package hook

import (
	"os"
	"strings"
)

var (
	cursorMarkers    = []string{"CURSOR_AGENT", "CURSOR_INVOKED_AS"}
	piMarkers        = []string{"PI_CODING_AGENT"}
	piMarkerPrefix   = "PI_SESSION_"
	grokMarkers      = []string{"GROK_AGENT"}
	claudeMarkers    = []string{"CLAUDECODE"}
	callerOverClaude = []string{Cursor, Pi, Grok}
)

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

func Caller() string { return CallerFrom(os.Environ()) }

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
