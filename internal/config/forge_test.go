package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

// [forge] pr_heads narrows the branch prefixes a story's PR is found on; unset
// or blank is epic/ and feat/.
func TestForgePRHeadsResolve(t *testing.T) {
	for _, tc := range []struct {
		name, toml string
		want       []string
	}{
		{"unset", "[forge]\nprovider = \"github\"\n", []string{"epic/", "feat/"}},
		{"feat only", "[forge]\npr_heads = [\"feat/\"]\n", []string{"feat/"}},
		{"blank entries", "[forge]\npr_heads = [\" \", \" feat/ \"]\n", []string{"feat/"}},
		{"all blank", "[forge]\npr_heads = [\"\"]\n", []string{"epic/", "feat/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			if _, err := toml.Decode(tc.toml, &c); err != nil {
				t.Fatal(err)
			}
			if got := c.Forge.ResolvedPRHeads(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ResolvedPRHeads = %q, want %q", got, tc.want)
			}
		})
	}
}

// A config that still sets the retired [forge] release_heads and release_files
// loads: the keys are ignored and the rest of [forge] resolves as before.
func TestForgeConfigIgnoresRetiredReleaseKeys(t *testing.T) {
	const text = "[forge]\nprovider = \"github\"\nrelease_heads = [\"release/\"]\nrelease_files = [\".version\"]\n"
	var c Config
	if _, err := toml.Decode(text, &c); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), ".satelle", "satelle.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load of a config with the retired release keys: %v", err)
	}
	for name, f := range map[string]ForgeConfig{"decoded": c.Forge, "loaded": loaded.Forge} {
		if got := f.ResolvedProvider(); got != "github" {
			t.Errorf("%s: ResolvedProvider = %q, want github", name, got)
		}
		if got := f.ResolvedPRHeads(); !reflect.DeepEqual(got, DefaultForgePRHeads) {
			t.Errorf("%s: ResolvedPRHeads = %q, want %q", name, got, DefaultForgePRHeads)
		}
	}
}
