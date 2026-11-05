package workflow

import (
	"fmt"
	"slices"

	"github.com/bobmcallan/satelle/internal/item"
)

// Rules choose an item's workflow: the first rule whose conditions all match,
// else the default. They are the [workflows] section of satelle.toml.
type Rules struct {
	Default string `toml:"default"`
	Rule    []Rule `toml:"rule"`
}

// Rule matches an item by any of category, tag and kind (all that are set).
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

// Select returns the workflow for it.
func (r Rules) Select(it item.Item) string {
	for _, rule := range r.Rule {
		if rule.matches(it) {
			return rule.Workflow
		}
	}
	return r.Default
}

// Validate checks every named workflow exists and every rule has a condition.
func (r Rules) Validate(exists func(string) bool) []error {
	var errs []error
	if r.Default == "" || !exists(r.Default) {
		errs = append(errs, fmt.Errorf("workflows: default workflow %q does not exist", r.Default))
	}
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
