package agentvalidate

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/health"
)

// wfWithHook builds a minimal workflow doc whose frontmatter carries the given
// lines and whose DOT is a trivial valid graph.
func wfWithHook(fm string) docindex.Doc {
	return docindex.Doc{
		Kind: "workflows", Name: "w",
		Body: "---\nname: done\ntype: workflow\nscope: system\ndescription: fixture\n" + fm + "---\n\n" +
			"## *\n- raised\n- closed\n",
	}
}

// healthyAgents is an agents layer whose reviewer is a real isolated read-only
// binding — the shape a hook allocation is judged against.
func healthyAgents() config.AgentsConfig {
	return config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop", Role: config.RoleAgent},
		Reviewer: config.AgentBinding{
			Role:    config.RoleReviewer,
			Command: agentcli.DefaultClaudeCommand,
			Tools:   "Read,Grep,Glob",
		},
	}
}

// TestFindingsClassifyByProducingSite guards the rule that an id comes from the
// CHECK that produced it, never from matching the message text.
func TestFindingsClassifyByProducingSite(t *testing.T) {
	agents := healthyAgents()
	agents.Agents = map[string]config.AgentBinding{
		"bad": {Role: config.RoleReviewer, Command: "some-cli --model {model}"}, // omits {system}
	}
	r := Validate(agents, nil, nil)
	var found bool
	for _, f := range r.Findings {
		if f.ID == health.IDAgentsBinding && strings.Contains(f.Detail, "{system}") {
			found = true
			if f.Artifact != "bad" || f.Remediation == "" {
				t.Errorf("binding finding should name its section and a fix: %+v", f)
			}
		}
	}
	if !found {
		t.Errorf("a malformed command must be an %s finding: %+v", health.IDAgentsBinding, r.Findings)
	}
}
