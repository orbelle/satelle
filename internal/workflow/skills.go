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

// TypeCheck is the frontmatter type of a deterministic check skill.
const TypeCheck = "check"

// Skill is one skills/<name>.md: frontmatter, the markdown body, and for a
// check the script of its first ```sh (or ```check) block.
type Skill struct {
	Name   string
	Path   string
	Meta   map[string]string
	Body   string
	Script string
}

// IsCheck reports whether the skill is a deterministic check.
func (s Skill) IsCheck() bool { return s.Meta["type"] == TypeCheck }

// ParseSkill parses a skill's text: an optional frontmatter block of
// `key: value` lines between "---" lines, then the markdown body.
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
		switch {
		case !in && (trimmed == "```sh" || trimmed == "```check"):
			in = true
		case in && strings.HasPrefix(trimmed, "```"):
			return strings.Join(lines, "\n") + "\n"
		case in:
			lines = append(lines, line)
		}
	}
	return ""
}

// LoadSkills reads every <dir>/*.md. A missing directory has no skills.
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
