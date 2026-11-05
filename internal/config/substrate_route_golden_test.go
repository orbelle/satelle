package config

import (
	"fmt"
	"sort"
	"strings"
)

// The embedded route is about to gain a large amount of explanatory comment
// (sty_58911b1a). Comments cannot change the grammar — the parser skips a line
// starting with `<!--` — but "cannot" is a claim, and this file is the proof.
//
// Written and passing against the BARE files BEFORE the comments were added, so
// a diff of the derived route after the edit is a real regression signal rather
// than a restatement of whatever the files happen to say now.

// fingerprintOf renders a category's derived Spec as deterministic text: the
// step sequence with start/terminal markers and the reviewers gating entry to
// each, then any always-on gate carrying an `on:` list. Everything a reader of
// the route cares about, and nothing that depends on map iteration.
func fingerprintOf(category string) string {
	specs := embeddedRouteSpecs()
	spec, ok := specs[category]
	if !ok {
		return "<no route>"
	}
	var b strings.Builder
	// Entry gates keyed by target step, so a step's line carries what admits it.
	entry := map[string][]string{}
	for _, tr := range spec.Transitions {
		if len(tr.Skills) > 0 {
			entry[tr.To] = append(entry[tr.To], tr.Skills...)
		}
	}
	var gates []string
	for _, st := range spec.States {
		if len(st.On) > 0 {
			gates = append(gates, fmt.Sprintf("%s on=%s", st.Skill, strings.Join(st.On, "+")))
			continue
		}
		marks := ""
		switch st.Shape {
		case "Mdiamond":
			marks = " [start]"
		case "Msquare":
			marks = " [terminal]"
		}
		agent := st.Agent
		if agent == "" {
			agent = "-"
		}
		in := entry[st.Name]
		sort.Strings(in)
		fmt.Fprintf(&b, "step %s%s agent=%s skill=%s provides=%s admits=[%s]\n",
			st.Name, marks, agent, orDash(st.Skill), orDash(st.Obligation), strings.Join(in, ","))
	}
	sort.Strings(gates)
	for _, g := range gates {
		fmt.Fprintf(&b, "gate %s\n", g)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
