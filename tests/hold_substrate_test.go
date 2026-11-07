//go:build integration

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// substrate reads a file under the repo's .satelle.
func substrate(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", ".satelle", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// section returns the text under heading up to the next heading of the same
// or a higher level; "" when the heading is missing.
func section(text, heading string) string {
	level := strings.Count(strings.Fields(heading)[0], "#")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != heading {
			continue
		}
		var out []string
		for _, m := range lines[i+1:] {
			if f := strings.Fields(m); len(f) > 0 && strings.Trim(f[0], "#") == "" && len(f[0]) <= level {
				break
			}
			out = append(out, m)
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// firstJSON unmarshals the first ```json block of text.
func firstJSON(t *testing.T, text, where string) map[string]any {
	t.Helper()
	_, rest, ok := strings.Cut(text, "```json\n")
	if !ok {
		t.Fatalf("%s: no json block", where)
	}
	block, _, _ := strings.Cut(rest, "```")
	var v map[string]any
	if err := json.Unmarshal([]byte(block), &v); err != nil {
		t.Fatalf("%s: json block: %v\n%s", where, err, block)
	}
	return v
}

// The driver's principle carries the hold's stop in both paragraphs that
// would otherwise have the session present the move out of the park.
func TestDriverStopsAtHold(t *testing.T) {
	text := substrate(t, "principles/satelle-agent-goals.md")
	for _, lead := range []string{"**The workflow is the authority.**", "**A named performer hands the drive back here.**"} {
		found := false
		for _, p := range strings.Split(text, "\n\n") {
			if !strings.HasPrefix(strings.TrimSpace(p), lead) {
				continue
			}
			found = true
			if !strings.Contains(strings.Join(strings.Fields(p), " "), "reports the hold and stops") {
				t.Errorf("paragraph %s does not stop the session at a hold:\n%s", lead, p)
			}
		}
		if !found {
			t.Errorf("no paragraph starts %s", lead)
		}
	}
}

// The plan-review rubric asks a judge for the class a hold names.
func TestPlanReviewAsksClass(t *testing.T) {
	v := section(substrate(t, "skills/satelle-story-plan-review.md"), "## Verdict")
	if c, ok := firstJSON(t, v, "plan-review Verdict")["class"].(string); !ok || c == "" {
		t.Errorf("the verdict example has no class")
	}
	if !strings.Contains(strings.Join(strings.Fields(v), " "), "A reject names its class") {
		t.Errorf("the Verdict section does not ask a reject for its class:\n%s", v)
	}
}

// A re-plan writes the whole plan as a fresh body and answers findings
// beside it, and the reviewer judges only the newest plan.
func TestReplanSkillShape(t *testing.T) {
	plan := substrate(t, "skills/plan.md")
	flat := strings.Join(strings.Fields(plan), " ")
	for _, want := range []string{"write the whole plan as it now stands, as a fresh body", "Answer each finding in an `answers` array beside the artifact"} {
		if !strings.Contains(flat, want) {
			t.Errorf("plan skill lacks %q", want)
		}
	}
	for _, gone := range []string{"byte-identical", "findings_to_answer"} {
		if strings.Contains(plan, gone) {
			t.Errorf("plan skill still names %q", gone)
		}
	}
	obj := firstJSON(t, section(plan, "## Return the structured artifact"), "plan Return the structured artifact")
	if _, ok := obj["artifact"].(map[string]any); !ok {
		t.Errorf("canonical object has no artifact: %v", obj)
	}
	answers, ok := obj["answers"].([]any)
	if !ok || len(answers) == 0 {
		t.Fatalf("canonical object has no answers array: %v", obj)
	}
	first, _ := answers[0].(map[string]any)
	for _, k := range []string{"gate", "binding", "answer", "evidence"} {
		if _, ok := first[k]; !ok {
			t.Errorf("answers entry lacks %q: %v", k, first)
		}
	}
	review := strings.Join(strings.Fields(substrate(t, "skills/satelle-story-plan-review.md")), " ")
	if !strings.Contains(review, "Only the newest plan is live") {
		t.Errorf("plan review does not say only the newest plan is live")
	}
}
