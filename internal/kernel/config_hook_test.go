package kernel

import (
	"path/filepath"
	"strings"
	"testing"
)

// [hook] refresh: unset and true refresh, false opts the repo out; an
// unknown [hook] key is still an error.
func TestLoadSettingsReadsHookRefresh(t *testing.T) {
	for body, want := range map[string]bool{
		"":                          true,
		"[hook]\nrefresh = true\n":  true,
		"[hook]\nrefresh = false\n": false,
	} {
		root := t.TempDir()
		write(t, filepath.Join(root, SettingsFile), body)
		st, err := LoadSettings(root)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if got := st.Hook.Refreshes(); got != want {
			t.Errorf("%q: Refreshes() = %v, want %v", body, got, want)
		}
	}
	root := t.TempDir()
	write(t, filepath.Join(root, SettingsFile), "[hook]\nrefreshh = false\n")
	if _, err := LoadSettings(root); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("unknown [hook] key: %v, want an unknown key error", err)
	}
}
