package workflow

import (
	"fmt"
	"slices"

	"github.com/bobmcallan/satelle/internal/item"
)

type Rules struct {
	Default string `toml:"default"`
	Rule    []Rule `toml:"rule"`
}

type Rule struct {
	Workflow string `toml:"workflow"`
	Category string `toml:"category"`
	Tag      string `toml:"tag"`
	Kind     string `toml:"kind"`
}

func (r Rule) matches(it item.Item) bool {
	return (r.Category == "" || r.Category == it.Category) &&
		(r.Kind == "" || r.Kind == it.Kind) &&
		(r.Tag == "" || slices.Contains(it.Tags, r.Tag))
}

func (r Rules) Select(it item.Item) string {
	for _, rule := range r.Rule {
		if rule.matches(it) {
			return rule.Workflow
		}
	}
	return r.Default
}

func (r Rules) Categories() []string {
	var out []string
	for _, rule := range r.Rule {
		if rule.Category != "" && !slices.Contains(out, rule.Category) {
			out = append(out, rule.Category)
		}
	}
	return out
}

func (r Rules) Validate(exists func(string) bool) []error {
	var errs []error
	if r.Default == "" || !exists(r.Default) {
		errs = append(errs, fmt.Errorf("workflows: default workflow %q does not exist", r.Default))
	}
	return append(errs, r.RuleProblems(exists)...)
}

func (r Rules) RuleProblems(exists func(string) bool) []error {
	var errs []error
	for i, rule := range r.Rule {
		if !exists(rule.Workflow) {
			errs = append(errs, fmt.Errorf("workflows: rule %d names workflow %q, which does not exist", i+1, rule.Workflow))
		}
		if rule.Category == "" && rule.Tag == "" && rule.Kind == "" {
			errs = append(errs, fmt.Errorf("workflows: rule %d has no category, tag or kind", i+1))
		}
	}
	return errs
}
