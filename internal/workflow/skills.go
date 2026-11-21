package workflow

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	TypeCheck     = "check"
	TypePrinciple = "principle"
)

const (
	KeyName        = "name"
	KeyType        = "type"
	KeyDescription = "description"
	KeyAfter       = "after"
	KeyFraming     = "framing"
	KeyCommands    = "commands"
)

var FrontmatterKeys = []string{KeyName, KeyType, KeyDescription, KeyAfter, KeyFraming, KeyCommands}

const (
	readmeFile  = "README.md"
	testdataDir = "testdata"
	SkillFile   = "SKILL.md"
)

type Skill struct {
	Name   string
	Path   string
	Dir    string
	Meta   map[string]string
	Lines  map[string]int
	Body   string
	Script string
}

func (s Skill) IsDir() bool { return s.Dir != "" }

func (s Skill) IsCheck() bool { return s.Meta[KeyType] == TypeCheck }

func (s Skill) IsFraming() bool {
	return s.Meta[KeyType] == TypePrinciple && s.Meta[KeyFraming] == "true"
}

func (s Skill) AfterAlways() bool { return s.IsCheck() && s.Meta[KeyAfter] == afterAlways }

const afterAlways = "always"

func ParseSkill(name, text string) (Skill, error) {
	s := Skill{Name: name, Meta: map[string]string{}, Lines: map[string]int{}, Body: text}
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
		for i, line := range lines[1:end] {
			k, v, ok := strings.Cut(line, ":")
			if ok && !strings.HasPrefix(strings.TrimSpace(k), "#") {
				s.Meta[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
				s.Lines[strings.TrimSpace(k)] = i + 2
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
	var errs []error
	if v, ok := s.Meta[KeyAfter]; ok {
		switch {
		case v != afterAlways:
			errs = append(errs, fmt.Errorf("skill %s: frontmatter after is %q; the only value is %s", s.Name, v, afterAlways))
		case !s.IsCheck():
			errs = append(errs, fmt.Errorf("skill %s: frontmatter after: %s is for a check, but its type is %q", s.Name, afterAlways, s.Meta[KeyType]))
		}
	}
	if !s.IsCheck() {
		for _, line := range strings.Split(s.Body, "\n") {
			if fence, ok := scriptFence(strings.TrimSpace(line)); ok && fence == "check" {
				errs = append(errs, fmt.Errorf("skill %s: holds a ```check block but its frontmatter type is %q, not check; v1 runs a script only for type: check (set type: check, or make the block ```sh if it is an example)",
					s.Name, s.Meta[KeyType]))
				break
			}
		}
	}
	if s.IsDir() {
		errs = append(errs, s.dirPathProblems()...)
	}
	return errs
}

func (s Skill) dirPathProblems() []error {
	var errs []error
	seenMissing := map[string]bool{}
	seenForeign := map[string]bool{}
	addMissing := func(clean, ref string) {
		if seenMissing[clean] {
			return
		}
		seenMissing[clean] = true
		errs = append(errs, fmt.Errorf("skill %s: missing path %s (does not exist under skills/%s/)", s.Name, ref, s.Name))
	}
	addForeign := func(ref string) {
		if seenForeign[ref] {
			return
		}
		seenForeign[ref] = true
		errs = append(errs, fmt.Errorf("skill %s: path %s is not its own (a skill directory may only reference its own scripts/ and assets/)", s.Name, ref))
	}
	checkCandidate := func(raw string, inShell bool) {
		if raw == "" {
			return
		}
		norm := strings.TrimPrefix(raw, "$SATELLE_SKILL_DIR/")
		norm = strings.TrimPrefix(norm, "${SATELLE_SKILL_DIR}/")
		norm = strings.TrimPrefix(norm, "./")
		if rest, ok := strings.CutPrefix(norm, ".satelle/scripts/"); ok {
			if rest != "" && !hasPlaceholder(rest) {
				addForeign(raw)
			}
			return
		}
		if rest, ok := strings.CutPrefix(norm, "../"); ok {
			if rest != "" && !hasPlaceholder(rest) {
				addForeign(raw)
			}
			return
		}
		if rest, ok := cutSkillsPrefix(norm); ok {
			other, sub, hasSlash := strings.Cut(rest, "/")
			if !hasSlash || other == "" || sub == "" || hasPlaceholder(other) || hasPlaceholder(sub) {
				return
			}
			subClean := path.Clean(sub)
			if subClean == ".." || strings.HasPrefix(subClean, "../") {
				addForeign(raw)
				return
			}
			if other != s.Name {
				if inShell || subClean != SkillFile {
					addForeign(raw)
				}
			}
			return
		}
		var prefix, tail string
		switch {
		case strings.HasPrefix(norm, "scripts/"):
			prefix, tail = "scripts/", strings.TrimPrefix(norm, "scripts/")
		case strings.HasPrefix(norm, "assets/"):
			prefix, tail = "assets/", strings.TrimPrefix(norm, "assets/")
		default:
			return
		}
		if tail == "" || hasPlaceholder(tail) {
			return
		}
		clean := path.Clean(prefix + tail)
		if !strings.HasPrefix(clean, "scripts/") && !strings.HasPrefix(clean, "assets/") {
			addForeign(raw)
			return
		}
		if _, err := os.Stat(filepath.Join(s.Dir, filepath.FromSlash(clean))); err != nil {
			addMissing(clean, clean)
		}
	}
	checkToken := func(tok string, inShell bool) {
		checkCandidate(tok, inShell)
		if _, rhs, ok := strings.Cut(tok, "="); ok && rhs != "" {
			checkCandidate(rhs, inShell)
		}
	}

	var fenceCh byte
	fenceCount := 0
	inShell := false
	for _, line := range strings.Split(s.Body, "\n") {
		trimmed := strings.TrimSpace(line)
		if fenceCount > 0 {
			if fenceClose(trimmed, fenceCh, fenceCount) {
				fenceCh, fenceCount, inShell = 0, 0, false
				continue
			}
			if inShell {
				for _, tok := range shellTokens(line, true) {
					checkToken(tok, true)
				}
			}
			continue
		}
		if ch, n, lang, ok := fenceOpen(trimmed); ok {
			fenceCh, fenceCount = ch, n
			inShell = lang == "sh" || lang == "bash" || lang == "check"
			continue
		}
		for _, span := range backtickSpans(line) {
			for _, tok := range shellTokens(span, false) {
				checkToken(tok, false)
			}
		}
	}
	return errs
}

func cutSkillsPrefix(s string) (string, bool) {
	if rest, ok := strings.CutPrefix(s, ".satelle/skills/"); ok {
		return rest, true
	}
	return strings.CutPrefix(s, "skills/")
}

func hasPlaceholder(s string) bool {
	return strings.ContainsAny(s, "<>$*?[{")
}

func fenceOpen(trimmed string) (byte, int, string, bool) {
	if len(trimmed) < 3 {
		return 0, 0, "", false
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return 0, 0, "", false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, "", false
	}
	rest := strings.TrimSpace(trimmed[n:])
	if ch == '`' && strings.ContainsRune(rest, '`') {
		return 0, 0, "", false
	}
	lang := rest
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		lang = rest[:i]
	}
	return ch, n, lang, true
}

func fenceClose(trimmed string, ch byte, minCount int) bool {
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	return n >= minCount && strings.TrimSpace(trimmed[n:]) == ""
}

func backtickSpans(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '`' {
			continue
		}
		n := 1
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		fence := strings.Repeat("`", n)
		end := strings.Index(s[i+n:], fence)
		if end < 0 {
			break
		}
		out = append(out, s[i+n:i+n+end])
		i += n + end + n - 1
	}
	return out
}

func shellTokens(s string, stripComments bool) []string {
	var out []string
	var cur strings.Builder
	var stack []byte
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		top := byte(0)
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch top {
		case '\'':
			if c == '\'' {
				stack = stack[:len(stack)-1]
			} else {
				cur.WriteByte(c)
			}
		case '"':
			switch {
			case c == '"':
				stack = stack[:len(stack)-1]
			case c == '\\' && i+1 < len(s):
				i++
				cur.WriteByte(s[i])
			case c == '$' && i+1 < len(s) && s[i+1] == '(':
				flush()
				stack = append(stack, '(')
				i++
			default:
				cur.WriteByte(c)
			}
		default:
			switch {
			case stripComments && c == '#' && cur.Len() == 0:
				return out
			case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ';' || c == '|' || c == '&' || c == '`':
				flush()
			case c == '(':
				flush()
				stack = append(stack, '(')
			case c == ')':
				flush()
				if top == '(' {
					stack = stack[:len(stack)-1]
				}
			case c == '\'' || c == '"':
				stack = append(stack, c)
			case c == '\\' && i+1 < len(s):
				i++
				cur.WriteByte(s[i])
			case c == '<':
				if cur.Len() > 0 && hasClosingAngle(s[i+1:]) {
					cur.WriteByte(c)
				} else {
					flush()
				}
			case c == '>':
				if strings.ContainsRune(cur.String(), '<') {
					cur.WriteByte(c)
				} else {
					flush()
				}
			default:
				cur.WriteByte(c)
			}
		}
	}
	flush()
	return out
}

