package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/structure"
	"github.com/bobmcallan/satelle/internal/wfdot"
)

const (
	instructionReviewSkill  = "satelle-instruction-change-review"
	instructionTriggerSkill = "satelle-instruction-change-trigger"
)

// AC1: the shipped rubric is a structurally valid reviewer skill that states the
// three tests and asks for a verdict whose notes name the failing passage.
func TestInstructionChangeRubricStatesTheThreeTests(t *testing.T) {
	body, ok := embeddedDefault("skills", instructionReviewSkill)
	if !ok {
		t.Fatalf("the binary ships no skills/%s", instructionReviewSkill)
	}
	for _, p := range structure.Doc("skills", instructionReviewSkill, body, nil) {
		t.Errorf("%s: %s", instructionReviewSkill, p)
	}
	for _, want := range []string{
		"Supports accurate, readable code",
		"About satelle and this project",
		"Neither duplicates nor pads",
		`"decision"`, "accept", "Reject",
		"quote it, name the test it", // notes name the failing passage and the test
		"read-only",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rubric does not say %q", want)
		}
	}
	if !strings.Contains(body, "type:reviewer") {
		t.Error("the rubric must be tagged type:reviewer")
	}
	if structure.CheckCommand(body) != "" {
		t.Error("the rubric is an LLM judgement; it must carry no ```check")
	}
}

// AC2: every shipped category whose route has a step an agent can edit under
// carries the gate; a container, which performs no work, does not. The gate runs
// on the default reviewer seat behind the trigger skill (AC4).
func TestEmbeddedRouteCarriesTheInstructionGateWhereWorkIsEdited(t *testing.T) {
	for _, category := range []string{"*", "docs", "execution", "task", "feature"} {
		spec := embeddedRouteFor(t, category)
		got := gateNamed(spec, instructionReviewSkill, "done")
		if got == nil {
			t.Errorf("category %q: no %s gate on done", category, instructionReviewSkill)
			continue
		}
		if got.Agent != "reviewer" {
			t.Errorf("category %q: gate binds %q, want the default reviewer seat", category, got.Agent)
		}
		if got.When != instructionTriggerSkill {
			t.Errorf("category %q: when = %q, want %s", category, got.When, instructionTriggerSkill)
		}
	}
	for _, category := range []string{"epic-parent", "parent"} {
		spec := embeddedRouteFor(t, category)
		// The only step a container can edit under is `merging`, where it lands its
		// children's branches (satelle-epic-merge); it authors no instruction text.
		if got := spec.EditCapableStates(); len(got) != 1 || got[0] != "merging" {
			t.Fatalf("category %q editable steps = %v, want only [merging]", category, got)
		}
		if gateNamed(spec, instructionReviewSkill, "done") != nil {
			t.Errorf("container %q performs no work; it must not carry the gate", category)
		}
	}
}

// AC2: this repo's own route — which owns its gate list — carries the gate on the
// product spine and on the docs, substrate and task-run lanes. The substrate lane
// used to close with no reviewer at all.
func TestRepoRouteCarriesTheInstructionGateOnEveryEditableLane(t *testing.T) {
	root := repoRootFromTest(t)
	done, derr := os.ReadFile(filepath.Join(root, ".satelle", "workflows", "done.toml"))
	step, serr := os.ReadFile(filepath.Join(root, ".satelle", "workflows", "step.toml"))
	if derr != nil || serr != nil {
		t.Skipf("this repo carries no authored route: %v %v", derr, serr)
	}
	lanes := map[string]string{"*": "integration", "docs": "done", "substrate": "done", "execution": "done", "task": "done"}
	for category, on := range lanes {
		spec, err := wfdot.ParseRoute(string(done), string(step), category, nil)
		if err != nil {
			t.Fatalf("derive %q: %v", category, err)
		}
		got := gateNamed(spec, instructionReviewSkill, on)
		if got == nil {
			t.Errorf("category %q: no %s gate on %s in .satelle/workflows/step.toml", category, instructionReviewSkill, on)
			continue
		}
		if got.Agent != "reviewer" || got.When != instructionTriggerSkill {
			t.Errorf("category %q: gate = %+v, want the default reviewer behind %s", category, *got, instructionTriggerSkill)
		}
	}
}

