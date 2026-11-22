//go:build integration

package tests

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestAnUntieredItemIsJudgedByTheHighTierPanel(t *testing.T) {
	var cfg struct {
		Review workflow.Review `toml:"review"`
	}
	if _, err := toml.DecodeFile(filepath.Join("..", ".satelle", "satelle.toml"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Review.TierTag != "risk" {
		t.Fatalf("[review] tier_tag = %q, want risk", cfg.Review.TierTag)
	}
	routes, err := workflow.Load(filepath.Join("..", ".satelle", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gate := range []string{"security", "code-ac"} {
		high := cfg.Review.Panels[gate]["high"]
		if len(high) == 0 {
			t.Errorf("[review.panels.%s] has no high panel", gate)
			continue
		}
		for _, wf := range []string{"delivery", "chore"} {
			g, ok := routes[wf].Gates[gate]
			if !ok {
				t.Errorf("%s has no gate %s", wf, gate)
				continue
			}
			if !slices.Equal(g.Judges(), high) {
				t.Errorf("%s gates.%s judges %v, want the high tier %v so an item with no risk tag is never judged more lightly", wf, gate, g.Judges(), high)
			}
			resolved, tier, err := cfg.Review.Resolve(gate, g, nil)
			if err != nil || tier != "" || !slices.Equal(resolved.Judges(), high) {
				t.Errorf("%s gates.%s with no risk tag resolves to %v (tier %q, err %v), want %v", wf, gate, resolved.Judges(), tier, err, high)
			}
		}
	}
}

func TestRiskAssessRatesBeforeTheJudgesItPicks(t *testing.T) {
	routes, err := workflow.Load(filepath.Join("..", ".satelle", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ wf, from, to, before string }{
		{"delivery", "plan", "in_progress", "security"},
		{"chore", "backlog", "in_progress", ""},
	} {
		w := routes[c.wf]
		g, ok := w.Gates["risk-assess"]
		if !ok {
			t.Fatalf("%s has no risk-assess gate", c.wf)
		}
		if !slices.Contains(g.After, "satelle-risk-tier-record") {
			t.Errorf("%s gates.risk-assess after %v lacks satelle-risk-tier-record", c.wf, g.After)
		}
		found := false
		for _, tr := range w.Transitions {
			if tr.To != c.to || !slices.Contains(tr.From, c.from) {
				continue
			}
			for _, grp := range tr.Groups {
				i := slices.Index(grp.Gates, "risk-assess")
				if i < 0 {
					continue
				}
				found = true
				if c.before != "" {
					if j := slices.Index(grp.Gates, c.before); j >= 0 && j < i {
						t.Errorf("%s %s -> %s: %s runs before risk-assess", c.wf, c.from, c.to, c.before)
					}
				}
			}
		}
		if !found {
			t.Errorf("%s %s -> %s does not run risk-assess", c.wf, c.from, c.to)
		}
	}
}
