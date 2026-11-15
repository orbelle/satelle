//go:build integration

package tests

import (
	"strings"
	"testing"
)

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
