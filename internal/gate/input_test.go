package gate

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestInputIsWhatTheGateReceives(t *testing.T) {
	bound := 500
	gates := map[string]workflow.Gate{
		"chk":   {Skill: "lint", Paths: []string{"a/**"}, Timeout: "3m"},
		"trig":  {Skill: "lint", Trigger: "lint", After: []string{"lint"}},
		"one":   {Skill: "judge", Agent: "y1", Carry: true, DiffMaxBytes: &bound},
		"panel": {Skill: "judge", Panel: []string{"y1", "n1"}},
	}
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"chk", "trig", "one", "panel"}}}, gates)
	r.Skills["lint"] = workflow.Skill{Name: "lint", Path: "skills/lint.md", Meta: map[string]string{"type": "check"}, Script: "exit 1"}
	r.Skills["judge"] = workflow.Skill{Name: "judge", Path: "skills/judge.md", Meta: map[string]string{}, Body: "judge it"}
	r.Base, r.Dir = "b4se", "/tree"
	want := CheckInput{Skill: "lint", Env: r.env().Vars(), Stdin: string(r.env().Payload())}

	chk, err := r.Input("chk")
	if err != nil || chk.Kind != InputCheck || chk.Check == nil || !reflect.DeepEqual(*chk.Check, want) || chk.Trigger != nil {
		t.Fatalf("check input %+v %v", chk, err)
	}
	if chk.SkillPath != "skills/lint.md" || chk.Dir != "/tree" || chk.Timeout.Minutes() != 3 || chk.Paths[0] != "a/**" || chk.DiffMaxBytes != 0 || chk.DiffBoundSource != "" {
		t.Fatalf("check header %+v", chk)
	}
	trig, err := r.Input("trig")
	if err != nil || trig.Trigger == nil || !reflect.DeepEqual(*trig.Trigger, want) || trig.After[0] != "lint" {
		t.Fatalf("trigger input %+v %v", trig, err)
	}
	wantBound := map[string]struct {
		max    int
		source string
	}{
		"one":   {500, "gates.one diff_max_bytes"},
		"panel": {DefaultDiffMaxBytes, "default; set diff_max_bytes on [gates.panel] to change it"},
	}
	for _, name := range []string{"one", "panel"} {
		in, err := r.Input(name)
		if err != nil || in.Kind != InputJudge {
			t.Fatalf("%s: %+v %v", name, in, err)
		}
		rest, _ := r.promptRest(name, gates[name])
		prompt := r.judgePrompt(rest, "")
		if in.Prompt != prompt || !reflect.DeepEqual(in.Judges, gates[name].Judges()) {
			t.Fatalf("%s input %+v", name, in)
		}
		if wb := wantBound[name]; in.DiffMaxBytes != wb.max || in.DiffBoundSource != wb.source {
			t.Fatalf("%s diff bound = %d (%q), want %d (%q)", name, in.DiffMaxBytes, in.DiffBoundSource, wb.max, wb.source)
		}
		for _, j := range gates[name].Judges() {
			if in.Prompts[j] != prompt {
				t.Fatalf("%s prompt[%s] = %q, want %q", name, j, in.Prompts[j], prompt)
			}
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("Input asked judges: %v", d.calls)
	}
	if _, err := r.Input("nope"); err == nil {
		t.Fatal("an undefined gate gave input")
	}
	if res := r.Gate(context.Background(), "one"); res.Result != Accept || res.Attempt != "" || len(d.calls) != 1 {
		t.Fatalf("Gate %+v %v", res, d.calls)
	}
}

func TestPromptNamesTargetWorktree(t *testing.T) {
	gates := map[string]workflow.Gate{"one": {Skill: "judge", Agent: "y1"}}
	r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"one"}}}, gates)
	r.Skills["judge"] = workflow.Skill{Name: "judge", Path: "skills/judge.md", Meta: map[string]string{}, Body: "judge it"}
	r.Base = "b4se"
	self, err := r.Input("one")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(self.Prompt, "Target repository:") || strings.Contains(self.Prompt, "Target work tree:") || self.Worktree != "" {
		t.Fatalf("a self prompt names a target:\n%s", self.Prompt)
	}
	r.Item.Repo, r.Worktree = "web", "/w"
	in, err := r.Input("one")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in.Prompt, "Base commit: b4se\nTarget repository: web\nTarget work tree: /w\n") || in.Worktree != "/w" {
		t.Fatalf("target prompt:\n%s", in.Prompt)
	}
	if strings.Replace(in.Prompt, "Target repository: web\nTarget work tree: /w\nThe base commit, changed paths and diff above are read from that repository, not from your working directory; read its files there.\n", "", 1) != self.Prompt {
		t.Fatalf("the target prompt differs from the self prompt by more than the target lines:\n%s\n---\n%s", in.Prompt, self.Prompt)
	}
}

func TestInputCarriesJudgeBriefs(t *testing.T) {
	gates := map[string]workflow.Gate{
		"one":   {Skill: "judge", Agent: "y1"},
		"same":  {Skill: "judge", Panel: []string{"y1", "y2"}},
		"mixed": {Skill: "judge", Panel: []string{"y1", "n1"}},
	}
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"one", "same", "mixed"}}}, gates)
	r.Item.Kind = "story"
	b1 := r.Agents["y1"]
	b1.Brief = "Focus on the diff."
	r.Agents["y1"] = b1
	b2 := r.Agents["y2"]
	b2.Brief = "Focus on the diff."
	r.Agents["y2"] = b2

	one, err := r.Input("one")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(one.Prompt, "Brief:\nFocus on the diff.\n\nKind: story\n") || one.Prompts["y1"] != one.Prompt {
		t.Fatalf("single briefed input: %+v", one)
	}
	r.Gate(context.Background(), "one")
	if d.prompts["y1"] != one.Prompt {
		t.Fatalf("single briefed dispatch differs from Input:\n--- input\n%s\n--- dispatch\n%s", one.Prompt, d.prompts["y1"])
	}

	same, err := r.Input("same")
	if err != nil {
		t.Fatal(err)
	}
	if same.Prompt != one.Prompt || same.Prompts["y1"] != same.Prompt || same.Prompts["y2"] != same.Prompt {
		t.Fatalf("identical-brief panel input: %+v", same)
	}

	mixed, err := r.Input("mixed")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mixed.Prompts["y1"], "Brief:\nFocus on the diff.\n\nKind: story\n") {
		t.Fatalf("mixed y1 prompt: %s", mixed.Prompts["y1"])
	}
	if strings.Contains(mixed.Prompts["n1"], "Brief:") || mixed.Prompt != mixed.Prompts["n1"] {
		t.Fatalf("mixed n1 / base prompt:\n--- base\n%s\n--- n1\n%s", mixed.Prompt, mixed.Prompts["n1"])
	}
	r.Gate(context.Background(), "mixed")
	for _, j := range []string{"y1", "n1"} {
		if d.prompts[j] != mixed.Prompts[j] {
			t.Fatalf("mixed %s dispatch differs from Input:\n--- input\n%s\n--- dispatch\n%s", j, mixed.Prompts[j], d.prompts[j])
		}
	}
}
