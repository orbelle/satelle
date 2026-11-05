package agentstep

import (
	"context"
	"github.com/bobmcallan/satelle/internal/workitem"
	"strings"
	"testing"
)

// substrateDiff is the shape of what the reviewer resolver returns for a story
// that changed only gitignored substrate (sty_98a2816b): the path in Files and
// its content as a whole-file add in Patch.
func substrateDiff(body string) *DiffState {
	const rel = ".satelle/skills/new-rubric.md"
	return &DiffState{
		Baseline: "abc123",
		Files:    []string{rel},
		Stat:     "# substrate (untracked):\n" + rel + "\n",
		Patch: "# substrate (untracked; whole file shown)\n" +
			"diff --git a/" + rel + " b/" + rel + "\nnew file mode 100644\n" +
			"--- /dev/null\n+++ b/" + rel + "\n@@ -0,0 +1,1 @@\n+" + body + "\n",
		Source: "live",
	}
}

// AC3: the substrate paths ride every judging gate's payload — two review skills
// on two different edges both receive them, and nothing in the engine keys on a
// skill, gate or status name.
func TestSubstrateDiffRidesEveryJudgingGate(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: inventedEdgeWorkflow, skillBody: "rubric", skillFound: true})
	g.SetDiffResolver(func(context.Context, string) *DiffState { return substrateDiff("the instruction text") })

	for _, edge := range []struct{ from, to string }{
		{"backlog", "draft"}, // judged by satelle-story-intent-review
		{"draft", "shipped"}, // judged by satelle-story-done-review
	} {
		if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_substrate", Status: edge.from}, edge.to); err != nil {
			t.Fatalf("%s → %s: %v", edge.from, edge.to, err)
		}
		diff := string(openedAt(t, r.opened, r.got.Payload, "diff"))
		for _, want := range []string{".satelle/skills/new-rubric.md", "the instruction text", "+++ b/.satelle/skills/new-rubric.md"} {
			if !strings.Contains(diff, want) {
				t.Errorf("%s → %s: diff material missing %q:\n%s", edge.from, edge.to, want, diff)
			}
		}
	}
}
