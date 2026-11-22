//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/bobmcallan/satelle/internal/workflow"
)

const (
	lowTier  = "reviewer-gemini-4-hydrogen-high"
	highTier = "reviewer-gemini-4-neon-high"
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
		if got := cfg.Review.Panels[gate]["low"]; !slices.Equal(got, []string{lowTier}) {
			t.Errorf("[review.panels.%s] low = %v, want [%s]", gate, got, lowTier)
		}
		if got := cfg.Review.Panels[gate]["high"]; !slices.Equal(got, []string{highTier}) {
			t.Errorf("[review.panels.%s] high = %v, want [%s]", gate, got, highTier)
		}
		for _, wf := range []string{"delivery", "chore"} {
			g, ok := routes[wf].Gates[gate]
			if !ok {
				t.Errorf("%s has no gate %s", wf, gate)
				continue
			}
			if !slices.Equal(g.Judges(), []string{highTier}) {
				t.Errorf("%s gates.%s judges %v, want [%s] so an item with no risk tag is never judged more lightly", wf, gate, g.Judges(), highTier)
			}
			resolved, tier, err := cfg.Review.Resolve(gate, g, nil)
			if err != nil || tier != "" || !slices.Equal(resolved.Judges(), []string{highTier}) {
				t.Errorf("%s gates.%s with no risk tag resolves to %v (tier %q, err %v), want [%s]", wf, gate, resolved.Judges(), tier, err, highTier)
			}
			for tag, want := range map[string]string{"low": lowTier, "high": highTier} {
				resolved, tier, err := cfg.Review.Resolve(gate, g, []string{tag})
				if err != nil || tier != tag || !slices.Equal(resolved.Judges(), []string{want}) {
					t.Errorf("%s gates.%s at risk:%s resolves to %v (tier %q, err %v), want [%s]", wf, gate, tag, resolved.Judges(), tier, err, want)
				}
			}
		}
	}
}

func TestRiskAssessRatesBeforeTheJudgesItPicks(t *testing.T) {
	routes, err := workflow.Load(filepath.Join("..", ".satelle", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		wf, from, to  string
		after, before []string
	}{
		{"delivery", "plan", "in_progress", []string{"binary-scope"}, []string{"security", "plan-review"}},
		{"chore", "backlog", "in_progress", []string{"story-review"}, nil},
	} {
		w := routes[c.wf]
		g, ok := w.Gates["risk-assess"]
		if !ok {
			t.Fatalf("%s has no risk-assess gate", c.wf)
		}
		if !slices.Contains(g.After, "satelle-risk-tier-record") {
			t.Errorf("%s gates.risk-assess after %v lacks satelle-risk-tier-record", c.wf, g.After)
		}
		var order []string
		for _, tr := range w.Transitions {
			if tr.To == c.to && slices.Contains(tr.From, c.from) {
				for _, grp := range tr.Groups {
					order = append(order, grp.Gates...)
				}
			}
		}
		i := slices.Index(order, "risk-assess")
		if i < 0 {
			t.Errorf("%s %s -> %s does not run risk-assess (%v)", c.wf, c.from, c.to, order)
			continue
		}
		for _, a := range c.after {
			if j := slices.Index(order, a); j < 0 || j > i {
				t.Errorf("%s %s -> %s: risk-assess does not run after %s (%v)", c.wf, c.from, c.to, a, order)
			}
		}
		for _, b := range c.before {
			if j := slices.Index(order, b); j < 0 || j < i {
				t.Errorf("%s %s -> %s: risk-assess does not run before %s (%v)", c.wf, c.from, c.to, b, order)
			}
		}
	}
}

func riskRecordScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", ".satelle", "skills", "satelle-risk-tier-record.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	start := strings.Index(body, "```sh\n")
	end := strings.LastIndex(body, "```")
	if start < 0 || end <= start {
		t.Fatal("satelle-risk-tier-record.md has no sh block")
	}
	p := filepath.Join(t.TempDir(), "record.sh")
	writeFile(t, p, body[start+len("```sh\n"):end], 0o755)
	return p
}

func riskTags(s *sandbox, id string) []string {
	var tags []string
	for _, tag := range s.get(id).Tags {
		if strings.HasPrefix(tag, "risk:") {
			tags = append(tags, tag)
		}
	}
	return tags
}

func TestRiskTierRecordWritesOneRiskTag(t *testing.T) {
	script := riskRecordScript(t)
	s := newSandbox(t)
	s.must("init")
	cfg, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/satelle.toml", string(cfg)+"\n[review]\ntier_tag = \"risk\"\n")
	id := s.create("story", "--title", "rate me", "--tag", "keep:me").ID
	s.env["SATELLE_ITEM"] = id
	for _, c := range []struct{ name, stdin, want string }{
		{"low", `{"result":"accept","notes":"TIER: low\nwhy"}`, "risk:low"},
		{"high replaces low", `{"result":"accept","notes":"TIER: high\nwhy"}`, "risk:high"},
		{"back to low", `{"result":"accept","notes":"TIER: low\nwhy"}`, "risk:low"},
		{"a later line cannot rate", `{"result":"accept","notes":"see the plan\nTIER: low"}`, "risk:high"},
		{"the first line only", `{"result":"accept","notes":"TIER: high\nquoted: TIER: low"}`, "risk:high"},
		{"a panel member's first line", `{"result":"accept","notes":"panel (all): 1 of 1 accept","members":[{"notes":"TIER: low\nwhy"}]}`, "risk:low"},
		{"unreadable", `{"result":"accept","notes":""}`, "risk:high"},
		{"not json", "garbage", "risk:high"},
	} {
		r := s.exec("bash", c.stdin, script)
		if r.code != 0 {
			t.Fatalf("%s: exit %d, want 0 (the rating never refuses)\n%s%s", c.name, r.code, r.out, r.err)
		}
		if got := riskTags(s, id); !slices.Equal(got, []string{c.want}) {
			t.Fatalf("%s: risk tags %v, want [%s]\n%s", c.name, got, c.want, r.out)
		}
		if !slices.Contains(s.get(id).Tags, "keep:me") {
			t.Fatalf("%s: the item lost its other tags: %v", c.name, s.get(id).Tags)
		}
	}
	s.env["SATELLE_ITEM"] = "sto_00000000"
	if r := s.exec("bash", `{"result":"accept","notes":"TIER: low"}`, script); r.code == 0 {
		t.Fatalf("an item whose tags cannot be read was not refused:\n%s%s", r.out, r.err)
	}
}
