package agentvalidate

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/health"
)

func TestValidate_Healthy(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
		Agents: map[string]config.AgentBinding{
			"planner": {Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "opus"},
		},
	}
	wfs := routeDocs(
		`["*"]
obligations = ["raised", "planned", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "planner"
skills = ["plan"]
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["planned"]
`)
	r := Validate(agents, nil, wfs)
	if !r.OK() {
		t.Fatalf("healthy fixture must have no problems: %v", r.Problems)
	}
	byName := map[string]Grant{}
	for _, g := range r.Grants {
		byName[g.Name] = g
	}
	if byName["executor"].Backend != "in-loop" {
		t.Errorf("executor backend = %q, want in-loop", byName["executor"].Backend)
	}
	if !strings.HasPrefix(byName["reviewer"].Backend, "isolated:") {
		t.Errorf("reviewer backend = %q, want isolated:*", byName["reviewer"].Backend)
	}
	if byName["reviewer"].ReadOnly != true {
		t.Errorf("reviewer should be read-only")
	}
	if byName["planner"].Backend != "isolated:claude" {
		t.Errorf("planner backend = %q, want isolated:claude", byName["planner"].Backend)
	}
}

// TestValidate_GateBindingSection (sty_a476a2f8): effective model is the binding
// that will run the gate; named gate agent= resolves that binding; model= is ignored.
func TestValidate_GateBindingSection(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
		Agents: map[string]config.AgentBinding{
			"planner":       {Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "sonnet", Role: "agent"},
			"reviewer-deep": {Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob", Model: "opus", Role: "reviewer"},
		},
	}
	wfs := routeDocs(
		`["*"]
obligations = ["raised", "planned", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "planner"
skills = ["plan"]
reviewers = ["intent"]
reviewer_agent = "reviewer-deep"
requires = ["raised"]

[closed]
status = "done"
reviewers = ["close"]
reviewer_agent = "reviewer"
terminal = true
requires = ["planned"]

[[gate]]
skill = "est"
agent = "reviewer"
on = ["done"]
`)
	r := Validate(agents, nil, wfs)
	if !r.OK() {
		t.Fatalf("problems: %v", r.Problems)
	}
	by := map[string]GateAllocation{}
	for _, g := range r.Gates {
		key := g.Node + "|" + g.Skill
		by[key] = g
	}
	edge := by["edge:backlog→plan|intent"]
	if edge.Agent != "reviewer-deep" || edge.EffectiveModel != "opus" {
		t.Errorf("edge gate = %+v, want agent=reviewer-deep model=opus", edge)
	}
	closeG := by["edge:plan→done|close"]
	if closeG.Agent != "reviewer" || closeG.EffectiveModel != "grok-4.5" {
		t.Errorf("close edge = %+v, want [reviewer]/grok-4.5", closeG)
	}
	plan := by["plan|plan"]
	if plan.Agent != "planner" || plan.EffectiveModel != "sonnet" {
		t.Errorf("plan node = %+v, want planner/sonnet", plan)
	}
	// A derived route names an always-on gate node for its skill.
	est := by["gate_est|est"]
	if est.EffectiveModel != "grok-4.5" {
		t.Errorf("estimate scoped = %+v, want grok-4.5", est)
	}
	// sty_6ab016dc: named role=reviewer on a gated edge is live — no stale WARN
	// claiming it is a "named perform binding" or that gates fall back to [reviewer].
	for _, w := range r.Warnings {
		if strings.Contains(w, "named perform") || strings.Contains(w, "gates use [reviewer] by default") {
			t.Errorf("stale contradictory WARN must not fire when NODE names a named reviewer: %s", w)
		}
	}
}

