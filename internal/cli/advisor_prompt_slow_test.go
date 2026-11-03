//go:build clislow

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlowFailingValidatePrintsNoAdvisorPrompt(t *testing.T) {
	brokenRoute := func(t *testing.T) string {
		repo := tempRepo(t)
		t.Chdir(repo)
		writeRoute(t, filepath.Join(repo, ".satelle", "workflows"),
			`["*"]
obligations = ["raised", "nowhere"]
`, `[raised]
status = "backlog"
start = true
`)
		return repo
	}
	brokenSkill := func(t *testing.T) string {
		repo := validateRepo(t)
		dir := filepath.Join(repo, ".satelle", "skills")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte("---\nname: bad\n---\n# bad\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return repo
	}
	cases := []struct {
		name string
		repo func(*testing.T) string
		args []string
	}{
		{"broken route, validate", brokenRoute, []string{"validate"}},
		{"broken route, workflow validate", brokenRoute, []string{"workflow", "validate"}},
		{"workflows pass but a skill fails, validate", brokenSkill, []string{"validate"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.repo(t)
			out, err := runRootIn(t, "", tc.args...)
			if err == nil {
				t.Fatalf("%v must fail:\n%s", tc.args, out)
			}
			if n := promptCount(out); n != 0 {
				t.Errorf("a failing %v prints no prompt, got %d:\n%s", tc.args, n, out)
			}
		})
	}

	// The skill fixture fails only the skills section: workflow validate on the
	// same repo passes, so the top-level case above is not a workflow failure.
	brokenSkill(t)
	if out, err := runRootIn(t, "", "workflow", "validate"); err != nil || promptCount(out) != 1 {
		t.Errorf("the broken-skill repo's workflows pass and prompt once: err=%v\n%s", err, out)
	}
}
