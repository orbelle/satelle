//go:build integration && unix

package tests

import (
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestRepoGatesNameReviewersAsAPanel(t *testing.T) {
	files, err := filepath.Glob("../.satelle/workflows/*.toml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no repo workflows: %v", err)
	}
	panels := 0
	for _, f := range files {
		rel := filepath.Join(".satelle", "workflows", filepath.Base(f))
		var wf struct {
			Gates map[string]struct {
				Agent   string   `toml:"agent"`
				Panel   []string `toml:"panel"`
				Combine string   `toml:"combine"`
			} `toml:"gates"`
		}
		if _, err := toml.Decode(repoText(t, rel), &wf); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for name, g := range wf.Gates {
			if g.Agent != "" {
				t.Errorf("%s: gate %s names agent %q; name it as panel = [%q]", rel, name, g.Agent, g.Agent)
			}
			if len(g.Panel) > 0 {
				panels++
				if g.Combine == "" {
					t.Errorf("%s: gate %s has a panel and no combine", rel, name)
				}
			}
		}
	}
	if panels == 0 {
		t.Fatal("no judged gate found in the repo workflows")
	}
}