// TestValidate_StepModelOverride (sty_7069bced / epic:model-selection order:3):
// a spine performer node's model= is reported as the effective model, with
// source=step, when the allocated binding itself has no model=. A binding
// model= still wins outright (source=binding) — AC3/AC6.
func TestValidate_StepModelOverride(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
		Agents: map[string]config.AgentBinding{
			"coder":   {Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob,Bash(satelle:*)", Role: "agent"},
			"planner": {Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "opus", Role: "agent"},
		},
	}
	wfs := routeDocs(
		`["*"]
obligations = ["raised", "planned", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "planner"
model = "sonnet"
skills = ["plan"]
requires = ["raised"]

[coded]
status = "in_progress"
agent = "coder"
model = "haiku"
skills = ["coder"]
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	r := Validate(agents, nil, wfs)
	if !r.OK() {
		t.Fatalf("problems: %v", r.Problems)
	}
	by := map[string]GateAllocation{}
	for _, g := range r.Gates {
		by[g.Node] = g
	}
	coded := by["in_progress"]
	if coded.EffectiveModel != "haiku" || coded.ModelSource != config.ModelSourceStep {
		t.Errorf("coded node = %+v, want effective=haiku source=step", coded)
	}
	// planner's binding already names a model — the step's model= must lose to it.
	planned := by["plan"]
	if planned.EffectiveModel != "opus" || planned.ModelSource != config.ModelSourceBinding {
		t.Errorf("plan node = %+v, want effective=opus source=binding", planned)
	}
}

func TestValidate_BrokenBinding(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: "not-a-cli"},
	}
	r := Validate(agents, nil, nil)
	if r.OK() {
		t.Fatal("unknown preset must produce a problem")
	}
	joined := strings.Join(r.Problems, "\n")
	if !strings.Contains(joined, "reviewer") {
		t.Errorf("problem should name the section:\n%s", joined)
	}
}

func TestValidate_BarePresetRejected(t *testing.T) {
	for _, bare := range []string{"claude", "grok"} {
		agents := config.AgentsConfig{
			Executor: config.AgentBinding{Command: "in-loop"},
			Reviewer: config.AgentBinding{Command: bare},
		}
		r := Validate(agents, nil, nil)
		if r.OK() {
			t.Fatalf("bare %q must be flagged", bare)
		}
		joined := strings.Join(r.Problems, "\n")
		if !strings.Contains(joined, "bare CLI presets removed") {
			t.Errorf("bare %q: expected bare-preset problem:\n%s", bare, joined)
		}
		for _, g := range r.Grants {
			if g.Name == "reviewer" && g.Backend != "invalid" {
				t.Errorf("bare %q: reviewer backend = %q, want invalid", bare, g.Backend)
			}
		}
	}
	// in-loop and empty stay valid (executor path).
	ok := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultClaudeCommand},
	}
	if r := Validate(ok, nil, nil); !r.OK() {
		t.Fatalf("full template + in-loop must pass: %v", r.Problems)
	}
	// Omitted [reviewer] command resolves to full DefaultClaudeCommand (AC2).
	omit := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		// Reviewer empty — ReviewerBinding fills DefaultReviewerCommand
	}
	// Validate receives raw config; empty command is treated as in-loop path
	// unless we resolve via ReviewerBinding. Mirror how the runtime resolves:
	resolved := omit
	rb := omit.ReviewerBinding()
	resolved.Reviewer = rb
	if rb.Command != agentcli.DefaultClaudeCommand {
		t.Fatalf("omitted reviewer command resolved to %q, want DefaultClaudeCommand", rb.Command)
	}
	if r := Validate(resolved, nil, nil); !r.OK() {
		t.Fatalf("resolved omitted reviewer must pass: %v", r.Problems)
	}
}

// TestValidate_MissingSystemPlaceholder (sty_21db3670 AC1): a multi-token
// isolated command that omits {system} as its own argv token is a hard problem.
func TestValidate_MissingSystemPlaceholder(t *testing.T) {
	// Full-looking claude template with no {system} token — rubric never appended.
	noSystem := "claude -p --output-format json --allowedTools {tools} --model {model}"
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: noSystem, Tools: "Read,Grep,Glob"},
	}
	r := Validate(agents, nil, nil)
	if r.OK() {
		t.Fatal("command missing {system} must produce a problem")
	}
	joined := strings.Join(r.Problems, "\n")
	if !strings.Contains(joined, "{system}") || !strings.Contains(joined, "reviewer") {
		t.Errorf("problem should name {system} and the section:\n%s", joined)
	}
	// Fused form (placeholder not its own token) also fails — mirrors buildArgs.
	fused := "claude -p --append-system-prompt={system} --allowedTools {tools}"
	agents.Reviewer.Command = fused
	r2 := Validate(agents, nil, nil)
	if r2.OK() {
		t.Fatal("fused {system} (not own argv token) must produce a problem")
	}
	// Present {system} as own token passes (no system-placeholder problem).
	ok := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultClaudeCommand},
	}
	r3 := Validate(ok, nil, nil)
	if !r3.OK() {
		t.Fatalf("DefaultClaudeCommand must pass: %v", r3.Problems)
	}
	for _, p := range r3.Problems {
		if strings.Contains(p, "{system}") {
			t.Errorf("DefaultClaudeCommand must not flag {system}: %s", p)
		}
	}
}

// TestValidate_ReviewerReadOnlyGrantCeiling (sty_ea948877): a reviewer whose
// adapter trims the offered tools to a read-only grant (pi --tools) is held
// read-only by agentcli.ReadOnlyGrantProblems, so it passes the ceiling check; a
// pi grant that admits a mutator still warns.
func TestValidate_ReviewerReadOnlyGrantCeiling(t *testing.T) {
	const pi = "pi -p --provider openrouter --tools {tools} --model {model} {system} {payload}"
	for _, tc := range []struct {
		name, tools string
		warn        bool
	}{
		{"read-only grant", "read,grep,find,ls", false},
		{"mutator grant", "read,bash,edit,write", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agents := config.AgentsConfig{
				Executor: config.AgentBinding{Command: "in-loop"},
				Reviewer: config.AgentBinding{Command: pi, Tools: tc.tools, Model: "openrouter/minimax/minimax-m3", Role: "reviewer"},
			}
			r := Validate(agents, nil, nil)
			var unsafe []string
			for _, f := range r.Findings {
				if f.ID == health.IDReviewerUnsafe && f.Artifact == "reviewer" {
					unsafe = append(unsafe, f.Detail)
				}
			}
			if tc.warn && len(unsafe) == 0 {
				t.Errorf("tools=%s: want a reviewer.unsafe warning, got none", tc.tools)
			}
			if !tc.warn && len(unsafe) > 0 {
				t.Errorf("tools=%s: read-only grant must pass the ceiling check, got: %v", tc.tools, unsafe)
			}
			for _, g := range r.Grants {
				if g.Name == "reviewer" && g.ReadOnly == tc.warn {
					t.Errorf("tools=%s: grant ReadOnly=%v, want %v", tc.tools, g.ReadOnly, !tc.warn)
				}
			}
		})
	}
}

// TestValidate_PlaceholderHealthyUnchanged (sty_21db3670 AC3): in-loop + canonical
// full templates produce neither a {system} problem nor a ceiling warning.
func TestValidate_PlaceholderHealthyUnchanged(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
		Agents: map[string]config.AgentBinding{
			"planner": {Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "opus"},
		},
	}
	r := Validate(agents, nil, nil)
	if !r.OK() {
		t.Fatalf("canonical templates must pass: %v", r.Problems)
	}
	for _, p := range r.Problems {
		if strings.Contains(p, "{system}") {
			t.Errorf("unexpected {system} problem: %s", p)
		}
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "read-only ceiling") {
			t.Errorf("canonical reviewer must not warn about ceiling: %s", w)
		}
	}
}

func TestValidate_UnresolvedEnv(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{
			Command: agentcli.DefaultClaudeCommand,
			Env:     map[string]string{"TOKEN": "${MISSING_VAR}"},
		},
	}
	r := Validate(agents, map[string]string{}, nil)
	if r.OK() {
		t.Fatal("unresolved env var must produce a problem")
	}
	joined := strings.Join(r.Problems, "\n")
	if !strings.Contains(joined, "MISSING_VAR") && !strings.Contains(joined, "reviewer") {
		t.Errorf("problem should name the unresolved var / section:\n%s", joined)
	}
	// Never leak a secret-shaped value — the raw ${…} may appear but no fabricated secret.
	if strings.Contains(joined, "sk-") {
		t.Errorf("problems must not leak secret-like values:\n%s", joined)
	}
}

func TestValidate_MissingNodeBinding(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultClaudeCommand},
	}
	wfs := []docindex.Doc{{
		Kind: "workflows", Name: "w",
		Body: "",
	}}
	_ = wfs
	wfs = routeDocs(
		`["*"]
obligations = ["raised", "worked", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[worked]
status = "work"
agent = "ghost"
skills = ["code"]
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["worked"]
`)
	r := Validate(agents, nil, wfs)
	if r.OK() {
		t.Fatal("missing node binding must produce a problem")
	}
	joined := strings.Join(r.Problems, "\n")
	if !strings.Contains(joined, "ghost") || !strings.Contains(joined, "work") {
		t.Errorf("problem should name agent and node:\n%s", joined)
	}
}

func TestValidate_OrphanBinding(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultClaudeCommand},
		Agents: map[string]config.AgentBinding{
			"unused-agent": {Command: agentcli.DefaultClaudeCommand},
		},
	}
	wfs := []docindex.Doc{{
		Kind: "workflows", Name: "w",
		Body: "",
	}}
	_ = wfs
	wfs = routeDocs(
		`["*"]
obligations = ["raised", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[closed]
status = "done"
terminal = true
requires = ["raised"]
`)
	r := Validate(agents, nil, wfs)
	// Orphans are warnings (advisory) — [retrospective]-style non-workflow agents
	// must not hard-fail engage/init.
	if !r.OK() {
		t.Fatalf("orphan must not be a hard problem: %v", r.Problems)
	}
	joined := strings.Join(r.Warnings, "\n")
	if !strings.Contains(joined, "unused-agent") || !strings.Contains(joined, "orphan") {
		t.Errorf("warning should name the orphan:\n%s", joined)
	}
}

// on_enter_agent validation lived here (sty_5cabe26f): the entry performer had
// to resolve to a binding and counted as a use so the binding was not flagged
// orphaned. Flat dispatch retired entry dispatch entirely (sty_05a5e203), so
// there is no entry binding to validate — an ADVISOR is declared on the route,
// consulted by the orchestrator, and is not a dispatch target.
func TestValidate_BadTimeout(t *testing.T) {
	// LoadAgents would refuse this at load; Validate still checks TimeoutDuration
	// on the in-memory binding for callers that construct AgentsConfig directly.
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Timeout: "not-a-duration"},
	}
	r := Validate(agents, nil, nil)
	if r.OK() {
		t.Fatal("bad timeout must produce a problem")
	}
	if !strings.Contains(strings.Join(r.Problems, "\n"), "timeout") {
		t.Errorf("problem should mention timeout: %v", r.Problems)
	}
}

func TestValidate_StreamInterface(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{
			Interface: "stream",
			Command:   "claude -p --input-format stream-json {system}",
			Tools:     "Read,Grep,Glob",
			Role:      "reviewer",
		},
	}
	r := Validate(agents, nil, nil)
	if r.OK() {
		t.Fatal("stream with {system} must fail validate")
	}

	agents.Reviewer = config.AgentBinding{
		Interface: "stream",
		Command:   agentcli.DefaultClaudeStreamCommand,
		Tools:     "Read,Grep,Glob",
		Role:      "reviewer",
	}
	r = Validate(agents, nil, nil)
	if !r.OK() {
		t.Fatalf("valid stream reviewer problems: %v", r.Problems)
	}
	var g Grant
	for _, x := range r.Grants {
		if x.Name == "reviewer" {
			g = x
		}
	}
	if g.Interface != "stream" || !strings.HasPrefix(g.Backend, "stream:") {
		t.Errorf("grant = %+v, want interface=stream backend stream:*", g)
	}
	if !g.ReadOnly {
		t.Error("stream reviewer with read-only tools should be ReadOnly")
	}
}

func TestValidate_TypeSafeInterfaceRejected(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{
			Command: agentcli.DefaultGrokCommand,
			Tools:   "read_file,grep,list_dir",
			Model:   "grok-4.5",
			Role:    "reviewer",
		},
		Agents: map[string]config.AgentBinding{
			"reviewer-typesafe": {
				Interface: "typesafe",
				Command:   "https://api.typesafe.ai/v1/systemone",
				Role:      "reviewer",
				Model:     "jev-1.13.0",
			},
		},
	}
	r := Validate(agents, nil, nil)
	if r.OK() {
		t.Fatal("interface=typesafe must be rejected as unknown")
	}
	joined := strings.Join(r.Problems, "\n")
	if !strings.Contains(joined, "typesafe") || !strings.Contains(joined, "interface") {
		t.Errorf("problems should name the unknown interface, got: %s", joined)
	}
}

func TestValidate_GrantEffort(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{
			Command: agentcli.DefaultGrokCommand,
			Tools:   "read_file,grep,list_dir",
			Model:   "grok-4.5",
			Effort:  "high",
		},
	}
	r := Validate(agents, nil, nil)
	var rev Grant
	for _, g := range r.Grants {
		if g.Name == "reviewer" {
			rev = g
		}
	}
	if rev.Effort != "high" {
		t.Fatalf("reviewer grant effort = %q, want high", rev.Effort)
	}
}

// TestValidate_ReviewerDefaultTemplatesPass: the shipped claude and grok
// reviewer templates validate.
func TestValidate_ReviewerDefaultTemplatesPass(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
	}

	// Claude default template → pass.
	agents.Reviewer = config.AgentBinding{
		Command: agentcli.DefaultClaudeCommand,
		Role:    "reviewer",
		Tools:   "Read,Grep,Glob",
	}
	r := Validate(agents, nil, nil)
	if !r.OK() {
		t.Fatalf("DefaultClaudeCommand reviewer must pass: %v", r.Problems)
	}

	// Grok default template → pass.
	agents.Reviewer = config.AgentBinding{
		Command: agentcli.DefaultGrokCommand,
		Role:    "reviewer",
		Tools:   "read_file,grep,list_dir",
	}
	r = Validate(agents, nil, nil)
	if !r.OK() {
		t.Fatalf("DefaultGrokCommand reviewer must pass: %v", r.Problems)
	}

}

// channelWF is a minimal spine with ONE named performer node, used to vary only
// the binding under test (sty_87c0ef37).
func channelWF(section string) []docindex.Doc {
	return routeDocs(
		`["*"]
obligations = ["raised", "planned", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "`+section+`"
skills = ["plan"]
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["planned"]
`)
}

// TestValidate_ContextChannelFindings is the AC1/AC2/AC7/AC8 matrix: a DISPATCHED
// performer must carry a pull-context channel (error when it does not), and a
// role=reviewer binding that grants shell holds capability it never uses
// (warning). Correct configurations produce neither. Nothing dispatches an agent
// — Validate is store-free and spawns no process.
func TestValidate_ContextChannelFindings(t *testing.T) {
	cases := []struct {
		name        string
		section     string // binding name allocated to the performing node
		binding     config.AgentBinding
		wantProblem bool
		wantWarning bool
	}{{
		name:        "claude without Read or a shell is not a channel — matches the runtime refusal",
		section:     "planner",
		binding:     config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Tools: "Grep,Glob", Model: "opus"},
		wantProblem: true,
	}, {
		name:        "empty grant",
		section:     "planner",
		binding:     config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Model: "opus"},
		wantProblem: true,
	}, {
		name:    "satelle CLI channel",
		section: "planner",
		binding: config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "opus"},
	}, {
		name:    "disk-read channel",
		section: "planner",
		binding: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
	}, {
		name:    "broad shell channel",
		section: "planner",
		binding: config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Tools: "*", Model: "opus"},
	}, {
		name:    "in-loop performer is exempt — never dispatched",
		section: "planner",
		binding: config.AgentBinding{Command: "in-loop"},
	}, {
		// A repo-agnostic name (AC5): nothing keys off "planner".
		name:        "arbitrary binding name is reported, not a known one",
		section:     "architect",
		binding:     config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Tools: "Grep,Glob", Model: "opus"},
		wantProblem: true,
	}, {
		name:    "claude Read is its disk-read channel",
		section: "planner",
		binding: config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob", Model: "opus"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agents := config.AgentsConfig{
				Executor: config.AgentBinding{Command: "in-loop"},
				Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
				Agents:   map[string]config.AgentBinding{tc.section: tc.binding},
			}
			wfs := channelWF(tc.section)
			r := Validate(agents, nil, wfs)

			got := findingWith(r.Problems, "no context channel")
			if tc.wantProblem && got == "" {
				t.Fatalf("want context-channel problem, got problems=%v", r.Problems)
			}
			if !tc.wantProblem && got != "" {
				t.Fatalf("unexpected context-channel problem: %s", got)
			}
			if tc.wantProblem {
				if !strings.Contains(got, tc.section) {
					t.Errorf("problem must name the binding %q: %s", tc.section, got)
				}
				if !strings.Contains(got, "Bash(satelle:*)") || !strings.Contains(got, "for disk reads") {
					t.Errorf("problem must name BOTH fixes: %s", got)
				}
				if !strings.Contains(got, `node "plan"`) {
					t.Errorf("problem must name the allocating node: %s", got)
				}
				if r.OK() {
					t.Error("a missing context channel must make the report not OK (non-zero exit)")
				}
			}
		})
	}
}

// A dispatched pi performer's context channel is pi's own read tool (sty_58a9bdc8):
// `read` passes, no read-capable grant still fails, and the finding names `read`
// rather than the grok-native read_file. `read` on another adapter is no channel.
func TestValidate_PiContextChannel(t *testing.T) {
	run := func(command, tools string) Report {
		agents := config.AgentsConfig{
			Executor: config.AgentBinding{Command: "in-loop"},
			Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
			Agents:   map[string]config.AgentBinding{"planner": {Command: command, Tools: tools}},
		}
		return Validate(agents, nil, channelWF("planner"))
	}
	const pi = "pi -p {system} {payload}"
	if got := findingWith(run(pi, "read,grep,find,ls").Problems, "no context channel"); got != "" {
		t.Errorf("pi with read: unexpected context-channel problem: %s", got)
	}
	r := run(pi, "grep,find,ls")
	got := findingWith(r.Problems, "no context channel")
	if got == "" || r.OK() {
		t.Fatalf("pi without read: want a context-channel problem, got problems=%v", r.Problems)
	}
	if !strings.Contains(got, "`read` for disk reads") || strings.Contains(got, "read_file") {
		t.Errorf("pi finding must name `read`, not read_file: %s", got)
	}
	if findingWith(run(agentcli.DefaultClaudeCommand, "read").Problems, "no context channel") == "" {
		t.Error("`read` on a claude binding must not be accepted as a context channel")
	}
}

// A dispatched Claude performer's context channel is Claude's own `Read` tool: a
// read-only grant passes with no shell, and a grant with no read tool and no satelle
// shell is still refused with a finding that names `Read`, not the grok read_file.
func TestValidate_ClaudeContextChannel(t *testing.T) {
	run := func(tools string) Report {
		agents := config.AgentsConfig{
			Executor: config.AgentBinding{Command: "in-loop"},
			Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
			Agents:   map[string]config.AgentBinding{"planner": {Command: agentcli.DefaultClaudeCommand, Tools: tools}},
		}
		return Validate(agents, nil, channelWF("planner"))
	}
	if got := findingWith(run("Read,Grep,Glob").Problems, "no context channel"); got != "" {
		t.Errorf("claude with Read: unexpected context-channel problem: %s", got)
	}
	if got := findingWith(run("read_file").Problems, "no context channel"); got == "" {
		t.Error("`read_file` on a claude binding must not be accepted as a context channel")
	}
	r := run("Grep,Glob")
	got := findingWith(r.Problems, "no context channel")
	if got == "" || r.OK() {
		t.Fatalf("claude without Read: want a context-channel problem, got problems=%v", r.Problems)
	}
	if !strings.Contains(got, "`Read` for disk reads") || strings.Contains(got, "read_file") {
		t.Errorf("claude finding must name `Read`, not read_file: %s", got)
	}
}

// A performer seat completed by a profile whose grant is empty still has no
// channel once every layer is visible (sty_50dae450): the refusal is deferred to
// the machine only when the machine's layers were left out, never waived.
func TestValidate_EmptyProfileGrantStillRefusedWithAllLayers(t *testing.T) {
	global, err := config.ParseGlobalAgents("[profiles.empty]\nrole = \"agent\"\ncommand = \"pi -p {system} {payload}\"\n")
	if err != nil {
		t.Fatal(err)
	}
	repo := config.AgentsConfig{
		Executor: config.AgentBinding{Role: config.RoleAgent, Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
		Agents:   map[string]config.AgentBinding{"coder": {Role: config.RoleAgent}},
	}
	local := config.AgentsConfig{Agents: map[string]config.AgentBinding{"coder": {Profile: "empty"}}}

	r := ValidateEffectiveLayered(config.AgentLayers{Repo: repo, Local: local}, global, nil, channelWF("coder"), nil)
	if got := findingWith(r.Problems, "no context channel"); got == "" || r.OK() {
		t.Fatalf("an empty profile grant must still be refused with every layer visible: %v", r.Problems)
	}

	r = ValidateEffectiveLayered(config.AgentLayers{Repo: repo, MachineLayersExcluded: true}, global, nil, channelWF("coder"), nil)
	if !r.OK() {
		t.Fatalf("with the machine layers left out, a role-only seat must not be refused: %v", r.Problems)
	}
	if w := findingWith(r.Warnings, "agents.local.toml or its profile"); w == "" {
		t.Errorf("want the per-machine grant warning, got %v", r.Warnings)
	}
}

// TestValidate_UnallocatedBindingHasNoChannelFinding — AC8: a binding no workflow
// allocates is an orphan, not a performer. Its grant is not judged for a channel
// it will never need.
func TestValidate_UnallocatedBindingHasNoChannelFinding(t *testing.T) {
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Reviewer: config.AgentBinding{Command: agentcli.DefaultGrokCommand, Tools: "read_file,grep,list_dir", Model: "grok-4.5"},
		Agents: map[string]config.AgentBinding{
			"planner": {Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "opus"},
			"nobody":  {Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob", Model: "opus"},
		},
	}
	wfs := channelWF("planner")
	r := Validate(agents, nil, wfs)
	if p := findingWith(r.Problems, "no context channel"); p != "" {
		t.Fatalf("unallocated binding must not be judged as a performer: %s", p)
	}
	if !r.OK() {
		t.Fatalf("orphan alone must not fail the report: %v", r.Problems)
	}
}

func findingWith(list []string, substr string) string {
	for _, s := range list {
		if strings.Contains(s, substr) {
			return s
		}
	}
	return ""
}
