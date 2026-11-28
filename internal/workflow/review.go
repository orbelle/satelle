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

func (r Review) Empty() bool {
	return r.TierTag == "" && len(r.Panels) == 0
}

func (r Review) With(part Review) Review {
	if part.Empty() {
		return r
	}
	out := Review{TierTag: r.TierTag}
	if part.TierTag != "" {
		out.TierTag = part.TierTag
	}
	if len(r.Panels)+len(part.Panels) > 0 {
		out.Panels = map[string]map[string][]string{}
	}
	for g, tiers := range r.Panels {
		out.Panels[g] = tiers
	}
	for g, tiers := range part.Panels {
		out.Panels[g] = tiers
	}
	return out
}

func (r Review) Validate(file string, gate func(name string) (found, check bool), agent AgentLookup) []error {
	var errs []error
	tag := strings.TrimSpace(r.TierTag)
	switch {
	case len(r.Panels) > 0 && tag == "":
		errs = append(errs, fmt.Errorf("%s: [review] panels are set but tier_tag is not", file))
	case r.TierTag != "" && tag == "":
		errs = append(errs, fmt.Errorf("%s: [review] tier_tag is blank", file))
	case strings.Contains(tag, ":"):
		errs = append(errs, fmt.Errorf("%s: [review] tier_tag %q holds ':', which separates a tag's key from its value", file, r.TierTag))
	}
	gates := make([]string, 0, len(r.Panels))
	for g := range r.Panels {
		gates = append(gates, g)
	}
	sort.Strings(gates)
	for _, gname := range gates {
		found, check := gate(gname)
		if !found {
			errs = append(errs, fmt.Errorf("%s: [review.panels.%s]: no workflow has a gate %q", file, gname, gname))
		} else if check {
			errs = append(errs, fmt.Errorf("%s: [review.panels.%s]: gate %q runs a check skill in every workflow that has it, which dispatches no agent", file, gname, gname))
		}
		tiers := make([]string, 0, len(r.Panels[gname]))
		for t := range r.Panels[gname] {
			tiers = append(tiers, t)
		}
		sort.Strings(tiers)
		for _, tier := range tiers {
			panel := r.Panels[gname][tier]
			if len(panel) == 0 {
				errs = append(errs, fmt.Errorf("%s: [review.panels.%s] %s lists no agent", file, gname, tier))
			}
			var seen []string
			for _, a := range panel {
				if slices.Contains(seen, a) {
					errs = append(errs, fmt.Errorf("%s: [review.panels.%s] %s lists agent %q twice", file, gname, tier, a))
					continue
				}
				seen = append(seen, a)
				if !agent(a) {
					errs = append(errs, fmt.Errorf("%s: [review.panels.%s] %s: agent %q is not in the agents catalog", file, gname, tier, a))
				}
			}
		}
	}
	return errs
}
