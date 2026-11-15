package gate

import (
	"context"
	"reflect"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestInputIsWhatTheGateReceives(t *testing.T) {
	gates := map[string]workflow.Gate{
		"chk":   {Skill: "lint", Paths: []string{"a/**"}, Timeout: "3m"},
		"trig":  {Skill: "lint", Trigger: "lint", After: []string{"lint"}},
		"one":   {Skill: "judge", Agent: "y1", Carry: true},
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
	if chk.SkillPath != "skills/lint.md" || chk.Dir != "/tree" || chk.Timeout.Minutes() != 3 || chk.Paths[0] != "a/**" {
		t.Fatalf("check header %+v", chk)
	}
	trig, err := r.Input("trig")
	if err != nil || trig.Trigger == nil || !reflect.DeepEqual(*trig.Trigger, want) || trig.After[0] != "lint" {
		t.Fatalf("trigger input %+v %v", trig, err)
	}
	for _, name := range []string{"one", "panel"} {
		in, err := r.Input(name)
		if err != nil || in.Kind != InputJudge {
			t.Fatalf("%s: %+v %v", name, in, err)
		}
		prompt, _ := r.prompt(gates[name])
		if in.Prompt != prompt || !reflect.DeepEqual(in.Judges, gates[name].Judges()) {
			t.Fatalf("%s input %+v", name, in)
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
