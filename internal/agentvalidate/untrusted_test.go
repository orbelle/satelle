package agentvalidate

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/health"
)

// `untrusted_input = true` on a step makes validate refuse any performer or
// reviewer seat on it that is not held read-only — one finding per cause, naming
// the binding and the step — on a story step and on a container step alike. A
// step without the attribute is unaffected.

const untrustedDone = `["*"]
obligations = ["raised", "planned", "closed"]

[epic-parent]
obligations = ["raised", "ready", "closed"]
`

func untrustedSteps(attr string) string {
	return `[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "perf"
skills = ["plan"]
reviewers = ["plan-review"]
reviewer_agent = "rev"
` + attr + `
requires = ["raised"]

[ready]
status = "ready"
agent = "perf"
waits_on_children = true
skills = ["ready"]
reviewers = ["ready-review"]
reviewer_agent = "rev"
` + attr + `
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["raised"]
`
}

const (
	piCmd     = "pi -p --provider openrouter --tools {tools} --model {model} {payload}"
	piRead    = "read,grep,find,ls"
	cursorCmd = "cursor-agent -p --output-format json"
)

func untrustedAgents(perf, rev config.AgentBinding) config.AgentsConfig {
	perf.Role, rev.Role = config.RoleAgent, config.RoleReviewer
	return config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop", Role: config.RoleAgent},
		Agents:   map[string]config.AgentBinding{"perf": perf, "rev": rev},
	}
}

var (
	piSeat = config.AgentBinding{Command: piCmd, Tools: piRead, Model: "openrouter/minimax/minimax-m3"}
)

// untrustedFindings are the step.untrusted_input findings of a validation run.
func untrustedFindings(r Report) []string {
	var out []string
	for _, f := range r.Findings {
		if f.ID == health.IDStepUntrusted {
			out = append(out, f.Detail)
		}
	}
	return out
}

// seatCase is one binding on a seat and the causes it must be refused for, each
// naming one finding ("" wants none: the binding is read-only).
type seatCase struct {
	name    string
	binding config.AgentBinding
	causes  []string
}

func runSeatCases(t *testing.T, seat string, cases []seatCase) {
	t.Helper()
	for _, step := range []string{"planned", "ready"} {
		status := map[string]string{"planned": "plan", "ready": "ready"}[step]
		for _, tc := range cases {
			t.Run(step+"/"+tc.name, func(t *testing.T) {
				perf, rev := piSeat, piSeat
				if seat == "perf" {
					perf = tc.binding
				} else {
					rev = tc.binding
				}
				r := Validate(untrustedAgents(perf, rev), nil, routeDocs(untrustedDone, untrustedSteps("untrusted_input = true")))
				// Only the step under test: each fixture step names the same seats.
				var got []string
				for _, d := range untrustedFindings(r) {
					if strings.Contains(d, `step "`+status+`"`) {
						got = append(got, d)
					}
				}
				if len(got) != len(tc.causes) {
					t.Fatalf("want %d finding(s) %v, got %d: %v", len(tc.causes), tc.causes, len(got), got)
				}
				for i, want := range tc.causes {
					for _, part := range []string{"agent=" + seat, want} {
						if !strings.Contains(got[i], part) {
							t.Errorf("finding %d = %q, want it to contain %q", i, got[i], part)
						}
					}
				}
				if seat == "perf" {
					// The channel conflict replaces the general missing-channel finding.
					for _, p := range r.Problems {
						if strings.Contains(p, status) && strings.Contains(p, "has no context channel") {
							t.Errorf("a second finding for the same cause: %s", p)
						}
					}
				}
			})
		}
	}
}

// A panel's seats are the step's reviewer seats: each is judged.
func TestUntrustedStep_PanelSeats(t *testing.T) {
	steps := strings.Replace(untrustedSteps("untrusted_input = true"), `reviewer_agent = "rev"`, `panel = ["rev", "rev2"]`+"\n"+`combine = "fold"`, -1)
	agents := untrustedAgents(piSeat, piSeat)
	agents.Agents["rev2"] = config.AgentBinding{Role: config.RoleReviewer, Command: agentcli.DefaultClaudeCommand, Tools: "Read,Bash"}
	got := strings.Join(untrustedFindings(Validate(agents, nil, routeDocs(untrustedDone, steps))), "\n")
	if !strings.Contains(got, "reviewer agent=rev2") || strings.Contains(got, "agent=rev:") {
		t.Errorf("want only the Bash panel seat refused, got:\n%s", got)
	}
}

// The attribute is opt-in: without it the same seats validate as before, and the
// general missing-channel finding stays the one a read-only claude performer gets.
func TestUntrustedStep_AttributeAbsentIsUnaffected(t *testing.T) {
	for name, perf := range map[string]config.AgentBinding{
		"satelle cli": {Command: agentcli.DefaultClaudeCommand, Tools: "Bash(satelle:*)"},
		"read only":   {Command: agentcli.DefaultClaudeCommand, Tools: "Grep,Glob"},
	} {
		t.Run(name, func(t *testing.T) {
			r := Validate(untrustedAgents(perf, config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Tools: "Bash(satelle:*)"}), nil,
				routeDocs(untrustedDone, untrustedSteps("")))
			if got := untrustedFindings(r); len(got) != 0 {
				t.Errorf("a step without untrusted_input must not be judged: %v", got)
			}
			hasChannel := strings.Contains(strings.Join(r.Problems, "\n"), "has no context channel")
			if want := name == "read only"; hasChannel != want {
				t.Errorf("general channel finding = %v, want %v: %v", hasChannel, want, r.Problems)
			}
		})
	}
}
