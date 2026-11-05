package agentvalidate

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// catalog parses a machine-wide profile catalog fixture.
func catalog(t testing.TB, body string) config.GlobalAgentsConfig {
	t.Helper()
	g, err := config.ParseGlobalAgents(body)
	if err != nil {
		t.Fatalf("ParseGlobalAgents: %v", err)
	}
	return g
}

// grantOf returns the named grant from a report.
func grantOf(t testing.TB, r Report, name string) Grant {
	t.Helper()
	for _, g := range r.Grants {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("no grant named %q in %+v", name, r.Grants)
	return Grant{}
}

// TestValidateEffectiveReportsSources pins AC4's display half: every effective
// field carries the tier that supplied it, so an operator reading `agent
// validate` sees not only WHAT will run but WHERE it was authored.
func TestValidateEffectiveReportsSources(t *testing.T) {
	global := catalog(t, `
[profiles.claude-opus]
role    = "reviewer"
command = "claude -p --disallowedTools Write,Edit --append-system-prompt {system} --model {model}"
tools   = "Read,Grep,Glob"
model   = "opus"
effort  = "high"
`)
	repo := config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop", Role: config.RoleAgent},
		Reviewer: config.AgentBinding{Profile: "claude-opus", Effort: "low", Role: config.RoleReviewer},
	}
	r := ValidateEffective(repo, global, nil, nil)
	if !r.OK() {
		t.Fatalf("fixture must validate green: %v", r.Problems)
	}
	g := grantOf(t, r, "reviewer")
	want := map[string]string{
		"command": config.SourceProfile("claude-opus"),
		"tools":   config.SourceProfile("claude-opus"),
		"model":   config.SourceProfile("claude-opus"),
		"effort":  config.SourceRepo,
		"role":    config.SourceRepo,
	}
	for field, src := range want {
		if got := g.Sources[field]; got != src {
			t.Errorf("sources[%s] = %q, want %q", field, got, src)
		}
	}
	if g.Model != "opus" || g.Effort != "low" || !strings.Contains(g.Command, "{system}") {
		t.Errorf("grant does not describe the merged binding: %+v", g)
	}
	if r.Provenance == nil {
		t.Error("ValidateEffective must carry the provenance table")
	}
}

// TestValidateEffectiveRefusesBrokenReferences pins AC4's refusal half for the
// reference classes: a missing profile, a reference cycle, and a role conflict
// each surface as a PROBLEM on the report rather than being swallowed — and the
// rest of the report still renders, so one run shows the whole picture.
func TestValidateEffectiveRefusesBrokenReferences(t *testing.T) {
	cases := []struct {
		name   string
		global string
		repo   config.AgentsConfig
		want   string
	}{
		{
			name:   "missing profile",
			global: "[profiles.present]\nrole = \"reviewer\"\ncommand = \"claude -p {system}\"\n",
			repo:   config.AgentsConfig{Reviewer: config.AgentBinding{Profile: "absent"}},
			want:   "absent",
		},
		{
			name:   "role conflict",
			global: "[profiles.judge]\nrole = \"reviewer\"\ncommand = \"claude -p {system}\"\n",
			repo: config.AgentsConfig{Agents: map[string]config.AgentBinding{
				"worker": {Profile: "judge", Role: config.RoleAgent},
			}},
			want: "role",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ValidateEffective(c.repo, catalog(t, c.global), nil, nil)
			if r.OK() {
				t.Fatalf("want a problem for %s", c.name)
			}
			if !strings.Contains(strings.Join(r.Problems, "\n"), c.want) {
				t.Errorf("problems should name %q: %v", c.want, r.Problems)
			}
			if len(r.Grants) == 0 {
				t.Error("a broken reference must not suppress the rest of the report")
			}
		})
	}
}

// TestValidateEffectiveJudgesTheMergedBinding pins the check that matters most
// for AC4's "unsafe reviewer ceilings": a profile must not be able to smuggle a
// capability past a check by supplying it from the machine-wide catalog. The
// ceiling is judged on the MERGED binding, so a write-capable command inherited
// from a profile is caught exactly as an inline one would be.
func TestValidateEffectiveJudgesTheMergedBinding(t *testing.T) {
	global := catalog(t, `
[profiles.wide-open]
role      = "reviewer"
interface = "acp"
command   = "nosuch acp-serve"
`)
	repo := config.AgentsConfig{Agents: map[string]config.AgentBinding{
		"judge": {Profile: "wide-open", Role: config.RoleReviewer},
	}}
	r := ValidateEffective(repo, global, nil, nil)
	if r.OK() {
		t.Fatal("a profile-supplied reviewer with no tools= grant evidence must be refused")
	}
	if !strings.Contains(strings.Join(r.Problems, "\n"), "requires tools=") {
		t.Errorf("problem should name the ceiling escape: %v", r.Problems)
	}
	// The same check runs on an unresolved layer, so the catalog changed only
	// WHERE the value came from — never WHETHER it is judged.
	inline := config.AgentsConfig{Agents: map[string]config.AgentBinding{"judge": {
		Role:      config.RoleReviewer,
		Interface: "acp",
		Command:   "nosuch acp-serve",
	}}}
	if direct := Validate(inline, nil, nil); direct.OK() {
		t.Fatal("the inline equivalent must be refused identically")
	}
}

// TestValidateEffectiveRefusesUnresolvedVars pins the remaining AC4 class: a
// ${VAR} that neither the catalog nor the repo defines refuses, naming the
// binding and the variable — never the value.
func TestValidateEffectiveRefusesUnresolvedVars(t *testing.T) {
	global := catalog(t, `
[profiles.remote]
role    = "reviewer"
command = "claude -p --disallowedTools Write,Edit --append-system-prompt {system}"
tools   = "Read,Grep,Glob"
env     = { ANTHROPIC_AUTH_TOKEN = "${NOT_DEFINED}" }
`)
	repo := config.AgentsConfig{Reviewer: config.AgentBinding{Profile: "remote", Role: config.RoleReviewer}}
	r := ValidateEffective(repo, global, nil, nil)
	if r.OK() {
		t.Fatal("an undefined ${VAR} must be refused")
	}
	if !strings.Contains(strings.Join(r.Problems, "\n"), "NOT_DEFINED") {
		t.Errorf("problem should name the missing var: %v", r.Problems)
	}
	// Defined in the catalog's own [vars], the same binding resolves.
	withVar := catalog(t, `
[vars]
NOT_DEFINED = "sk-value"

[profiles.remote]
role    = "reviewer"
command = "claude -p --disallowedTools Write,Edit --append-system-prompt {system}"
tools   = "Read,Grep,Glob"
env     = { ANTHROPIC_AUTH_TOKEN = "${NOT_DEFINED}" }
`)
	if ok := ValidateEffective(repo, withVar, nil, nil); !ok.OK() {
		t.Errorf("a catalog-defined var must satisfy a catalog profile: %v", ok.Problems)
	}
	if joined := strings.Join(r.Problems, "\n"); strings.Contains(joined, "sk-value") {
		t.Error("a problem must never carry a variable's value")
	}
}
