package config

import (
	"regexp"
	"testing"
)

// sty_b9d9a3df: the shipped container route carries the epic-branch lane. These
// resolve the embedded halves with no repo overlay, which is the route a repo
// with no workflow of its own runs.

func embeddedSkillBody(t *testing.T, name string) string {
	t.Helper()
	for _, d := range EmbeddedDefaults() {
		if d.Kind == "skills" && d.Name == name {
			return d.Body
		}
	}
	t.Fatalf("no embedded skill %q ships", name)
	return ""
}

// ghWord matches the GitHub CLI as a word, not a substring of another word.
var ghWord = regexp.MustCompile(`\bgh\b`)
