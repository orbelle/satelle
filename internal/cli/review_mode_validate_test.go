package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// sty_93359e0f: a gate declares its review mode in the workflow configuration;
// `satelle validate` accepts full and fail-fast and refuses anything else.

const reviewModeDone = `["*"]
obligations = ["raised", "readied", "closed"]
`

const reviewModeSteps = `[raised]
status = "backlog"
start = true

[readied]
status = "plan"
reviewers = ["gate-a", "gate-b"]
requires = ["raised"]
%s
[closed]
status = "done"
terminal = true
requires = ["readied"]

[[gate]]
skill = "scoped-judge"
on = ["plan"]
%s
`

func validateWithRoute(t *testing.T, stepKeys, gateKeys string) (string, error) {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	for _, skill := range []string{"gate-a", "gate-b", "scoped-judge"} {
		writeFile(t, repo, ".satelle/skills/"+skill+".md", alwaysPassSkill(skill))
	}
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"), reviewModeDone,
		strings.Replace(strings.Replace(reviewModeSteps, "%s", stepKeys, 1), "%s", gateKeys, 1))
	return runRootIn(t, "", "validate")
}

func TestValidateRefusesAnUnknownReviewMode(t *testing.T) {
	out, err := validateWithRoute(t, "", `review_mode = "quick"`)
	if err == nil {
		t.Fatalf("validate accepted review_mode = \"quick\":\n%s", out)
	}
	got := err.Error() + out
	for _, want := range []string{"review_mode", `"quick"`, "fail-fast"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestValidateRefusesAFailFastEntryThatIsNotAReviewer(t *testing.T) {
	out, err := validateWithRoute(t, `fail_fast = ["nobody"]`, "")
	if err == nil {
		t.Fatalf("validate accepted a fail_fast entry naming no reviewer:\n%s", out)
	}
	got := err.Error() + out
	for _, want := range []string{"fail_fast", `"nobody"`} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}
