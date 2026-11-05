package config

import (
	"github.com/bobmcallan/satelle/internal/wfdot"
)

// The embedded lifecycle is one DERIVED ROUTE in two halves — workflows/done.md
// and workflows/step.md — rather than four DOT graphs (sty_3795e7f6). The
// corpus tests in this package used to reach for `wfdot.Parse` per workflow
// file; these helpers give them the same two answers (what states does the
// shipped lifecycle have, what skills does it bind) off the route grammar, so
// there is one place that knows the shipped representation.

// embeddedRouteHalves returns the two shipped halves, empty when either is
// absent.
func embeddedRouteHalves() (done, step string) {
	for _, d := range EmbeddedDefaults() {
		if d.Kind != "workflows" {
			continue
		}
		switch d.Name {
		case "done":
			done = d.Body
		case "step":
			step = d.Body
		}
	}
	if done == "" || step == "" {
		return "", ""
	}
	return done, step
}

// embeddedRouteSpecs derives one Spec per category the shipped route declares.
func embeddedRouteSpecs() map[string]wfdot.Spec {
	done, step := embeddedRouteHalves()
	if done == "" {
		return nil
	}
	lists, err := wfdot.ParseDone(done)
	if err != nil {
		return nil
	}
	cat, err := wfdot.ParseSteps(step)
	if err != nil {
		return nil
	}
	out := map[string]wfdot.Spec{}
	for _, l := range lists {
		spec, err := wfdot.BuildRoute(l, cat, nil)
		if err != nil {
			continue
		}
		out[l.Category] = spec
	}
	return out
}

// routeSourceSkillRefs returns every skill one half of the route binds — a
// step's executor rubrics and entry reviewers, an always-on gate, a park or
// cancel gate. A body that is not route grammar yields nothing.
func routeSourceSkillRefs(body string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(names ...string) {
		for _, n := range names {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	if lists, err := wfdot.ParseDone(body); err == nil {
		for _, l := range lists {
			add(l.ParkGate, l.CancelGate, l.ParkAdvisorSkill)
		}
	}
	if cat, err := wfdot.ParseSteps(body); err == nil {
		for _, st := range cat.Steps {
			add(st.Skills...)
			add(st.Reviewers...)
			add(st.AdvisorSkill)
		}
		for _, g := range cat.Gates {
			add(g.Skill)
		}
	}
	return out
}

// splitLines is the frontmatter slice fmScalarLine expects.
func splitLines(body string) []string {
	fm, _, ok := splitFrontmatter(body)
	if !ok {
		return nil
	}
	return fm
}
