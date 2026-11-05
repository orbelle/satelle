package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// sty_992cffc6 AC6 — the lock list is decided by key PRESENCE in the raw
// committed file, so an absent key (default lock) and `= []` (documented
// opt-out) are separate outcomes. Each case asserts the exact pair.
func TestParseLockSubstratePathsSplitsAbsentFromExplicitEmpty(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
		optOut  bool
	}{
		{"no gate section", "[review]\ngate_create = false\n", []string{".satelle/"}, false},
		{"gate section without the key", "[gate]\nedit_exempt_paths = [\".satelle/\"]\n", []string{".satelle/"}, false},
		{"empty file", "", []string{".satelle/"}, false},
		{"key with entries", "[gate]\nlock_substrate_paths = [\".satelle/\", \"docs/process/\"]\n", []string{".satelle/", "docs/process/"}, false},
		{"entries are trimmed", "[gate]\nlock_substrate_paths = [\"  .satelle/ \"]\n", []string{".satelle/"}, false},
		{"multi-line array", "[gate]\nlock_substrate_paths = [\n  \".satelle/\",\n  \"policy/\",\n]\n", []string{".satelle/", "policy/"}, false},
		{"key = []", "[gate]\nlock_substrate_paths = []\n", nil, true},
		{"all-blank entries", "[gate]\nlock_substrate_paths = [\"\", \"  \"]\n", nil, true},
		{"empty multi-line array", "[gate]\nlock_substrate_paths = [\n]\n", nil, true},
		{"key in another table is not the gate key", "[other]\nlock_substrate_paths = []\n", []string{".satelle/"}, false},
		// A damaged file or a value that is not a list of strings is never read as
		// the operator's opt-out.
		{"syntax error fails closed", "[gate\nlock_substrate_paths = []\n", []string{".satelle/"}, false},
		{"not a list fails closed", "[gate]\nlock_substrate_paths = \"\"\n", []string{".satelle/"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, optOut := ParseLockSubstratePaths(tc.content)
			if !reflect.DeepEqual(got, tc.want) || optOut != tc.optOut {
				t.Fatalf("ParseLockSubstratePaths = (%#v, %v), want (%#v, %v)", got, optOut, tc.want, tc.optOut)
			}
		})
	}
}

// The key is not a decoded field, so a repo that pins it still loads, and the
// settings schema carries no row for a value that would only ever read empty.
func TestPinnedLockKeyLoadsAndHasNoSettingsRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "satelle.toml")
	if err := os.WriteFile(path, []byte("[gate]\nlock_substrate_paths = [\"a/\", \"b/\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err != nil {
		t.Fatalf("a pinned lock_substrate_paths must not break Load: %v", err)
	}
	if _, ok := SettingByID("gate.lock_substrate_paths"); ok {
		t.Error("settings schema must not list gate.lock_substrate_paths")
	}
}

func TestParseEditExemptGlobsSplitsDamagedFromEmpty(t *testing.T) {
	if got, ok := ParseEditExemptGlobs("[gate]\nedit_exempt_globs = [\" a \", \"\"]\n"); !ok || !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("entries = (%v, %v), want ([a], true)", got, ok)
	}
	if got, ok := ParseEditExemptGlobs(""); !ok || got != nil {
		t.Errorf("empty = (%v, %v), want (nil, true)", got, ok)
	}
	if _, ok := ParseEditExemptGlobs("[gate\n"); ok {
		t.Error("a syntax error must report ok=false")
	}
}

// sty_32431a16 AC5 — the agents layer is the agents*.toml bindings directly under
// .satelle/workflows/, and nothing else that happens to share the name.
func TestIsAgentsLayerPath(t *testing.T) {
	for rel, want := range map[string]bool{
		".satelle/workflows/agents.toml":        true,
		".satelle/workflows/agents.local.toml":  true,
		".satelle/workflows/step.toml":          false,
		".satelle/workflows/x/agents.toml":      false,
		".satelle/skills/agents.toml":           false,
		"agents.local.toml":                     false,
		".satelle/workflows/agents.local.toml~": false,
		"x/.satelle/workflows/agents.toml":      false,
	} {
		if got := IsAgentsLayerPath(rel); got != want {
			t.Errorf("IsAgentsLayerPath(%q) = %v, want %v", rel, got, want)
		}
	}
}
