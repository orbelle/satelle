package kernel

import (
	"reflect"
	"testing"
)

func TestAgentUsesNamesEveryStateAndGate(t *testing.T) {
	const tiers = "[review]\ntier_tag = \"risk\"\n[review.panels.review]\nhigh = [\"r3\"]\n"
	root := fallbackRoot(t, `agent = "r1"`, tiers, fallbackLines(`r1 = "r2"`), "")
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for name, uses := range c.AgentUses() {
		for _, u := range uses {
			got[name] = append(got[name], u.String())
		}
	}
	want := map[string][]string{
		"doer": {"workflow flow state work (performer)"},
		"r1":   {"workflow flow gate review (judge)"},
		"r2":   {"workflow flow gate review (fallback of r1)"},
		"r3":   {"workflow flow gate review (judge)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uses %v, want %v", got, want)
	}
	if again := c.AgentUses(); !reflect.DeepEqual(again, c.AgentUses()) {
		t.Fatal("uses are not deterministic")
	}
}
