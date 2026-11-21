package workflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const PackFile = "pack.toml"

type PackNeeds struct {
	Scripts []string `toml:"scripts"`
	Agents  []string `toml:"agents"`
	Network []string `toml:"network"`
	Secrets []string `toml:"secrets"`
}

type Pack struct {
	File    string    `toml:"-"`
	Name    string    `toml:"name"`
	Version string    `toml:"version"`
	Skills  []string  `toml:"skills"`
	Needs   PackNeeds `toml:"needs"`
}

func LoadPacks(dir string) (map[string]Pack, []UnknownKey, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Pack{}, nil, nil
		}
		return nil, nil, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.Name() == testdataDir {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := map[string]Pack{}
	var unknown []UnknownKey
	var errs []error
	for _, entry := range names {
		entryDir := filepath.Join(dir, entry)
		entryInfo, err := os.Lstat(entryDir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.IsDir() {
			continue
		}
		packPath := filepath.Join(entryDir, PackFile)
		info, err := os.Lstat(packPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, err)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			errs = append(errs, fmt.Errorf("%s: symlink is not allowed", packPath))
			continue
		}
		if !info.Mode().IsRegular() {
			errs = append(errs, fmt.Errorf("%s: is not a regular file", packPath))
			continue
		}
		skillPath := filepath.Join(entryDir, SkillFile)
		skInfo, err := os.Lstat(skillPath)
		if err != nil || !skInfo.Mode().IsRegular() {
			errs = append(errs, fmt.Errorf("%s: entry skill directory skills/%s has no %s", packPath, entry, SkillFile))
			continue
		}
		var pk Pack
		un, err := Decode(packPath, &pk)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		unknown = append(unknown, un...)
		pk.File = packPath
		out[entry] = pk
	}
	return out, unknown, errors.Join(errs...)
}

func (p Pack) Validate(skill SkillLookup) []error {
	file := p.File
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", file, fmt.Sprintf(format, args...)))
	}
	if strings.TrimSpace(p.Name) == "" {
		bad("name is required")
	}
	if strings.TrimSpace(p.Version) == "" {
		bad("version is required")
	}
	if len(p.Skills) == 0 {
		bad("skills is required")
	} else {
		seen := map[string]bool{}
		for _, member := range p.Skills {
			trimmed := strings.TrimSpace(member)
			switch {
			case trimmed == "":
				bad("skills contains an empty skill name")
				continue
			case member != trimmed || member == "." || member == ".." || strings.ContainsAny(member, `/\`):
				bad("skills entry %q is not a valid skill directory name", member)
				continue
			case seen[member]:
				bad("skills lists %q more than once", member)
				continue
			}
			seen[member] = true
			sk, ok := skill(member)
			if !ok {
				bad("member skill %q does not exist (no skills/%s/%s)", member, member, SkillFile)
			} else if !sk.IsDir() {
				bad("member skill %q is a single-file skill (skills/%s.md), not a skill directory (skills/%s/%s)", member, member, member, SkillFile)
			}
		}
	}
	for _, nf := range []struct {
		name string
		vals []string
	}{
		{"scripts", p.Needs.Scripts},
		{"agents", p.Needs.Agents},
		{"network", p.Needs.Network},
		{"secrets", p.Needs.Secrets},
	} {
		for i, v := range nf.vals {
			if strings.TrimSpace(v) == "" {
				bad("needs.%s[%d] is empty", nf.name, i)
			}
		}
	}
	return errs
}
