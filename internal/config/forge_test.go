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
