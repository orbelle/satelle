package substrate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/gate"
)

const KindVolatile = "volatile"

const VolatileField = "volatile"

type VolatilePattern struct {
	Name  string `toml:"name"`
	Match string `toml:"match"`
}

type VolatileRules struct {
	Paths    []string           `toml:"paths"`
	Exclude  []string           `toml:"exclude"`
	Exempt   string             `toml:"exempt"`
	Dated    string             `toml:"dated"`
	Patterns []VolatilePattern  `toml:"pattern"`
	Upstream []VolatileUpstream `toml:"upstream"`
}

type VolatileUpstream struct {
	Files []string `toml:"files"`
	Owner string   `toml:"owner"`
	Fix   string   `toml:"fix"`
}

func (r VolatileRules) UpstreamOf(file string) (VolatileUpstream, bool) {
	for _, u := range r.Upstream {
		for _, g := range u.Files {
			if gate.Match(g, file) {
				return u, true
			}
		}
	}
	return VolatileUpstream{}, false
}

func (r VolatileRules) Problems() []error {
	var errs []error
	seen := map[string]bool{}
	for i, p := range r.Patterns {
		switch {
		case p.Name == "":
			errs = append(errs, fmt.Errorf("pattern %d has no name", i+1))
		case seen[p.Name]:
			errs = append(errs, fmt.Errorf("pattern %q is named twice", p.Name))
		}
		seen[p.Name] = true
		if p.Match == "" {
			errs = append(errs, fmt.Errorf("pattern %q has no match", p.Name))
		} else if _, err := regexp.Compile(p.Match); err != nil {
			errs = append(errs, fmt.Errorf("pattern %q: match does not compile: %v", p.Name, err))
		}
	}
	if r.Dated != "" {
		if _, err := regexp.Compile(r.Dated); err != nil {
			errs = append(errs, fmt.Errorf("dated does not compile: %v", err))
		}
	}
	for _, key := range []struct {
		name  string
		globs []string
	}{{"paths", r.Paths}, {"exclude", r.Exclude}} {
		for _, g := range key.globs {
			if why := globProblem(g); why != "" {
				errs = append(errs, fmt.Errorf("%s glob %q %s", key.name, g, why))
			}
		}
	}
	for i, u := range r.Upstream {
		if len(u.Files) == 0 {
			errs = append(errs, fmt.Errorf("upstream %d names no files", i+1))
		}
		for _, g := range u.Files {
			if why := globProblem(g); why != "" {
				errs = append(errs, fmt.Errorf("upstream %d files glob %q %s", i+1, g, why))
			}
		}
		if strings.TrimSpace(u.Owner) == "" {
			errs = append(errs, fmt.Errorf("upstream %d names no owner", i+1))
		}
	}
	return errs
}

func globProblem(g string) string {
	if g == "" {
		return "is empty"
	}
	if strings.HasPrefix(g, "/") || filepath.IsAbs(g) {
		return "is absolute: globs are relative to the directory that holds .satelle"
	}
	for _, seg := range strings.Split(g, "/") {
		switch seg {
		case "":
			return "has an empty segment"
		case ".", "..":
			return "has a " + seg + " segment: globs may not leave the directory that holds .satelle"
		case "**":
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Sprintf("has a malformed segment %q", seg)
		}
	}
	return ""
}

type VolatileSet struct {
	rules    VolatileRules
	patterns []*regexp.Regexp
	dated    *regexp.Regexp
}

func CompileVolatile(r VolatileRules) (*VolatileSet, error) {
	if errs := r.Problems(); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	s := &VolatileSet{rules: r}
	for _, p := range r.Patterns {
		s.patterns = append(s.patterns, regexp.MustCompile(p.Match))
	}
	if r.Dated != "" {
		s.dated = regexp.MustCompile(r.Dated)
	}
	return s, nil
}

func VolatileText(file, text string, s *VolatileSet) []Finding {
	if s == nil {
		return nil
	}
	owner := ""
	if u, ok := s.rules.UpstreamOf(file); ok {
		owner = "; owned by " + u.Owner
	}
	md := strings.HasSuffix(file, ".md")
	fence := ""
	var out []Finding
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if md && reFence.MatchString(line) {
			run := fenceRun(line)
			switch {
			case fence == "":
				fence = run
			case run[0] == fence[0] && len(run) >= len(fence) && strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), run[:1])) == "":
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if (s.dated != nil && s.dated.MatchString(line)) || (s.rules.Exempt != "" && strings.Contains(line, s.rules.Exempt)) {
			continue
		}
		for j, p := range s.rules.Patterns {
			re := s.patterns[j]
			m := re.FindString(line)
			if m == "" && !re.MatchString(line) {
				continue
			}
			out = append(out, Finding{File: file, Line: i + 1, Kind: KindVolatile, Subject: p.Name,
				Why: fmt.Sprintf("%q matches %s", strings.TrimSpace(m), p.Match) + owner})
		}
	}
	return out
}

func Volatile(dataDir string, r VolatileRules) ([]Finding, error) {
	if len(r.Patterns) == 0 {
		return nil, nil
	}
	s, err := CompileVolatile(r)
	if err != nil {
		return nil, err
	}
	base := filepath.Dir(dataDir)
	files := map[string]string{}
	for _, g := range s.rules.Paths {
		if err := s.walkGlob(base, g, files); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Finding
	for _, n := range names {
		b, err := os.ReadFile(files[n])
		if err != nil {
			return nil, err
		}
		out = append(out, VolatileText(n, string(b), s)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

func (s *VolatileSet) excluded(name string) bool {
	for _, g := range s.rules.Exclude {
		if gate.Match(g, name) {
			return true
		}
	}
	return false
}

func (s *VolatileSet) pruned(name string) bool {
	for _, g := range s.rules.Exclude {
		if d, ok := strings.CutSuffix(g, "/**"); ok && gate.Match(d, name) {
			return true
		}
	}
	return false
}

func (s *VolatileSet) walk(base, root string, keep func(name string) bool, files map[string]string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == root {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			if p != root && s.pruned(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && keep(name) && !s.excluded(name) {
			files[name] = p
		}
		return nil
	})
}

func (s *VolatileSet) walkGlob(base, g string, files map[string]string) error {
	segs := strings.Split(g, "/")
	lit := 0
	for lit < len(segs) && !strings.ContainsAny(segs[lit], `*?[\`) {
		lit++
	}
	dir := base
	for i := 0; i < lit; i++ {
		dir = filepath.Join(dir, segs[i])
		fi, err := os.Lstat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		name := strings.Join(segs[:i+1], "/")
		if i == len(segs)-1 {
			if fi.Mode().IsRegular() && !s.excluded(name) {
				files[name] = dir
			}
			return nil
		}
		if !fi.IsDir() || s.pruned(name) {
			return nil
		}
	}
	return s.walk(base, dir, func(name string) bool { return gate.Match(g, name) }, files)
}

func fenceRun(line string) string {
	s := strings.TrimLeft(line, " \t")
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	return s[:n]
}
