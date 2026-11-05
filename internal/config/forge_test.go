package config

import (
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

// [forge] release_heads and release_files override the release PR's head
// prefixes and the paths it may change; unset or blank is release/ and the
// version stamp with the changelogs.
func TestForgeReleaseKeysResolve(t *testing.T) {
	for _, tc := range []struct {
		name, toml   string
		heads, files []string
	}{
		{"unset", "[forge]\n", []string{"release/"}, []string{".version", "CHANGELOG.md", "internal/verb/embedded/CHANGELOG.md"}},
		{"set", "[forge]\nrelease_heads = [\" rel/ \", \"\"]\nrelease_files = [\"VERSION\", \"docs/*.md\"]\n", []string{"rel/"}, []string{"VERSION", "docs/*.md"}},
		{"all blank", "[forge]\nrelease_heads = [\" \"]\nrelease_files = [\"\"]\n", []string{"release/"}, []string{".version", "CHANGELOG.md", "internal/verb/embedded/CHANGELOG.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			if _, err := toml.Decode(tc.toml, &c); err != nil {
				t.Fatal(err)
			}
			if got := c.Forge.ResolvedReleaseHeads(); !reflect.DeepEqual(got, tc.heads) {
				t.Errorf("ResolvedReleaseHeads = %q, want %q", got, tc.heads)
			}
			if got := c.Forge.ResolvedReleaseFiles(); !reflect.DeepEqual(got, tc.files) {
				t.Errorf("ResolvedReleaseFiles = %q, want %q", got, tc.files)
			}
		})
	}
	var c Config
	c.Forge.ResolvedReleaseFiles()[0] = "mutated"
	if DefaultForgeReleaseFiles[0] != ".version" {
		t.Errorf("ResolvedReleaseFiles returned the default slice itself")
	}
}