// AC4: no separate lean binding exists — the gate rides the ordinary seat.
func TestNoLeanReviewerBindingIsShipped(t *testing.T) {
	root := repoRootFromTest(t)
	for _, path := range []string{
		filepath.Join(root, ".satelle", "workflows", "agents.toml"),
		filepath.Join(root, "internal", "config", "agents.go"),
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			l := strings.ToLower(strings.TrimSpace(line))
			if strings.HasPrefix(l, "[lean") || strings.Contains(l, `"lean`) {
				t.Errorf("%s:%d declares a lean binding: %s", path, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func gateNamed(spec wfdot.Spec, skill, on string) *wfdot.ScopedReviewer {
	enq, _ := spec.ScopedReviewersSplit(on, nil)
	for i := range enq {
		if enq[i].Skill == skill {
			return &enq[i]
		}
	}
	return nil
}

// --- the trigger script ----------------------------------------------------

const triggerParagraph = "Always check the ledger before you claim a story is done because status is the " +
	"only proof and a local test run proves nothing about the gates the workflow declares."

type triggerRepo struct {
	dir, base string
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTriggerRepo commits a baseline of instruction and non-instruction files.
func newTriggerRepo(t *testing.T, satelleToml string) triggerRepo {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	writeFile(t, dir, ".satelle/skills/a.md", "# A\n\n"+triggerParagraph+"\n")
	writeFile(t, dir, ".satelle/skills/b.md", "# B\n\n"+triggerParagraph+"\n")
	writeFile(t, dir, ".satelle/principles/p.md", "# P\n\n"+triggerParagraph+"\n")
	writeFile(t, dir, "internal/config/substrate/skills/e.md", "# E\n\n"+triggerParagraph+"\n")
	writeFile(t, dir, "README.md", "# readme\n\n"+triggerParagraph+"\n")
	if satelleToml != "" {
		writeFile(t, dir, ".satelle/satelle.toml", satelleToml)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "baseline")
	return triggerRepo{dir: dir, base: gitIn(t, dir, "rev-parse", "HEAD")}
}

// runTrigger runs the shipped check script in the repo with a stub `satelle` that
// answers `story diff` with the given changed files, and returns the exit code.
func runTrigger(t *testing.T, r triggerRepo, baseline bool, files ...string) (int, string) {
	t.Helper()
	body, ok := embeddedDefault("skills", instructionTriggerSkill)
	if !ok {
		t.Fatalf("the binary ships no skills/%s", instructionTriggerSkill)
	}
	script := structure.CheckFence(body)
	if script == "" {
		t.Fatal("the trigger skill carries no ```check block")
	}
	bin := t.TempDir()
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = `"` + f + `"`
	}
	base := ""
	if baseline {
		base = `"baseline_sha":"` + r.base + `",`
	}
	stub := "#!/bin/sh\ncase \"$*\" in\n *--recorded*) printf '{\"files\":[]}' ;;\n" +
		" *) printf '{" + base + "\"files\":[" + strings.Join(quoted, ",") + "]}' ;;\nesac\n"
	writeFile(t, bin, "satelle", stub)
	if err := os.Chmod(filepath.Join(bin, "satelle"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Stdin = strings.NewReader(`{"story":{"id":"sty_abc12345","title":"t"},"from":"in_progress","to":"done"}`)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String()
	}
	t.Fatalf("run trigger: %v", err)
	return -1, ""
}

// An unreadable payload is the script's own error, not a skip: the engine treats
// every exit other than 1 as "run the gate".
func TestInstructionTriggerNeverSkipsOnItsOwnError(t *testing.T) {
	r := newTriggerRepo(t, "")
	body, _ := embeddedDefault("skills", instructionTriggerSkill)
	cmd := exec.Command("bash", "-c", structure.CheckFence(body))
	cmd.Dir = r.dir
	cmd.Stdin = strings.NewReader(`{"story":{}}`)
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() == 1 || ee.ExitCode() == 0 {
		t.Fatalf("a payload without a story id must exit neither 0 nor 1, got %v", err)
	}
}
