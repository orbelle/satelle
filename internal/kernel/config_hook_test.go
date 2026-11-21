package kernel

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

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
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, SettingsFile), "[workflows]\ndefault = \"default\"\n[hook]\nrefreshh = false\n")
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("unknown [hook] key failed load: %v", err)
	}
	if !c.Settings.Hook.Refreshes() {
		t.Error("unknown [hook] key changed Refreshes() default")
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Errorf("Validate reported problems for unknown [hook] key: %v", errs)
	}
	want := []workflow.UnknownKey{{File: ".satelle/satelle.toml", Table: "hook", Key: "refreshh", Value: "false"}}
	if !slices.Equal(c.UnknownKeys, want) {
		t.Errorf("UnknownKeys = %+v, want %+v", c.UnknownKeys, want)
	}
}
