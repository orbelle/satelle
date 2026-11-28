package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func skillSource(parts map[string]map[string]workflow.Skill, target string) string {
	if target != reporeg.Self && len(parts[target]) > 0 {
		return target
	}
	return ""
}

func skillsFor(shared map[string]workflow.Skill, parts map[string]map[string]workflow.Skill, target string) map[string]workflow.Skill {
	src := skillSource(parts, target)
	if src == "" {
		return shared
	}
	out := maps.Clone(shared)
	if out == nil {
		out = map[string]workflow.Skill{}
	}
	maps.Copy(out, parts[src])
	return out
}

func (k *Kernel) SkillsFor(it item.Item) (map[string]workflow.Skill, error) {
	if err := k.Resolver().Refusal(it); err != nil {
		return nil, err
	}
	return skillsFor(k.Skills, k.PartSkills, it.Target()), nil
}

func sourcedCoverage(inner, src string) string {
	if src == "" {
		return inner
	}
	s := sha256.Sum256([]byte("satelle-part-skills\x1f" + src + "\x1f" + inner))
	return hex.EncodeToString(s[:])
}

func (c *Config) skillOf(part, name string) (workflow.Skill, bool) {
	if p := c.Parts[part]; part != "" && p != nil {
		if s, ok := p.Skills[name]; ok {
			return s, true
		}
	}
	s, ok := c.Skills[name]
	return s, ok
}

func (c *Config) PartSkills() map[string]map[string]workflow.Skill {
	var out map[string]map[string]workflow.Skill
	for x, p := range c.Parts {
		if len(p.Problems) > 0 || len(p.Skills) == 0 {
			continue
		}
		if out == nil {
			out = map[string]map[string]workflow.Skill{}
		}
		out[x] = p.Skills
	}
	return out
}

func (c *Config) loadPartSkills(p *Part, partDir string) {
	sk, err := workflow.LoadSkillsStrict(filepath.Join(partDir, SkillsDir), reporeg.ReadRegular)
	for _, e := range joinedErrors(err) {
		p.Problems = append(p.Problems, partErr("%s/%s/%v", PartsDir, p.Name, e))
	}
	if len(sk) > 0 {
		p.Skills = sk
	}
	for _, e := range c.partSkillProblems(p) {
		p.Problems = append(p.Problems, partErr("%s/%s/%s: %v", PartsDir, p.Name, SkillsDir, e))
	}
}

func (c *Config) partSkillProblems(p *Part) []error {
	var errs []error
	for _, n := range sortedKeys(p.Skills) {
		s := p.Skills[n]
		errs = append(errs, s.Problems()...)
		shared, ok := c.Skills[n]
		if !ok {
			continue
		}
		if s.Meta[workflow.KeyType] != shared.Meta[workflow.KeyType] || s.Meta[workflow.KeyAfter] != shared.Meta[workflow.KeyAfter] {
			errs = append(errs, fmt.Errorf("skill %s is type %q after %q but the shared skill it overrides is type %q after %q; an override keeps its type and after",
				n, s.Meta[workflow.KeyType], s.Meta[workflow.KeyAfter], shared.Meta[workflow.KeyType], shared.Meta[workflow.KeyAfter]))
		}
		for _, wname := range sortedKeys(c.Workflows) {
			w := c.Workflows[wname]
			if w.Part != "" {
				continue
			}
			for _, gname := range sortedKeys(w.Gates) {
				if g := w.Gates[gname]; g.Skill == n {
					if err := legacyAnswerProblem(w.Name, gname, g, s); err != nil {
						errs = append(errs, err)
					}
				}
			}
		}
	}
	return errs
}

func legacyAnswerProblem(wname, gname string, g workflow.Gate, sk workflow.Skill) error {
	if len(g.Judges()) == 0 || sk.IsCheck() {
		return nil
	}
	key, legacy := gate.LegacyAnswerKey(sk.Body)
	if !legacy {
		return nil
	}
	return fmt.Errorf("workflow %s: gate %q: skill %q answers with %q; a judge answers {\"verdict\":\"accept\"|\"reject\",\"notes\":\"...\"}, and an answer without verdict is a reject",
		wname, gname, g.Skill, key)
}

func joinedErrors(err error) []error {
	if err == nil {
		return nil
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}
