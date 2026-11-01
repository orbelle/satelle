package agentvalidate

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
)

// interface = "pi-rpc" in validate (sty_79ce4858): validate builds the runner
// through RunnerFromBinding as dispatch does, so a template that would not start
// cold is a binding problem naming the missing flags.

func piRPCAgents(command string) config.AgentsConfig {
	return config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Role: config.RoleReviewer},
		Agents: map[string]config.AgentBinding{
			"planner": {Role: config.RoleAgent, Interface: config.InterfacePiRPC, Command: command, Tools: "read,grep,find,ls"},
		},
	}
}

func piRPCGrant(t *testing.T, r Report) Grant {
	t.Helper()
	for _, g := range r.Grants {
		if g.Name == "planner" {
			return g
		}
	}
	t.Fatal("no grant reported for the pi-rpc planner")
	return Grant{}
}

func TestValidate_PiRPCColdBindingIsClean(t *testing.T) {
	r := Validate(piRPCAgents(agentcli.PiRPCCommand), nil, nil)
	for _, p := range r.Problems {
		if strings.Contains(p, "[planner]") {
			t.Errorf("a cold pi-rpc binding is a problem: %s", p)
		}
	}
	g := piRPCGrant(t, r)
	if g.Interface != config.InterfacePiRPC || g.Backend != "isolated:pi" {
		t.Errorf("grant = %+v, want interface=pi-rpc backend isolated:pi", g)
	}
	// sty_58a7bdfe: satelle hands no harness a turn limit, so a grant names none.
	if strings.Contains(g.Notes, "turn budget") {
		t.Errorf("notes = %q, want no turn budget", g.Notes)
	}
}

// sty_58a7bdfe AC3: {max_turns} is removed, so a command still carrying it is a
// binding problem rather than a literal token handed to the harness.
func TestValidate_MaxTurnsPlaceholderIsAProblem(t *testing.T) {
	ac := piRPCAgents(agentcli.PiRPCCommand)
	ac.Agents["coder"] = config.AgentBinding{Role: config.RoleAgent, Command: "claude -p --output-format json --max-turns {max_turns}", Tools: "Read"}
	r := Validate(ac, nil, nil)
	joined := strings.Join(r.Problems, "\n")
	if !strings.Contains(joined, "[coder]") || !strings.Contains(joined, config.RemovedMaxTurnsPlaceholder) {
		t.Fatalf("problems = %v, want [coder] refused naming %s", r.Problems, config.RemovedMaxTurnsPlaceholder)
	}
}

func TestValidate_PiRPCWarmBindingNamesTheMissingFlag(t *testing.T) {
	warm := strings.Replace(agentcli.PiRPCCommand, " --no-session", "", 1)
	r := Validate(piRPCAgents(warm), nil, nil)
	joined := strings.Join(r.Problems, "\n")
	if r.OK() || !strings.Contains(joined, "[planner]") || !strings.Contains(joined, "--no-session") {
		t.Fatalf("problems = %v, want the planner refused naming --no-session", r.Problems)
	}
	if g := piRPCGrant(t, r); g.Backend != "invalid" {
		t.Errorf("backend = %q, want invalid", g.Backend)
	}
}
