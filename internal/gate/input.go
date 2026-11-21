package gate

import (
	"fmt"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
)

const (
	InputCheck = "check"
	InputJudge = "judge"
)

type CheckInput struct {
	Skill string
	Env   []string
	Stdin string
}

type Input struct {
	Gate, Skill, SkillPath string
	Kind                   string
	Dir                    string
	Timeout                time.Duration
	TimeoutSource          string
	DiffMaxBytes           int
	DiffBoundSource        string
	Placement              string
	Paths                  []string
	Carry                  bool
	Check, Trigger         *CheckInput
	Head                   Head
	HeadErr                string
	Judges                 []string
	Tier                   string
	Prompt                 string
	Prompts                map[string]string
	After                  []string
}

func (r Run) Input(name string) (Input, error) {
	g, tier, ok, err := r.gate(name)
	if !ok {
		return Input{}, fmt.Errorf("gate %q is not defined", name)
	}
	if err != nil {
		return Input{}, err
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
	in.Tier = tier
	diffMax, diffSource, err := r.diffBound(name, g)
	if err != nil {
		return in, err
	}
	in.DiffMaxBytes, in.DiffBoundSource = diffMax, diffSource
	rest, err := r.promptRest(name, g)
	if err != nil {
		return in, err
	}
	in.Prompts = make(map[string]string, len(in.Judges))
	same := len(in.Judges) > 0
	for i, j := range in.Judges {
		p := r.judgePrompt(rest, r.Agents[j].Brief)
		in.Prompts[j] = p
		if i > 0 && p != in.Prompts[in.Judges[0]] {
			same = false
		}
	}
	if same {
		in.Prompt = in.Prompts[in.Judges[0]]
	} else {
		in.Prompt = r.judgePrompt(rest, "")
	}
	return in, nil
}

func (r Run) checkInput(skill string) *CheckInput {
	env := r.envFor(r.Skills[skill])
	return &CheckInput{Skill: skill, Env: env.Vars(), Stdin: string(env.Payload())}
}
