package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type callerCapture struct {
	Harness string `json:"harness"`
	CLI     string `json:"cli"`
	Version string `json:"version"`
	Hook    struct {
		Label string   `json:"label"`
		Env   []string `json:"env"`
	} `json:"hook"`
	Shell struct {
		Env []string `json:"env"`
	} `json:"shell"`
}

func callerFixture(t *testing.T, harness string) callerCapture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "caller-"+harness+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var c callerCapture
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("caller-%s.json: %v", harness, err)
	}
	if c.Harness != harness || c.Version == "" || len(c.Hook.Env) == 0 || len(c.Shell.Env) == 0 {
		t.Fatalf("caller-%s.json is not a capture of %s: %+v", harness, harness, c)
	}
	return c
}

func joinEnv(envs ...[]string) []string {
	var out []string
	for _, e := range envs {
		out = append(out, e...)
	}
	return out
}

func TestCallerFrom(t *testing.T) {
	cursor, pi, grok := callerFixture(t, Cursor), callerFixture(t, Pi), callerFixture(t, Grok)
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"claude alone", []string{"CLAUDECODE=1"}, Claude},
		{"cursor hook call", cursor.Hook.Env, Cursor},
		{"cursor shell", cursor.Shell.Env, Cursor},
		{"pi hook call", pi.Hook.Env, Pi},
		{"pi shell", pi.Shell.Env, Pi},
		{"grok hook call", grok.Hook.Env, ""},
		{"grok shell", grok.Shell.Env, Grok},
		{"cursor inside claude", joinEnv([]string{"CLAUDECODE=1"}, cursor.Shell.Env), Cursor},
		{"grok inside claude", joinEnv([]string{"CLAUDECODE=1"}, grok.Shell.Env), Grok},
		{"pi inside claude", joinEnv(pi.Shell.Env, []string{"CLAUDECODE=1"}), Pi},
		{"cursor and grok", joinEnv(cursor.Shell.Env, grok.Shell.Env), ""},
		{"cursor, grok and claude", joinEnv(cursor.Shell.Env, grok.Shell.Env, []string{"CLAUDECODE=1"}), ""},
		{"pi and cursor", joinEnv(pi.Shell.Env, cursor.Hook.Env), ""},
		{"no marker", []string{"HOME=/h", "PATH=/bin"}, ""},
		{"empty value is unset", []string{"CLAUDECODE=", "GROK_AGENT=  "}, ""},
		{"look-alikes are not markers", []string{"CURSOR_VERSION=1", "CURSOR_PROJECT_DIR=/p", "CURSOR_CONFIG_DIR=/c", "XCURSOR_SIZE=24", "CLAUDE_PROJECT_DIR=/p", "CLAUDE_CODE_OAUTH_TOKEN=x", "GROK_AGENT_ID=7"}, ""},
	} {
		if got := CallerFrom(c.env); got != c.want {
			t.Errorf("%s: CallerFrom(%v) = %q, want %q", c.name, c.env, got, c.want)
		}
	}
	for _, c := range []callerCapture{cursor, pi, grok} {
		for _, env := range [][]string{c.Hook.Env, c.Shell.Env} {
			if got := CallerFrom(env); got != "" && got != c.Harness {
				t.Errorf("%s %s environment detected as %q", c.Harness, c.Version, got)
			}
		}
	}
}

func TestCallerMarkersAreCaptured(t *testing.T) {
	seen := map[string]map[string]bool{}
	for _, h := range []string{Cursor, Pi, Grok} {
		c := callerFixture(t, h)
		seen[h] = map[string]bool{}
		for _, kv := range joinEnv(c.Hook.Env, c.Shell.Env) {
			name, value, _ := strings.Cut(kv, "=")
			if strings.TrimSpace(value) != "" {
				seen[h][name] = true
			}
		}
	}
	names, prefixes := CallerMarkers()
	for _, n := range names {
		h := CallerFrom([]string{n + "=1"})
		if h == Claude {
			continue
		}
		if !seen[h][n] {
			t.Errorf("marker %s (%s) is not set in testdata/caller-%s.json", n, h, h)
		}
	}
	for _, p := range prefixes {
		h := CallerFrom([]string{p + "X=1"})
		found := false
		for name := range seen[h] {
			found = found || strings.HasPrefix(name, p)
		}
		if !found {
			t.Errorf("marker prefix %s (%s) matches no name in testdata/caller-%s.json", p, h, h)
		}
	}
}

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
