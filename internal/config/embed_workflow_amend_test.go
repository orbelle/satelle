package config_test

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// embeddedDefaultBody returns one embedded default artifact's body.
func embeddedDefaultBody(t *testing.T, kind, name string) string {
	t.Helper()
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == kind && d.Name == name {
			return d.Body
		}
	}
	t.Fatalf("the binary must ship %s/%s", kind, name)
	return ""
}

// TestEmbeddedAmendReviewSkillShipsWithItsJudgement (sty_5c768dd3 AC1): the
// rubric ships as a system-scope default carrying the judgement the gate exists
// to make. A gate whose skill does not resolve refuses every amendment, so the
// skill and the hook above must ship together.
func TestEmbeddedAmendReviewSkillShipsWithItsJudgement(t *testing.T) {
	body := embeddedDefaultBody(t, "skills", "satelle-story-amend-review")

	if !strings.Contains(body, "scope: system") {
		t.Error("an embedded default reviewer is scope: system")
	}
	lower := strings.ToLower(body)
	// The one question the gate asks, and both of its answers.
	for _, want := range []string{"correction", "weaken", "## accept", "## reject"} {
		if !strings.Contains(lower, want) {
			t.Errorf("amend-review rubric missing load-bearing phrase %q", want)
		}
	}
	// A weakened AC is the failure the freeze exists to prevent: the rubric must
	// say so, or the promotion has shipped an empty gate.
	if !strings.Contains(lower, "dropped, narrowed, or made vaguer") {
		t.Error("the rubric must name AC weakening as the reject case")
	}
	// It must judge, not transition: no status advice belongs in an amend verdict.
	if !strings.Contains(lower, "read-only") {
		t.Error("the rubric must state the reviewer is read-only")
	}
	// Verdict contract (the engine refuses a reviewer skill without one).
	if !strings.Contains(body, `"decision"`) || !strings.Contains(body, `"notes"`) {
		t.Error("a reviewer skill must document the {decision, notes} verdict contract")
	}
}
