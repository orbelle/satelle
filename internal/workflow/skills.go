package workflow

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const TypeCheck = "check"

type Skill struct {
	Name   string
	Path   string
	Meta   map[string]string
	Body   string
	Script string
}

func (s Skill) IsCheck() bool { return s.Meta["type"] == TypeCheck }

func (s Skill) AfterAlways() bool { return s.IsCheck() && s.Meta["after"] == afterAlways }

const afterAlways = "always"

func ParseSkill(name, text string) (Skill, error) {
	s := Skill{Name: name, Meta: map[string]string{}, Body: text}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			return s, fmt.Errorf("skill %s: unterminated frontmatter", name)
		}
		for _, line := range lines[1:end] {
			k, v, ok := strings.Cut(line, ":")
			if ok && !strings.HasPrefix(strings.TrimSpace(k), "#") {
				s.Meta[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
		s.Body = strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n")
	}
	s.Script = firstShellBlock(s.Body)
	if s.IsCheck() && strings.TrimSpace(s.Script) == "" {
		return s, fmt.Errorf("skill %s: a check needs a ```sh or ```check block", name)
	}
	return s, nil
}

func firstShellBlock(body string) string {
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var lines []string
	in := false
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		_, opens := scriptFence(trimmed)
		switch {
		case !in && opens:
			in = true
		case in && strings.HasPrefix(trimmed, "```"):
			return strings.Join(lines, "\n") + "\n"
		case in:
			lines = append(lines, line)
		}
	}
	return ""
}

func scriptFence(trimmed string) (string, bool) {
	switch trimmed {
	case "```sh":
		return "sh", true
	case "```check":
		return "check", true
	}
	return "", false
}

func (s Skill) Problems() []error {
	if v, ok := s.Meta["after"]; ok {
		switch {
		case v != afterAlways:
			return []error{fmt.Errorf("skill %s: frontmatter after is %q; the only value is %s", s.Name, v, afterAlways)}
		case !s.IsCheck():
			return []error{fmt.Errorf("skill %s: frontmatter after: %s is for a check, but its type is %q", s.Name, afterAlways, s.Meta["type"])}
		}
	}
	if s.IsCheck() {
		return nil
	}
	for _, line := range strings.Split(s.Body, "\n") {
		if fence, ok := scriptFence(strings.TrimSpace(line)); ok && fence == "check" {
			return []error{fmt.Errorf("skill %s: holds a ```check block but its frontmatter type is %q, not check; v1 runs a script only for type: check (set type: check, or make the block ```sh if it is an example)",
				s.Name, s.Meta["type"])}
		}
	}
	return nil
}

func LoadSkills(dir string) (map[string]Skill, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := map[string]Skill{}
	var errs []error
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		name := strings.TrimSuffix(filepath.Base(p), ".md")
		s, err := ParseSkill(name, string(b))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s.Path = p
		out[name] = s
	}
	return out, errors.Join(errs...)
}
