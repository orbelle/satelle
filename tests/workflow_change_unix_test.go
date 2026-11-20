//go:build integration && unix

package tests

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestWorkflowChangeGateIsWiredOnWorkflowRoutes(t *testing.T) {
	routes, err := workflow.Load("../.satelle/workflows")
	if err != nil {
		t.Fatal(err)
	}
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	if sk, ok := skills["satelle-workflow-change-review"]; !ok || sk.IsCheck() {
		t.Fatal("satelle-workflow-change-review is not a judged skill of this repo")
	}
	if sk, ok := skills["satelle-workflow-change-trigger"]; !ok || !sk.IsCheck() {
		t.Fatal("satelle-workflow-change-trigger is not a check skill of this repo")
	}
	for _, c := range []struct {
		workflow, from, to string
		after, before      []string
	}{
		{"delivery", "in_progress", "integration", []string{"definition-unchanged", "judge-differs", "format-vet", "build-unit", "binary-scope", "security", "constitution"}, []string{"code-ac"}},
		{"chore", "in_progress", "integration", []string{"judge-differs", "format-vet", "build-unit", "binary-scope", "security", "constitution"}, []string{"code-ac"}},
		{"substrate", "in_progress", "done", []string{"substrate-paths", "substrate-validate", "judge-differs", "volatile-literals"}, []string{"reference-review"}},
	} {
		w, ok := routes[c.workflow]
		if !ok {
			t.Errorf("no workflow %s", c.workflow)
			continue
		}
		g, ok := w.Gates["workflow-change"]
		if !ok {
			t.Errorf("%s does not declare [gates.workflow-change]", c.workflow)
			continue
		}
		if g.Skill != "satelle-workflow-change-review" {
			t.Errorf("%s workflow-change skill = %q, want satelle-workflow-change-review", c.workflow, g.Skill)
		}
		if g.Trigger != "satelle-workflow-change-trigger" {
			t.Errorf("%s workflow-change trigger = %q, want satelle-workflow-change-trigger", c.workflow, g.Trigger)
		}
		if len(g.Panel) == 0 || g.Combine != workflow.CombineAll {
			t.Errorf("%s workflow-change panel = %v combine = %q, want non-empty panel with combine %q", c.workflow, g.Panel, g.Combine, workflow.CombineAll)
		}
		if len(g.After) < 2 || g.After[0] != notesRule || g.After[1] != referenceRule {
			t.Errorf("%s workflow-change after = %v, want [%s %s ...]", c.workflow, g.After, notesRule, referenceRule)
		}
		tr, err := w.Find(c.from, c.to, "")
		if err != nil {
			t.Errorf("%s %s -> %s: %v", c.workflow, c.from, c.to, err)
			continue
		}
		var order []string
		groupMode := ""
		for _, grp := range tr.Groups {
			if slices.Contains(grp.Gates, "workflow-change") {
				groupMode = grp.Mode
			}
			order = append(order, grp.Gates...)
		}
		pos := slices.Index(order, "workflow-change")
		if pos < 0 {
			t.Errorf("%s %s -> %s does not list workflow-change (%v)", c.workflow, c.from, c.to, order)
			continue
		}
		if groupMode != workflow.Sequential {
			t.Errorf("%s %s -> %s workflow-change group mode = %q, want %q", c.workflow, c.from, c.to, groupMode, workflow.Sequential)
		}
		for _, earlier := range c.after {
			if i := slices.Index(order, earlier); i < 0 || i > pos {
				t.Errorf("%s %s -> %s: %s at %d, want before workflow-change at %d (%v)", c.workflow, c.from, c.to, earlier, i, pos, order)
			}
		}
		for _, later := range c.before {
			if i := slices.Index(order, later); i < 0 || i < pos {
				t.Errorf("%s %s -> %s: %s at %d, want after workflow-change at %d (%v)", c.workflow, c.from, c.to, later, i, pos, order)
			}
		}
	}
}

func workflowChangeRoute(name, to string) string {
	var states string
	if to == "done" {
		states = "[states.in_progress]\nstart = true\n[states.done]\nterminal = true\n"
	} else {
		states = fmt.Sprintf("[states.in_progress]\nstart = true\n[states.%s]\n[states.done]\nterminal = true\n", to)
	}
	var tail string
	if to != "done" {
		tail = fmt.Sprintf("\n[[transition]]\nfrom = [%q]\nto = \"done\"\n", to)
	}
	return fmt.Sprintf(`name = %q
%s
[gates.workflow-change]
skill = "satelle-workflow-change-review"
panel = ["wf-judge"]
combine = "all"
trigger = "satelle-workflow-change-trigger"
after = ["satelle-verdict-notes-check", "satelle-verdict-reference-check"]

[[transition]]
from = ["in_progress"]
to = %q
groups = [{ mode = "sequential", gates = ["workflow-change"] }]
%s`, name, states, to, tail)
}

