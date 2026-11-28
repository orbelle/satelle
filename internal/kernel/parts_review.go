package kernel

import (
	"slices"
	"strconv"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func reviewFor(shared workflow.Review, parts map[string]*Part, target string) workflow.Review {
	if target == reporeg.Self {
		return shared
	}
	p := parts[target]
	if p == nil || len(p.Problems) > 0 || p.Review.Empty() {
		return shared
	}
	return shared.With(p.Review)
}

func (k *Kernel) ReviewFor(it item.Item) workflow.Review {
	return reviewFor(k.Settings.Review, k.Parts, it.Target())
}

func (c *Config) reviewOf(part string) workflow.Review {
	if p := c.Parts[part]; part != "" && p != nil {
		return c.Settings.Review.With(p.Review)
	}
	return c.Settings.Review
}

func (c *Config) servingWorkflows(x string) []*workflow.Workflow {
	byName := map[string]*workflow.Workflow{}
	for n, w := range c.Workflows {
		if w.Part == "" || (x != "" && w.Part == x) {
			byName[n] = w
		}
	}
	if p := c.Parts[x]; x != "" && p != nil {
		for n, w := range p.Workflows {
			byName[n] = w
		}
	}
	out := make([]*workflow.Workflow, 0, len(byName))
	for _, n := range sortedKeys(byName) {
		out = append(out, byName[n])
	}
	return out
}

func (c *Config) reviewGateIn(x string) func(name string) (found, check bool) {
	return func(name string) (found, check bool) {
		check = true
		for _, w := range c.servingWorkflows(x) {
			g, ok := w.Gates[name]
			if !ok {
				continue
			}
			found = true
			if sk, ok := c.skillOf(w.Part, g.Skill); !ok || !sk.IsCheck() {
				check = false
			}
		}
		return found, found && check
	}
}

func (c *Config) usedJudges(w *workflow.Workflow, gname string) []string {
	out := judges(c.reviewOf(w.Part), gname, w.Gates[gname])
	if w.Part != "" {
		return out
	}
	for _, x := range sortedKeys(c.Parts) {
		p := c.Parts[x]
		if len(p.Problems) > 0 {
			continue
		}
		for _, a := range p.Review.Agents(gname) {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}

func (c *Config) partReviewProblems(p *Part) []error {
	if p.Review.Empty() {
		return nil
	}
	x := p.Name
	file := PartsDir + "/" + x + "/" + SettingsFile
	shared := c.Settings.Review
	var errs []error
	if t := p.Review.TierTag; t != "" && shared.TierTag != "" && t != shared.TierTag {
		errs = append(errs, partErr("%s: [review] tier_tag %s differs from the shared %s tier_tag %s; the shared panels it keeps for its other gates read the shared tier_tag",
			file, strconv.Quote(t), SettingsFile, strconv.Quote(shared.TierTag)))
	}
	eff := shared.With(p.Review)
	cat := c.Agents.Overlay(c.PartAgents[x])
	for _, e := range (workflow.Review{TierTag: eff.TierTag, Panels: p.Review.Panels}).Validate(file, c.reviewGateIn(x), cat.Has) {
		errs = append(errs, partSafe(e))
	}
	for _, w := range c.servingWorkflows(x) {
		if w.Part != "" {
			continue
		}
		wcat := c.AgentsFor(w)
		for _, gname := range sortedKeys(p.Review.Panels) {
			if _, ok := w.Gates[gname]; !ok {
				continue
			}
			for _, tier := range sortedKeys(p.Review.Panels[gname]) {
				for _, a := range p.Review.Panels[gname][tier] {
					if cat.Has(a) && !wcat.Has(a) {
						errs = append(errs, partErr("%s: [review.panels.%s] %s: agent %s is not in the shared %s, which shared workflow %s dispatches gate %s from",
							file, gname, tier, strconv.Quote(a), AgentsFile, w.Name, strconv.Quote(gname)))
					}
				}
			}
		}
		before := map[string]bool{}
		for _, e := range c.bindingProblems(shared, w, wcat) {
			before[e.Error()] = true
		}
		for _, e := range c.bindingProblems(eff, w, wcat) {
			if !before[e.Error()] {
				errs = append(errs, partErr("%s: %v", file, e))
			}
		}
	}
	return errs
}
