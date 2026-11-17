package workflow

import (
	"strings"
	"testing"
)

func TestReviewResolve(t *testing.T) {
	r := Review{TierTag: "k", Panels: map[string]map[string][]string{"g": {"t1": {"a"}, "t2": {"b", "c"}}}}
	agentOnly := Gate{Skill: "s", Agent: "x", Carry: true}
	panel := Gate{Skill: "s", Panel: []string{"x", "y"}, Combine: CombineAny}
	for _, c := range []struct {
		name    string
		review  Review
		gate    string
		g       Gate
		tiers   []string
		judges  string
		combine string
		tier    string
		err     bool
	}{
		{"agent-only gate takes the tier panel and all", r, "g", agentOnly, []string{"t2"}, "b,c", CombineAll, "t2", false},
		{"own combine kept", r, "g", panel, []string{"t2"}, "b,c", CombineAny, "t2", false},
		{"one-judge tier", r, "g", panel, []string{"t1"}, "a", CombineAny, "t1", false},
		{"no tier tag", r, "g", panel, nil, "x,y", CombineAny, "", false},
		{"tier with no panel", r, "g", panel, []string{"t9"}, "x,y", CombineAny, "", false},
		{"gate not named", r, "other", agentOnly, []string{"t2"}, "x", "", "", false},
		{"no review table", Review{}, "g", agentOnly, []string{"t2"}, "x", "", "", false},
		{"two tiers", r, "g", panel, []string{"t1", "t2"}, "x,y", CombineAny, "", true},
	} {
		got, tier, err := c.review.Resolve(c.gate, c.g, c.tiers)
		if (err != nil) != c.err {
			t.Errorf("%s: err %v", c.name, err)
		}
		if strings.Join(got.Judges(), ",") != c.judges || got.Combine != c.combine || tier != c.tier {
			t.Errorf("%s: judges %v combine %q tier %q, want %s %q %q", c.name, got.Judges(), got.Combine, tier, c.judges, c.combine, c.tier)
		}
		if got.Skill != c.g.Skill || got.Carry != c.g.Carry {
			t.Errorf("%s: the gate's own skill or carry changed: %+v", c.name, got)
		}
	}
	if _, _, err := r.Resolve("g", panel, []string{"t1", "t2"}); err == nil || !strings.Contains(err.Error(), "t1, t2") {
		t.Errorf("two tiers: %v", err)
	}
}

func TestReviewValidate(t *testing.T) {
	gates := func(name string) (bool, bool) {
		switch name {
		case "judged":
			return true, false
		case "checked":
			return true, true
		}
		return false, false
	}
	agents := func(n string) bool { return n == "a" || n == "b" }
	for _, c := range []struct {
		review Review
		want   string
	}{
		{Review{TierTag: "k", Panels: map[string]map[string][]string{"judged": {"t": {"a", "b"}}}}, ""},
		{Review{}, ""},
		{Review{Panels: map[string]map[string][]string{"judged": {"t": {"a"}}}}, "tier_tag is not"},
		{Review{TierTag: "  "}, "tier_tag is blank"},
		{Review{TierTag: "k:v"}, "holds ':'"},
		{Review{TierTag: "k", Panels: map[string]map[string][]string{"missing": {"t": {"a"}}}}, `no workflow has a gate "missing"`},
		{Review{TierTag: "k", Panels: map[string]map[string][]string{"checked": {"t": {"a"}}}}, "runs a check skill"},
		{Review{TierTag: "k", Panels: map[string]map[string][]string{"judged": {"t": {}}}}, "lists no agent"},
		{Review{TierTag: "k", Panels: map[string]map[string][]string{"judged": {"t": {"a", "a"}}}}, `agent "a" twice`},
		{Review{TierTag: "k", Panels: map[string]map[string][]string{"judged": {"t": {"ghost"}}}}, `agent "ghost" is not in the agents catalog`},
	} {
		var msgs []string
		for _, e := range c.review.Validate(gates, agents) {
			msgs = append(msgs, e.Error())
		}
		all := strings.Join(msgs, "\n")
		if c.want == "" && all != "" || c.want != "" && !strings.Contains(all, c.want) {
			t.Errorf("%+v: problems %q, want %q", c.review, all, c.want)
		}
	}
}

func TestReviewAgents(t *testing.T) {
	r := Review{Panels: map[string]map[string][]string{"g": {"t1": {"b"}, "t2": {"a", "b"}}}}
	if got := strings.Join(r.Agents("g"), ","); got != "a,b" {
		t.Errorf("Agents = %s", got)
	}
	if got := r.Agents("other"); len(got) != 0 {
		t.Errorf("Agents of an unnamed gate = %v", got)
	}
}
