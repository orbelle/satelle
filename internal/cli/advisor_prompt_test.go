package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/verb"
)

// sty_36b91fd1 AC2 + AC3: init and a passing validate print one line naming the
// workflow advisor, run nothing and write nothing under .satelle/workflows.

func promptCount(out string) int { return strings.Count(out, advisorPrompt()) }

// workflowFiles snapshots every file under the repo's workflows dir.
func workflowFiles(t *testing.T, repo string) map[string]string {
	t.Helper()
	out := map[string]string{}
	dir := filepath.Join(repo, ".satelle", "workflows")
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatal(rerr)
		}
		out[p] = string(b)
		return nil
	})
	return out
}

func wantWorkflowsUnchanged(t *testing.T, repo string, before map[string]string) {
	t.Helper()
	after := workflowFiles(t, repo)
	if len(after) != len(before) {
		t.Errorf("the seam changed the workflow file set: %d files before, %d after", len(before), len(after))
	}
	for p, b := range before {
		if after[p] != b {
			t.Errorf("the seam changed %s", p)
		}
	}
}

func TestAdvisorPromptIsOneLineNamingTheSkill(t *testing.T) {
	line := advisorPrompt()
	if strings.Contains(line, "\n") || !strings.Contains(line, "satelle-workflow-advisor") {
		t.Fatalf("the prompt must be one line naming the advisor: %q", line)
	}
}

func TestRunInitPrintsTheAdvisorPromptOnce(t *testing.T) {
	repo := t.TempDir()
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if n := promptCount(out.String()); n != 1 {
		t.Fatalf("init must print the advisor prompt once, got %d:\n%s", n, out.String())
	}
	before := workflowFiles(t, repo)
	out.Reset()
	if err := runInit(&out, repo, false, nil); err != nil {
		t.Fatalf("re-run init: %v", err)
	}
	if n := promptCount(out.String()); n != 1 {
		t.Errorf("a re-run init prints it once too, got %d", n)
	}
	wantWorkflowsUnchanged(t, repo, before)
}

// validateRepo is a repo whose route validates.
func validateRepo(t *testing.T) string {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"), scheduleRouteDone, scheduleRouteStep(""))
	return repo
}

func TestPassingValidatePrintsTheAdvisorPrompt(t *testing.T) {
	for _, args := range [][]string{{"validate"}, {"workflow", "validate"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			repo := validateRepo(t)
			before := workflowFiles(t, repo)
			out, err := runRootIn(t, "", args...)
			if err != nil {
				t.Fatalf("%v must pass: %v\n%s", args, err, out)
			}
			if n := promptCount(out); n != 1 {
				t.Fatalf("a passing %v prints the prompt once, got %d:\n%s", args, n, out)
			}
			if !strings.HasSuffix(strings.TrimSpace(out), advisorPrompt()) {
				t.Errorf("the prompt comes after every section:\n%s", out)
			}
			wantWorkflowsUnchanged(t, repo, before)
		})
	}
}

// adviceCmd is a command carrying the persistent --json flag the root declares.
func adviceCmd(json bool) *cobra.Command {
	c := &cobra.Command{}
	c.Flags().Bool("json", false, "")
	if json {
		_ = c.Flags().Set("json", "true")
	}
	return c
}

func TestEmitAdvicePrintsEachDistinctSkillOnceAndNeverWithJSON(t *testing.T) {
	skills := []string{"satelle-workflow-advisor", "", "other-advisor", "satelle-workflow-advisor"}
	var buf bytes.Buffer
	emitAdvice(adviceCmd(false), &buf, skills)
	want := advisorPromptFor("satelle-workflow-advisor") + "\n" + advisorPromptFor("other-advisor") + "\n"
	if buf.String() != want {
		t.Errorf("emitAdvice = %q, want %q", buf.String(), want)
	}
	buf.Reset()
	emitAdvice(adviceCmd(true), &buf, skills)
	if buf.Len() != 0 {
		t.Errorf("--json prints no prompt: %q", buf.String())
	}
}

// A container readied as another's child carries its own advice; the command run
// collects it with the top-level results'.
func TestReadyAdviceIncludesNestedResults(t *testing.T) {
	got := readyAdvice([]verb.ReadyResult{
		{ID: "a", Children: []verb.ReadyResult{{ID: "b", Advise: []string{"nested"}}}},
		{ID: "c", Advise: []string{"top"}},
	})
	if strings.Join(got, ",") != "nested,top" {
		t.Errorf("readyAdvice = %v, want [nested top]", got)
	}
}

func TestSetAdviceReadsTheResponse(t *testing.T) {
	if got := setAdvice([]byte(`{"id":"sty_x","advise":["satelle-workflow-advisor"]}`)); len(got) != 1 || got[0] != "satelle-workflow-advisor" {
		t.Errorf("setAdvice = %v", got)
	}
	if got := setAdvice([]byte(`{"id":"sty_x"}`)); len(got) != 0 {
		t.Errorf("a record with no advice carries none: %v", got)
	}
	if got := setAdvice(nil); len(got) != 0 {
		t.Errorf("a handed-off call has no response and no advice: %v", got)
	}
}
