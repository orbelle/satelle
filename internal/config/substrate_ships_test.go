package config

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// shipsAllow is a per-artifact map of non-shipping tokens → justification.
// Key: "<kind>/<name>". Tokens are gate skill names, transition endpoints, or
// non-shipping state names that appear as illustrations only (sty_01f49dd5).
var shipsAllow = map[string]map[string]string{
	"principles/satelle-skill-naming": {
		"satelle-integration-review":   "name-shape illustration; principle teaches the naming grammar, not a shipped gate set",
		"satelle-integration-check":    "name-shape illustration of the functional-check suffix",
		"satelle-story-release-review": "name-shape illustration of a close-gate style name",
		"satelle-code-ac-review":       "name-shape illustration of a code-AC gate name a repo may author",
		"integration":                  "name-shape vocabulary example (what a skill acts on), not a shipped state",
		"plan":                         "name-shape vocabulary example of a bare executor rubric / stage name",
		"release":                      "name-shape vocabulary example of a bare executor rubric / stage name",
	},
	"principles/satelle-done-is-last": {
		"commit_push": "hedged e.g. illustration of a richer repo graph",
		"committed":   "hedged e.g. illustration of a richer repo graph",
	},
	"skills/satelle-workflow-change-review": {
		"integration": "illustration of edges a prior graph might have held",
	},
	"skills/satelle-story-intent-review": {
		"plan": "hedged when a workflow has a plan step",
	},
	"skills/satelle-story-plan-review": {
		"plan": "hedged when a workflow has a plan step",
	},
	"skills/satelle-reviewer-objective-audit": {
		"plan": "example of a pure executor skill name that matches neither reviewer rule",
	},
}

// nonShippingStateWatch is a list of known non-shipping lifecycle-shaped tokens.
// Prefer deriving from a pattern when cheap; this list is the deliberate limit
// for sty_01f49dd5 (architecture note: R3 is instance-based, not class-closed).
var nonShippingStateWatch = []string{
	"integration", "release", "deploy", "plan", "commit_push", "committed",
}

var (
	// gateSkillRe matches satelle-*-review / satelle-*-check gate skill names.
	gateSkillRe = regexp.MustCompile(`\bsatelle-[a-z0-9-]+-(?:review|check)\b`)
	// transitionRe matches backticked A → B / A -> B phrases.
	transitionRe = regexp.MustCompile("`([a-z][a-z0-9_]*)\\s*(?:→|->)\\s*([a-z][a-z0-9_]*)`")
	// backtickedTokenRe matches a single backticked lowercase/snake token.
	backtickedTokenRe = regexp.MustCompile("`([a-z][a-z0-9_]*)`")
)

// embeddedLifecycleStates returns state names that participate in edges across
// every embedded workflow (annotation/edge-less nodes excluded).
func embeddedLifecycleStates() map[string]bool {
	out := map[string]bool{}
	// The shipped lifecycle is a derived route, so the states come from the route
	// each declared category builds rather than from a graph (sty_3795e7f6).
	for _, spec := range embeddedRouteSpecs() {
		for _, tr := range spec.Transitions {
			out[tr.From], out[tr.To] = true, true
		}
	}
	return out
}

// embeddedSkillNames returns every embedded skill name.
func embeddedSkillNames() map[string]bool {
	out := map[string]bool{}
	for _, d := range EmbeddedDefaults() {
		if d.Kind == "skills" {
			out[d.Name] = true
		}
	}
	return out
}

// shipsClaimProblems reports tokens in body that claim a non-shipping state or
// gate as if it shipped, after fence-stripping. Inline code spans are KEPT:
// `release` in prose is exactly the claim this check exists to catch.
// allow is the per-artifact map.
func shipsClaimProblems(kind, name, body string, states, skills map[string]bool, allow map[string]string) []string {
	prose := stripFences(body)
	var problems []string
	seen := map[string]bool{}
	add := func(msg string) {
		if !seen[msg] {
			seen[msg] = true
			problems = append(problems, msg)
		}
	}
	allowed := func(tok string) bool {
		if allow != nil && allow[tok] != "" {
			return true
		}
		return false
	}

	// R1 — gate skill names must be embedded or allow-listed.
	for _, m := range gateSkillRe.FindAllString(prose, -1) {
		if skills[m] || allowed(m) {
			continue
		}
		add(fmt.Sprintf("names non-shipping gate skill %q as though it ships", m))
	}

	// R2 — backticked transition endpoints must be embedded lifecycle states.
	for _, m := range transitionRe.FindAllStringSubmatch(prose, -1) {
		for _, end := range []string{m[1], m[2]} {
			if states[end] || allowed(end) {
				continue
			}
			add(fmt.Sprintf("transition endpoint %q is not an embedded lifecycle state", end))
		}
	}

	// R3 — known non-shipping state tokens in backticks.
	for _, tok := range nonShippingStateWatch {
		if states[tok] {
			continue // somehow shipped — fine
		}
		// Match whole backticked token only.
		pat := regexp.MustCompile("`" + regexp.QuoteMeta(tok) + "`")
		if !pat.MatchString(prose) {
			continue
		}
		if allowed(tok) {
			continue
		}
		add(fmt.Sprintf("names non-shipping state %q as though it ships", tok))
	}
	return problems
}

// shippedExecutorSkills names the bare-verb executor rubrics the embed tree
// ships on purpose. The list is closed: a new entry is a deliberate decision that
// the binary carries a performer's rubric, not something that appears silently.
//
//   - ready: the shipped epic/parent readiness step (step.toml [ready]) names it,
//     so a repo with no workflow of its own has a rubric for the round
//     (sty_25064a6c).
var shippedExecutorSkills = map[string]bool{
	"ready": true,
}

func TestShipsAllowListJustified(t *testing.T) {
	for art, toks := range shipsAllow {
		if len(toks) == 0 {
			t.Errorf("shipsAllow[%q] is empty", art)
		}
		for tok, why := range toks {
			if strings.TrimSpace(why) == "" {
				t.Errorf("shipsAllow[%q][%q] missing justification", art, tok)
			}
		}
	}
}

func TestShipsClaimProblemsNegative(t *testing.T) {
	states := map[string]bool{"backlog": true, "in_progress": true, "done": true}
	skills := map[string]bool{"satelle-story-done-review": true}
	// (a) non-shipping gate
	if p := shipsClaimProblems("skills", "x", "see `satelle-code-ac-review` gate", states, skills, nil); len(p) == 0 {
		t.Error("expected problem for satelle-code-ac-review")
	}
	// (b) non-shipping transition endpoint
	if p := shipsClaimProblems("principles", "x", "stop for `in_progress → integration`", states, skills, nil); len(p) == 0 {
		t.Error("expected problem for in_progress → integration")
	}
	// (c) non-shipping state token
	if p := shipsClaimProblems("principles", "x", "at `release` the build ships", states, skills, nil); len(p) == 0 {
		t.Error("expected problem for `release`")
	}
	// allow-list silences
	allow := map[string]string{"satelle-code-ac-review": "test waiver"}
	if p := shipsClaimProblems("skills", "x", "see `satelle-code-ac-review` gate", states, skills, allow); len(p) != 0 {
		t.Errorf("allow-listed token still reported: %v", p)
	}
}
