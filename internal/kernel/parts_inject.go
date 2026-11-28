package kernel

import (
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

func (r RuleFiles) injectable(entry string) (string, bool) {
	if entry == ConstitutionFile && r.Constitution != "" {
		return r.Constitution, true
	}
	rest, ok := strings.CutPrefix(entry, PrinciplesDir+"/")
	if !ok {
		return "", false
	}
	stem, ok := strings.CutSuffix(rest, ".md")
	if !ok {
		return "", false
	}
	rel, ok := r.Principles[stem]
	return rel, ok
}

func (c *Config) checkPartInject(p *Part, raw []string) {
	file := PartsDir + "/" + p.Name + "/" + SettingsFile
	seen := map[string]bool{}
	for _, e := range raw {
		if seen[e] {
			p.Problems = append(p.Problems, partErr("%s: hook: inject entry %s is listed twice", file, reporeg.Literal(e)))
			continue
		}
		seen[e] = true
		rel, ok := p.RuleFiles.injectable(e)
		if !ok {
			p.Problems = append(p.Problems, partErr("%s: hook: inject entry %s is not this part's %s or one of its %s/<name>.md", file, reporeg.Literal(e), ConstitutionFile, PrinciplesDir))
			continue
		}
		p.Inject = append(p.Inject, rel)
	}
}

func (c *Config) PartInject() map[string][]string {
	var out map[string][]string
	for x, p := range c.Parts {
		if len(p.Problems) > 0 || p.Inject == nil {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[x] = p.Inject
	}
	return out
}

func (k *Kernel) InjectFor(it item.Item) ([]string, error) {
	if err := k.Resolver().Refusal(it); err != nil {
		return nil, err
	}
	t := it.Target()
	rels, ok := k.PartInject[t]
	if t == reporeg.Self || !ok {
		return nil, nil
	}
	out := make([]string, len(rels))
	for i, rel := range rels {
		out[i] = filepath.Join(k.SubstrateDir(), filepath.FromSlash(rel))
	}
	return out, nil
}
