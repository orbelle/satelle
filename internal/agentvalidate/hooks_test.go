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

// TestValidateRefusesUnsafeHookAllocations pins AC5's allocation classes. Each
// is refused BEFORE the hook is ever invoked, and each problem names the
// workflow, the operation, and the offending section so the fix is unambiguous.
func TestValidateRefusesUnsafeHookAllocations(t *testing.T) {
	cases := []struct {
		name   string
		agents func() config.AgentsConfig
		fm     string
		want   string
	}{
		{
			name:   "missing agent binding",
			agents: healthyAgents,
			fm:     "hooks:\n  - operation: create_review\n    skill: s\n    agent: nobody\n",
			want:   "no [nobody] binding",
		},
		{
			name: "non-reviewer role on a verdict hook",
			agents: func() config.AgentsConfig {
				a := healthyAgents()
				a.Agents = map[string]config.AgentBinding{
					"worker": {Role: config.RoleAgent, Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob"},
				}
				return a
			},
			fm:   "hooks:\n  - operation: create_review\n    skill: s\n    agent: worker\n",
			want: "want role=reviewer",
		},
		{
			name: "in-loop verdict binding",
			agents: func() config.AgentsConfig {
				a := healthyAgents()
				a.Agents = map[string]config.AgentBinding{
					"inloop-judge": {Role: config.RoleReviewer, Command: "in-loop"},
				}
				return a
			},
			fm:   "hooks:\n  - operation: create_review\n    skill: s\n    agent: inloop-judge\n",
			want: "cannot produce an isolated verdict",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Validate(c.agents(), nil, []docindex.Doc{wfWithHook(c.fm)})
			if r.OK() {
				t.Fatalf("want a refusal for %s", c.name)
			}
			joined := strings.Join(r.Problems, "\n")
			if !strings.Contains(joined, c.want) {
				t.Errorf("problems should mention %q: %v", c.want, r.Problems)
			}
			for _, want := range []string{`workflow "w"`, "hook create_review"} {
				if !strings.Contains(joined, want) {
					t.Errorf("problems should name %q: %v", want, r.Problems)
				}
			}
		})
	}
}

// TestValidateChecksHooksEvenWhenTheDotDoesNotParse pins the ordering: hooks are
// FRONTMATTER, so an unparseable DOT block must not hide a broken allocation.
func TestValidateChecksHooksEvenWhenTheDotDoesNotParse(t *testing.T) {
	doc := docindex.Doc{
		Kind: "workflows", Name: "w",
		Body: "---\nname: w\nhooks:\n  - operation: create_review\n    skill: s\n    agent: nobody\n---\n\nno dot block here\n",
	}
	r := Validate(healthyAgents(), nil, []docindex.Doc{doc})
	if r.OK() || !strings.Contains(strings.Join(r.Problems, "\n"), "nobody") {
		t.Errorf("a hook must be checked independently of the DOT: %v", r.Problems)
	}
}

// TestValidateHookAllocationResolvesThroughAProfile ties AC1 to sty_c7dfeedf: a
// hook allocated to a binding that resolves through a machine-wide profile is
// checked and reported on the RESOLVED binding.
func TestValidateHookAllocationResolvesThroughAProfile(t *testing.T) {
	global := catalog(t, `
[profiles.judge-profile]
role    = "reviewer"
command = "claude -p --disallowedTools Write,Edit --append-system-prompt {system}"
tools   = "Read,Grep,Glob"
model   = "opus"
`)
	repo := healthyAgents()
	repo.Agents = map[string]config.AgentBinding{"judge": {Profile: "judge-profile"}}
	doc := wfWithHook("hooks:\n  - operation: create_review\n    skill: s\n    agent: judge\n")

	r := ValidateEffective(repo, global, nil, []docindex.Doc{doc})
	if !r.OK() {
		t.Fatalf("a profile-resolved hook binding must validate green: %v", r.Problems)
	}
	for _, ga := range r.Gates {
		if ga.Node == "hook:create_review" {
			if ga.Agent != "judge" || ga.EffectiveModel != "opus" {
				t.Errorf("the allocation must report the RESOLVED binding: %+v", ga)
			}
			return
		}
	}
	t.Fatalf("no hook allocation in %+v", r.Gates)
}

// TestFindingsMirrorTheProseSurfaces pins the compatibility seam added for
// doctor (sty_e9da28e2): every Problem and Warning has a Finding whose Detail is
// that exact string, and every Finding carries a stable id. The two surfaces
// cannot drift, so a caller reading either sees the same set.
func TestFindingsMirrorTheProseSurfaces(t *testing.T) {
	// A fixture that trips several distinct classes at once.
	agents := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop", Role: config.RoleAgent},
		Reviewer: config.AgentBinding{Role: config.RoleReviewer, Command: "nosuch exec {system}"},
		Agents: map[string]config.AgentBinding{
			"orphan": {Role: config.RoleReviewer, Command: agentcli.DefaultClaudeCommand, Tools: "Read,Grep,Glob"},
		},
	}
	doc := wfWithHook("hooks:\n  - operation: create_review\n    skill: s\n    agent: nobody\n")
	r := Validate(agents, nil, []docindex.Doc{doc})

	if len(r.Problems) == 0 || len(r.Warnings) == 0 {
		t.Fatalf("fixture must produce both problems and warnings: %+v", r)
	}
	errs := r.Findings.Details(health.SeverityError)
	warns := r.Findings.Details(health.SeverityWarn)
	if len(errs) != len(r.Problems) || len(warns) != len(r.Warnings) {
		t.Fatalf("counts differ: %d/%d problems, %d/%d warnings", len(errs), len(r.Problems), len(warns), len(r.Warnings))
	}
	for i, p := range r.Problems {
		if errs[i] != p {
			t.Errorf("problem %d: finding detail %q != prose %q", i, errs[i], p)
		}
	}
	for i, w := range r.Warnings {
		if warns[i] != w {
			t.Errorf("warning %d: finding detail %q != prose %q", i, warns[i], w)
		}
	}
	known := map[string]bool{}
	for _, id := range health.IDs() {
		known[id] = true
	}
	for _, f := range r.Findings {
		if !known[f.ID] {
			t.Errorf("finding carries an unregistered id %q", f.ID)
		}
	}
	// The classes this fixture is built to trip, each by id.
	got := map[string]bool{}
	for _, f := range r.Findings {
		got[f.ID] = true
	}
	for _, want := range []string{health.IDReviewerUnsafe, health.IDHookAlloc, health.IDNodeAlloc} {
		if !got[want] {
			t.Errorf("expected a %s finding: %+v", want, r.Findings)
		}
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
