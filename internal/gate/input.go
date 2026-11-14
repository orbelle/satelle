package gate

import (
	"fmt"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
)

// Kinds of gate an Input describes.
const (
	InputCheck = "check"
	InputJudge = "judge"
)

// CheckInput is what one check script receives: its SATELLE_* variables and
// the JSON it reads on stdin.
type CheckInput struct {
	Skill string
	Env   []string
	Stdin string
}

// Input is what one gate of a run would receive, built by the same functions
// the run uses, with nothing started: no check, trigger, judge or remote node.
type Input struct {
	Gate, Skill, SkillPath string
	Kind                   string // InputCheck or InputJudge
	Dir                    string
	Timeout                time.Duration
	TimeoutSource          string
	Placement              string
	Paths                  []string
	Carry                  bool
	// Check is the check script's input; Trigger the trigger's, when the
	// gate has one (a trigger runs before the check or judge).
	Check, Trigger *CheckInput
	// Head is the pushed head a remote-placed check would judge; HeadErr why
	// there is none.
	Head    Head
	HeadErr string
	// Judges are the bindings asked, and Prompt what each is asked, with the
	// skill's body (at SkillPath) as system.
	Judges []string
	Prompt string
	// After are the after checks; their input adds the gate's verdict, which
	// exists only once the gate has run.
	After []string
}

// Input reports what the named gate would receive on this run.
func (r Run) Input(name string) (Input, error) {
	g, ok := r.Workflow.Gates[name]
	if !ok {
		return Input{}, fmt.Errorf("gate %q is not defined", name)
	}
	limit, source, err := r.bound(name, g)
	if err != nil {
		return Input{}, err
	}
	in := Input{
		Gate: name, Skill: g.Skill, Dir: r.Dir, Timeout: limit, TimeoutSource: source,
		Placement: g.Placement, Paths: g.Paths, Carry: g.Carry, After: g.After,
	}
	skill, ok := r.Skills[g.Skill]
	if !ok {
		return in, fmt.Errorf("skill %q is missing", g.Skill)
	}
	in.SkillPath = skill.Path
	if g.Trigger != "" {
		in.Trigger = r.checkInput(g.Trigger)
	}
	if skill.IsCheck() {
		in.Kind = InputCheck
		in.Check = r.checkInput(g.Skill)
		if strings.HasPrefix(g.Placement, agent.PlaceRemote) {
			in.Head = r.Head
			if r.HeadErr != nil {
				in.HeadErr = r.HeadErr.Error()
			}
		}
		return in, nil
	}
	in.Kind = InputJudge
	in.Judges = g.Judges()
	if in.Prompt, err = r.prompt(g); err != nil {
		return in, err
	}
	return in, nil
}

// checkInput is what a check skill run by this run receives.
func (r Run) checkInput(skill string) *CheckInput {
	env := r.env()
	return &CheckInput{Skill: skill, Env: env.Vars(), Stdin: string(env.Payload())}
}
