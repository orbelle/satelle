package cli

import (
	"strings"
	"testing"
)

// sty_73224b86: the derived route view reports a step's answer_progress rule (and
// only the steps that enable it), and validate refuses a malformed table or one
// on a step that does not propose.

func TestWorkflowShowRouteReportsAnswerProgress(t *testing.T) {
	rs := routeSourceFixture()
	rs.Step = strings.Replace(rs.Step, "requires = [\"raised\"]\n\n[released]",
		"requires = [\"raised\"]\npropose = true\n\n[coded.answer_progress]\nenabled = true\nmin_progress = 2\n\n[released]", 1)
	var b strings.Builder
	if err := renderWorkflowRoute(&b, rs, "feature", nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	var lines []string
	for _, ln := range strings.Split(b.String(), "\n") {
		if strings.Contains(ln, "answer progress:") {
			lines = append(lines, strings.TrimSpace(ln))
		}
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "answer progress: on, min_progress 2 ") || !strings.Contains(lines[0], "refused as unanswered findings before its gates run") {
		t.Fatalf("want exactly one `answer progress: on, min_progress 2` line, got %q in:\n%s", lines, b.String())
	}
	if out := renderRoute(t, "feature", nil); strings.Contains(out, "answer progress") {
		t.Errorf("a route declaring no rule must render none:\n%s", out)
	}
}

func answerProgressRouteStep(propose, table string) string {
	return `[raised]
status = "backlog"
start = true

[ready]
status = "plan"
agent = "planner"
skills = ["plan"]
` + propose + `requires = ["raised"]
` + table + `
[coded]
status = "in_progress"
agent = "executor"
requires = ["ready"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`
}
