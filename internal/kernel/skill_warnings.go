package kernel

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type SkillWarning struct {
	Skill string `json:"skill"`
	File  string `json:"file"`
}

func (w SkillWarning) String() string {
	return fmt.Sprintf("%s: skill %s: named by no workflow, doctor check or [hook] inject entry, and by no skill one of them reaches", w.File, w.Skill)
}

var skillPath = regexp.MustCompile(`skills/([A-Za-z0-9_-]+)\.md`)

func (c *Config) SkillWarnings() []SkillWarning {
	base := filepath.Base(c.Root)
	reached := map[string]bool{}
	var queue []string
	reach := func(name string) {
		if _, ok := c.Skills[name]; ok && !reached[name] {
			reached[name] = true
			queue = append(queue, name)
		}
	}
	for _, w := range c.Workflows {
		for _, name := range w.SkillNames() {
			reach(name)
		}
	}
	for _, ch := range c.Doctor.Checks {
		reach(ch.Skill)
	}
	injectPrefix := base + "/" + SkillsDir + "/"
	for _, p := range c.Settings.Hook.Inject {
		clean := path.Clean(filepath.ToSlash(p))
		if strings.HasPrefix(clean, injectPrefix) && strings.HasSuffix(clean, ".md") {
			reach(strings.TrimSuffix(strings.TrimPrefix(clean, injectPrefix), ".md"))
		}
	}
	if shipped, err := ShippedFiles(); err == nil {
		for _, f := range shipped {
			if rest, ok := strings.CutPrefix(f, SkillsDir+"/"); ok && strings.HasSuffix(rest, ".md") {
				reach(strings.TrimSuffix(rest, ".md"))
			}
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, m := range skillPath.FindAllStringSubmatch(c.Skills[name].Body, -1) {
			reach(m[1])
		}
	}
	names := make([]string, 0, len(c.Skills))
	for name := range c.Skills {
		if !reached[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := []SkillWarning{}
	for _, name := range names {
		out = append(out, SkillWarning{Skill: name, File: base + "/" + SkillsDir + "/" + name + ".md"})
	}
	return out
}
