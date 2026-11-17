package workflow

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type Review struct {
	TierTag string                         `toml:"tier_tag"`
	Panels  map[string]map[string][]string `toml:"panels"`
}

func (r Review) Resolve(name string, g Gate, tiers []string) (Gate, string, error) {
	panels, ok := r.Panels[name]
	if r.TierTag == "" || !ok || len(tiers) == 0 {
		return g, "", nil
	}
	if len(tiers) > 1 {
		return g, "", fmt.Errorf("item carries more than one %s tag (%s); [review] picks one panel per tier", r.TierTag, strings.Join(tiers, ", "))
	}
	panel, ok := panels[tiers[0]]
	if !ok {
		return g, "", nil
	}
	out := g
	out.Agent = ""
	out.Panel = slices.Clone(panel)
	if out.Combine == "" {
		out.Combine = CombineAll
	}
	return out, tiers[0], nil
}

func (r Review) Agents(name string) []string {
	var out []string
	for _, panel := range r.Panels[name] {
		for _, a := range panel {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (r Review) Validate(gate func(name string) (found, check bool), agent AgentLookup) []error {
	var errs []error
	tag := strings.TrimSpace(r.TierTag)
	switch {
	case len(r.Panels) > 0 && tag == "":
		errs = append(errs, fmt.Errorf("satelle.toml: [review] panels are set but tier_tag is not"))
	case r.TierTag != "" && tag == "":
		errs = append(errs, fmt.Errorf("satelle.toml: [review] tier_tag is blank"))
	case strings.Contains(tag, ":"):
		errs = append(errs, fmt.Errorf("satelle.toml: [review] tier_tag %q holds ':', which separates a tag's key from its value", r.TierTag))
	}
	gates := make([]string, 0, len(r.Panels))
	for g := range r.Panels {
		gates = append(gates, g)
	}
	sort.Strings(gates)
	for _, gname := range gates {
		found, check := gate(gname)
		if !found {
			errs = append(errs, fmt.Errorf("satelle.toml: [review.panels.%s]: no workflow has a gate %q", gname, gname))
		} else if check {
			errs = append(errs, fmt.Errorf("satelle.toml: [review.panels.%s]: gate %q runs a check skill in a workflow, which dispatches no agent", gname, gname))
		}
		tiers := make([]string, 0, len(r.Panels[gname]))
		for t := range r.Panels[gname] {
			tiers = append(tiers, t)
		}
		sort.Strings(tiers)
		for _, tier := range tiers {
			panel := r.Panels[gname][tier]
			if len(panel) == 0 {
				errs = append(errs, fmt.Errorf("satelle.toml: [review.panels.%s] %s lists no agent", gname, tier))
			}
			var seen []string
			for _, a := range panel {
				if slices.Contains(seen, a) {
					errs = append(errs, fmt.Errorf("satelle.toml: [review.panels.%s] %s lists agent %q twice", gname, tier, a))
					continue
				}
				seen = append(seen, a)
				if !agent(a) {
					errs = append(errs, fmt.Errorf("satelle.toml: [review.panels.%s] %s: agent %q is not in the agents catalog", gname, tier, a))
				}
			}
		}
	}
	return errs
}