func hasClosingAngle(rest string) bool {
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '>':
			return i > 0
		case ' ', '\t', '\r', '\n', ';', '|', '&', '(', ')', '<', '\'', '"', '`':
			return false
		}
	}
	return false
}

func LoadSkills(dir string) (map[string]Skill, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Skill{}, nil
		}
		return nil, err
	}
	filePaths := map[string]string{}
	dirPaths := map[string]string{}
	nameSet := map[string]bool{}
	for _, e := range ents {
		if !e.IsDir() {
			if strings.HasSuffix(e.Name(), ".md") && e.Name() != readmeFile {
				name := strings.TrimSuffix(e.Name(), ".md")
				filePaths[name] = filepath.Join(dir, e.Name())
				nameSet[name] = true
			}
			continue
		}
		if e.Name() == testdataDir {
			continue
		}
		p := filepath.Join(dir, e.Name(), SkillFile)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			dirPaths[e.Name()] = p
			nameSet[e.Name()] = true
		}
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)
	out := map[string]Skill{}
	var errs []error
	loadOne := func(name, p, skillDir string) {
		b, err := os.ReadFile(p)
		if err != nil {
			errs = append(errs, err)
			return
		}
		s, err := ParseSkill(name, string(b))
		if err != nil {
			errs = append(errs, err)
			return
		}
		s.Path = p
		s.Dir = skillDir
		out[name] = s
	}
	base := filepath.Base(dir)
	for _, name := range names {
		fp, hasFile := filePaths[name]
		dp, hasDir := dirPaths[name]
		if hasFile && hasDir {
			errs = append(errs, fmt.Errorf("skill %s: duplicate skill defined at both %s and %s",
				name, path.Join(base, name+".md"), path.Join(base, name, SkillFile)))
			loadOne(name, fp, "")
			continue
		}
		if hasFile {
			loadOne(name, fp, "")
		} else {
			loadOne(name, dp, filepath.Join(dir, name))
		}
	}
	return out, errors.Join(errs...)
}
