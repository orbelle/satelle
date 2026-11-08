//go:build integration

package tests

import (
	"strings"
	"testing"
)

// TestPlanSkillKeepsChangelogInTheAttachment pins the rule that a plan's slice
// never edits CHANGELOG.md or .version: the lines are the item's changelog
// attachment and the release writes CHANGELOG.md (sto_59399db5). Without it,
// planners fell back on editing CHANGELOG.md and plan review rejected them.
func TestPlanSkillKeepsChangelogInTheAttachment(t *testing.T) {
	text := repoText(t, ".satelle/skills/plan.md")
	for _, want := range []string{
		"never edits `CHANGELOG.md` or `.version`",
		"`changelog` attachment",
		"scripts/changelog-items.sh",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plan.md does not say %q", want)
		}
	}
}
