package substrate

// The volatile-literal check: each line of the repo's substrate Markdown, and
// of the files .satelle/volatile.toml names, that matches a configured
// volatile pattern (a version string, "currently", a model id) is a finding,
// unless the line is dated history or carries the configured exemption
// marker. The patterns, the marker, the extra paths, the exclusions and the
// upstream owners are all configuration: this file holds none of them. Two
// rules are fixed: the date exemption (sto_3de33a72), and that a fenced code
// block in a Markdown file is not read, by the fence the substrate check
// already owns (reFence; sto_1ee018c8).

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

// KindVolatile is the kind of a volatile-literal finding.
const KindVolatile = "volatile"

// VolatileField names the volatile findings wherever they are reported: the
// key of `satelle validate --json`'s list of them and the name of satelle
// doctor's check that reports them.
const VolatileField = "volatile"

// VolatilePattern is one [[pattern]] of volatile.toml: a name each finding
// carries and a Go regular expression (RE2) run on each line.
type VolatilePattern struct {
	Name  string `toml:"name"`
	Match string `toml:"match"`
}

// VolatileRules is volatile.toml. Paths are extra files to read and Exclude
// the files never read, both slash-separated globs relative to the directory
// that holds .satelle, in which "**" stands for any number of whole segments.
// A line containing Exempt ("" = no marker) is not reported.
// Upstream names the files another repo or path owns: they are still read,
// and each of their findings names the owner.
type VolatileRules struct {
	Paths    []string           `toml:"paths"`
	Exclude  []string           `toml:"exclude"`
	Exempt   string             `toml:"exempt"`
	Patterns []VolatilePattern  `toml:"pattern"`
	Upstream []VolatileUpstream `toml:"upstream"`
}

// VolatileUpstream is one [[upstream]] of volatile.toml: the files (globs, as
// paths and exclude are) that Owner owns, and an optional Fix, the command that
// brings the fixed files here. A finding in such a file is fixed at the owner,
// not in place.
type VolatileUpstream struct {
	Files []string `toml:"files"`
	Owner string   `toml:"owner"`
	Fix   string   `toml:"fix"`
}

// UpstreamOf returns the first [[upstream]] entry, in configuration order,
// whose files glob matches file.
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

// Problems returns each problem of the rules: a pattern with no name, a name
// given twice, an empty or uncompilable match, and each paths or exclude glob
// that is empty, absolute, has an empty, "." or ".." segment, or a malformed
// segment.
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

// globProblem says why g cannot be a glob relative to the base, "" when it can.
// It reads the raw string: path.Clean would hide a ".." or an empty segment.
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

// VolatileSet is a compiled, valid VolatileRules; CompileVolatile is its only
// constructor.
type VolatileSet struct {
	patterns []compiledPattern
	exempt   string
	paths    []string
	exclude  []string
	rules    VolatileRules
}

type compiledPattern struct {
	name, match string
	re          *regexp.Regexp
}

// CompileVolatile compiles r, or returns every problem of r.
func CompileVolatile(r VolatileRules) (*VolatileSet, error) {
	if errs := r.Problems(); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	s := &VolatileSet{exempt: r.Exempt, paths: r.Paths, exclude: r.Exclude, rules: r}
	for _, p := range r.Patterns {
		s.patterns = append(s.patterns, compiledPattern{name: p.Name, match: p.Match, re: regexp.MustCompile(p.Match)})
	}
	return s, nil
}

// reDated is the one owner of "dated history": a YYYY-MM-DD date on the line.
var reDated = regexp.MustCompile(`\b[0-9]{4}-[0-9]{2}-[0-9]{2}\b`)

// VolatileText returns the findings of one file's text, named file: one per
// pattern a line matches, in configuration order, skipping, in a Markdown file,
// every line of a fenced code block (the fences too), then a dated line and a
// line holding the exemption marker. A file an [[upstream]] entry matches has
// each finding's Why end in "; owned by <owner>". A nil set reports nothing.
func VolatileText(file, text string, s *VolatileSet) []Finding {
	if s == nil {
		return nil
	}
	owner := ""
	if u, ok := s.rules.UpstreamOf(file); ok {
		owner = "; owned by " + u.Owner
	}
	md := strings.HasSuffix(file, ".md")
	fence := "" // the open fence's run of ``` or ~~~, "" outside a fence
	var out []Finding
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if md && reFence.MatchString(line) {
			run := fenceRun(line)
			switch {
			case fence == "":
				fence = run
			case run[0] == fence[0] && len(run) >= len(fence) && strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), run[:1])) == "":
				// Closed only by the same character, at least as long, with
				// nothing after it (CommonMark): a ~~~ inside a ``` block
				// is the block's text.
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if reDated.MatchString(line) || (s.exempt != "" && strings.Contains(line, s.exempt)) {
			continue
		}
		for _, p := range s.patterns {
			m := p.re.FindString(line)
			if m == "" && !p.re.MatchString(line) {
				continue
			}
			out = append(out, Finding{File: file, Line: i + 1, Kind: KindVolatile, Subject: p.name,
				Why: fmt.Sprintf("%q matches %s", strings.TrimSpace(m), p.match) + owner})
		}
	}
	return out
}

// Volatile checks the files the rules name: every *.md under dataDir plus
// every regular file a paths glob matches, less every file an exclude glob
// matches, each read once. Names, and the globs, are slash paths relative to
// dataDir's parent. With no pattern it reads nothing and reports nothing; with
// a problem in the rules it reads nothing and returns the problems.
func Volatile(dataDir string, r VolatileRules) ([]Finding, error) {
	if len(r.Patterns) == 0 {
		return nil, nil
	}
	s, err := CompileVolatile(r)
	if err != nil {
		return nil, err
	}
	base := filepath.Dir(dataDir)
	files := map[string]string{} // name → path on disk
	if err := s.walk(base, dataDir, func(name string) bool { return strings.HasSuffix(name, ".md") }, files); err != nil {
		return nil, err
	}
	for _, g := range s.paths {
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

// excluded reports whether the file name is never read.
func (s *VolatileSet) excluded(name string) bool {
	for _, g := range s.exclude {
		if gate.Match(g, name) {
			return true
		}
	}
	return false
}

// pruned reports whether the directory name is not entered: it matches an
// exclude glob with its trailing "/**" removed.
func (s *VolatileSet) pruned(name string) bool {
	for _, g := range s.exclude {
		if d, ok := strings.CutSuffix(g, "/**"); ok && gate.Match(d, name) {
			return true
		}
	}
	return false
}

// walk adds to files each regular file under root that keep accepts and no
// exclude glob matches, entering no directory the exclusions prune. Symlinks
// are neither followed nor read. A missing root holds nothing.
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

// walkGlob adds the files glob g matches, walking only the subtree its
// literal leading segments name (one Lstat for a glob with no wildcard). A
// symlink on the literal prefix ends the walk: nothing outside base is read.
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

// fenceRun is the run of fence characters (``` or ~~~, three or more) a fence
// line opens with, after its indent.
func fenceRun(line string) string {
	s := strings.TrimLeft(line, " \t")
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	return s[:n]
}
