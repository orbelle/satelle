package agentvalidate

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// governingWF is a route whose wildcard lane dispatches a planner and a coder,
// and — when withSubstrate — a [substrate] lane that dispatches neither.
func governingWF(withSubstrate bool) []docindex.Doc {
	done := `["*"]
obligations = ["raised", "planned", "coded", "closed"]
`
	if withSubstrate {
		done += `
[substrate]
obligations = ["raised", "authored", "substrate-verified"]
`
	}
	return routeDocs(done, `[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "planner"
skills = ["plan"]
requires = ["raised"]

[coded]
status = "in_progress"
agent = "coder"
skills = ["coder"]
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[authored]
status = "in_progress"
requires = ["raised"]

[substrate-verified]
status = "done"
terminal = true
requires = ["authored"]
`)
}

// governingAgents gives the wildcard lane's planner and coder pi-named tools
// under a Claude command: the id4me/satelle-server shape, no context channel.
func governingAgents() config.AgentsConfig {
	return config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
		Agents: map[string]config.AgentBinding{
			"planner": {Command: agentcli.DefaultClaudeCommand, Tools: "read,grep,find,ls", Model: "opus"},
			"coder":   {Command: agentcli.DefaultClaudeCommand, Tools: "read,bash,edit,write", Model: "opus"},
		},
	}
}

// A node allocation on a lane the story never walks does not refuse it
// (sty_259572f6 AC1); the lane the story does walk, and the unscoped check,
// still report it.
func TestValidateGoverning_ScopesNodeAllocToGoverningRoute(t *testing.T) {
	agents := governingAgents()
	substrate := workitem.Item{ID: "sty_sub", Kind: workitem.KindStory, Category: "substrate"}
	bug := workitem.Item{ID: "sty_bug", Kind: workitem.KindStory, Category: "bug"}

	r := ValidateGoverning(agents, nil, governingWF(true), substrate)
	if got := findingWith(r.Problems, "no context channel"); got != "" || !r.OK() {
		t.Fatalf("substrate story on its own lane: want OK, got problems=%v", r.Problems)
	}
	for _, w := range r.Warnings {
		if findingWith([]string{w}, "orphaned") != "" {
			t.Errorf("a scoped check must not call another lane's seats orphaned: %s", w)
		}
	}

	r = ValidateGoverning(agents, nil, governingWF(true), bug)
	if findingWith(r.Problems, "no context channel") == "" || r.OK() {
		t.Errorf("bug story walks the * lane: want its channel problem, got %v", r.Problems)
	}

	// No [substrate] table: the substrate story falls to * and walks those seats.
	r = ValidateGoverning(agents, nil, governingWF(false), substrate)
	if findingWith(r.Problems, "no context channel") == "" || r.OK() {
		t.Errorf("substrate story governed by *: want its channel problem, got %v", r.Problems)
	}

	r = Validate(agents, nil, governingWF(true))
	if findingWith(r.Problems, "no context channel") == "" || r.OK() {
		t.Errorf("unscoped Validate must still report the * lane, got %v", r.Problems)
	}
}

// A broken BINDING refuses whatever route the story walks: only node
// allocations are scoped.
func TestValidateGoverning_BindingChecksStayUnscoped(t *testing.T) {
	agents := governingAgents()
	agents.Agents["planner"] = config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Tools: "Read", Env: map[string]string{"K": "${MISSING}"}}
	substrate := workitem.Item{ID: "sty_sub", Kind: workitem.KindStory, Category: "substrate"}
	if r := ValidateGoverning(agents, nil, governingWF(true), substrate); r.OK() {
		t.Error("an unresolved binding variable must still refuse a scoped check")
	}
}