func workflowChangeSandbox(t *testing.T, wfName, to string) (*sandbox, string) {
	t.Helper()
	s := newSandbox(t)
	s.env["SATELLE_SESSION"] = "wf-change-it"
	fake := fakeClaude(t, s)
	writeFile(t, filepath.Join(fake, "notes.wf-1"), "checked .satelle/workflows/extra.toml states, gates and transitions against [[satelle-reviewer-order]]", 0o644)
	s.must("init")
	s.write(".satelle/agents.toml", "[agents.wf-judge]\ncommand = \"claude\"\nmodel = \"wf-1\"\ntools = [\"Read\", \"Grep\", \"Glob\"]\n")
	s.write(".satelle/workflows/"+wfName+".toml", workflowChangeRoute(wfName, to))
	for _, skill := range []string{
		"satelle-workflow-change-review",
		"satelle-workflow-change-trigger",
		notesRule,
		referenceRule,
	} {
		s.write(".satelle/skills/"+skill+".md", repoText(t, ".satelle/skills/"+skill+".md"))
	}
	s.write("internal/x.go", "package x\n\nfunc Init() {}\n")
	s.write(".satelle/skills/guide.md", "---\nname: guide\n---\n\n# Guide\n\nBaseline.\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "baseline")
	s.mustConfigValid()
	return s, fake
}

func TestWorkflowChangeGateRunsOnWorkflowDiffAndSkipsOtherwise(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"bash", "jq", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the workflow-change trigger and is not on PATH", tool)
		}
	}
	for _, c := range []struct{ workflow, to, nonWorkflowPath, nonWorkflowBody string }{
		{"delivery", "integration", "internal/x.go", "package x\n\nfunc Init() { changed() }\n\nfunc changed() {}\n"},
		{"chore", "integration", "internal/x.go", "package x\n\nfunc Init() { changed() }\n\nfunc changed() {}\n"},
		{"substrate", "done", ".satelle/skills/guide.md", "---\nname: guide\n---\n\n# Guide\n\nUpdated.\n"},
	} {
		t.Run(c.workflow+"/skips-non-workflow", func(t *testing.T) {
			t.Parallel()
			s, fake := workflowChangeSandbox(t, c.workflow, c.to)
			id := s.create("story", "--title", "no workflow change", "--workflow", c.workflow).ID
			s.must("story", "seat", id)
			s.write(c.nonWorkflowPath, c.nonWorkflowBody)
			s.git("add", "-A")
			s.git("commit", "-q", "-m", "non-workflow change")
			s.must("story", "set", id, "--status", c.to)
			if n := len(argvs(t, fake, "wf-1")); n != 0 {
				t.Errorf("workflow-change dispatched judge %d time(s) for %s, want 0", n, c.nonWorkflowPath)
			}
			if g := latestRun(s, id, "workflow-change"); g.Result != "skipped" {
				t.Errorf("workflow-change result = %q, want skipped (%s)", g.Result, g.Notes)
			}
			if st := s.get(id).Status; st != c.to {
				t.Errorf("status = %q, want %q", st, c.to)
			}
		})
		t.Run(c.workflow+"/runs-on-workflow", func(t *testing.T) {
			t.Parallel()
			s, fake := workflowChangeSandbox(t, c.workflow, c.to)
			id := s.create("story", "--title", "workflow change", "--workflow", c.workflow).ID
			s.must("story", "seat", id)
			s.write(".satelle/workflows/extra.toml", extraRoute)
			s.git("add", "-A")
			s.git("commit", "-q", "-m", "workflow change")
			s.must("story", "set", id, "--status", c.to)
			runs := argvs(t, fake, "wf-1")
			if len(runs) != 1 {
				t.Fatalf("workflow-change dispatched judge %d time(s), want 1", len(runs))
			}
			if sys := strings.Join(runs[0], "\n"); !strings.Contains(sys, "--append-system-prompt") || !strings.Contains(sys, "[[satelle-reviewer-order]]") {
				t.Errorf("judge system prompt lacks [[satelle-reviewer-order]]")
			}
			if g := latestRun(s, id, "workflow-change"); g.Result != "accept" {
				t.Errorf("workflow-change result = %q, want accept (%s)", g.Result, g.Notes)
			}
			if st := s.get(id).Status; st != c.to {
				t.Errorf("status = %q, want %q", st, c.to)
			}
		})
	}
}

func TestWorkflowChangeReviewSkillJudgesTOMLAndReviewerOrder(t *testing.T) {
	body := repoText(t, ".satelle/skills/satelle-workflow-change-review.md")
	lower := strings.ToLower(body)
	for _, gone := range []string{"done.toml", "step.toml", "internal/config/substrate/"} {
		if strings.Contains(lower, gone) {
			t.Errorf("satelle-workflow-change-review.md still mentions %q", gone)
		}
	}
	flat := strings.Join(strings.Fields(body), " ")
	for _, want := range []string{
		"satelle help workflows",
		"[states.<name>]",
		"[gates.<name>]",
		"[[transition]]",
		"[[satelle-reviewer-order]]",
		"adds, removes or moves a reviewer gate",
		"record folder",
		"internal/kernel/template/workflows/**",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("satelle-workflow-change-review.md lacks %q", want)
		}
	}
}
