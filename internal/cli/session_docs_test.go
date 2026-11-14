package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// sto_2a41b9e4 AC4: every satelle command a document tells a session to run
// carries --session, reads included (operator direction, 2026-11-14: one rule
// for agents). The rule is mechanical: scanDoc finds each shown command, and
// the named exceptions below are the only ones.

// repoRoot is this repo's root, from internal/cli.
const repoRoot = "../.."

// sessionDocs is every document a session reads, as paths relative to root:
// the help topics, README.md, every .md under .satelle (but its run records
// and test fixtures), under .satelle-market and under the init template.
func sessionDocs(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	add := func(rel string) {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			out = append(out, rel)
		}
	}
	walk := func(dir string) {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && (d.Name() == "runs" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
				rel, _ := filepath.Rel(root, p)
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	walk("internal/help/topics")
	add("README.md")
	walk(".satelle")
	walk(".satelle-market")
	walk("internal/kernel/template")
	slices.Sort(out)
	return out
}

// shownCmd is one satelle command a document shows.
type shownCmd struct {
	Line int    // 1-based line where it starts
	Text string // from "satelle" to the end of the command
	Code bool   // in a code block (else an inline span)
	// CmdPos: in a code block, at shell command position (a check's own call).
	CmdPos bool
}

// HasSession reports whether the command carries --session.
func (c shownCmd) HasSession() bool {
	// The flag counts outside <...> and [...] and with a value that is not an
	// alternative (--session x|pi is a malformed edit, not the flag).
	depth := 0
	fields := strings.Fields(c.Text)
	for i, f := range fields {
		if depth == 0 && (f == "--session" && i+1 < len(fields) && !strings.Contains(fields[i+1], "|") ||
			strings.HasPrefix(f, "--session=") && !strings.Contains(f, "|")) {
			return true
		}
		depth += strings.Count(f, "<") + strings.Count(f, "[") - strings.Count(f, ">") - strings.Count(f, "]")
		if depth < 0 {
			depth = 0
		}
	}
	return false
}

var satelleWord = regexp.MustCompile(`(^|[^A-Za-z0-9_./-])satelle(\s|$)`)

// binaryWord matches the satelle binary in text: the word satelle, or a
// shell variable the text sets to it (sbin=$(command -v satelle),
// BIN="./satelle"), as "$sbin", $sbin or "${BIN}".
func binaryWord(text string) *regexp.Regexp {
	var names []string
	for _, m := range regexp.MustCompile(`(?m)(\w+)=(?:\$\(command -v satelle\b|"?[^"\s]*/satelle"?$)`).FindAllStringSubmatch(text, -1) {
		names = append(names, regexp.QuoteMeta(m[1]))
	}
	if len(names) == 0 {
		return satelleWord
	}
	alt := strings.Join(names, "|")
	return regexp.MustCompile(`(^|[^A-Za-z0-9_./-])(?:satelle|"\$\{?(?:` + alt + `)\}?"|\$\{?(?:` + alt + `)\}?)(\s|$)`)
}

// scanDoc returns the satelle commands text shows: each occurrence of the
// word satelle followed by a root subcommand, on a code-block line (fenced or
// indented; a line ending in a backslash joins the next) or in an inline
// backtick span (a prose paragraph's lines read as one), except an inline
// span that only names a command (nothing but flags or an elision after a
// command that cannot run with no operand).
func scanDoc(root *cobra.Command, text string) []shownCmd {
	root.InitDefaultHelpCmd()
	word := binaryWord(text)
	lines := strings.Split(text, "\n")
	var out []shownCmd
	inFence := false
	var para []int // line indexes of the current prose paragraph
	flush := func() {
		if len(para) == 0 {
			return
		}
		var b strings.Builder
		var starts []int // byte offset of each line in b
		for _, i := range para {
			starts = append(starts, b.Len())
			b.WriteString(lines[i])
			b.WriteString(" ")
		}
		joined := b.String()
		for _, sp := range inlineSpans(joined) {
			line := para[0]
			for k, s := range starts {
				if sp.start >= s {
					line = para[k]
				}
			}
			for _, c := range commandsIn(root, word, sp.text, false) {
				c.Line = line + 1
				out = append(out, c)
			}
		}
		para = nil
	}
	prevBlank, inIndent := true, false
	isIndent := func(l string) bool { return strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "\t") }
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			flush()
			inFence, inIndent = !inFence, false
			prevBlank = false
			continue
		}
		// An indented code block opens after a blank line with a line that
		// starts with satelle (or $ satelle), and runs while lines stay
		// indented (blank lines inside it included).
		switch {
		case inFence:
		case inIndent && trim != "" && !isIndent(l):
			inIndent = false
		case !inIndent && prevBlank && isIndent(l) && strings.HasPrefix(strings.TrimLeft(trim, "$ "), "satelle "):
			inIndent = true
		}
		if (inFence || inIndent) && trim != "" {
			flush()
			start := i
			full := l
			for strings.HasSuffix(strings.TrimRight(full, " "), "\\") && i+1 < len(lines) {
				i++
				full = strings.TrimSuffix(strings.TrimRight(full, " "), "\\") + " " + strings.TrimSpace(lines[i])
			}
			for _, c := range commandsIn(root, word, full, true) {
				c.Line = start + 1
				out = append(out, c)
			}
			prevBlank = false
			continue
		}
		if trim == "" {
			flush()
			prevBlank = true
			continue
		}
		para = append(para, i)
		prevBlank = false
	}
	flush()
	return out
}

type span struct {
	start int
	text  string
}

// inlineSpans are the single-backtick code spans of a prose paragraph.
func inlineSpans(s string) []span {
	var out []span
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
		out = append(out, span{start: i, text: s[i+n : i+n+end]})
		i += n + end + n - 1
	}
	return out
}

// commandsIn finds the commands in one code line or one inline span.
func commandsIn(root *cobra.Command, word *regexp.Regexp, s string, code bool) []shownCmd {
	var out []shownCmd
	for _, m := range word.FindAllStringSubmatchIndex(s, -1) {
		at := m[0] + len(s[m[2]:m[3]])
		if inPlaceholder(s[:at]) {
			// <... of satelle service status>, <satelle runtime path>: a
			// placeholder's words, not a command.
			continue
		}
		ext := extent(s, at, code)
		fields := strings.Fields(ext)
		if len(fields) == 0 {
			continue
		}
		words := fields[1:]
		path, after, ok := resolvePath(root, words)
		if !ok {
			continue
		}
		if !code && isName(path, after) {
			continue
		}
		out = append(out, shownCmd{Text: ext, Code: code, CmdPos: code && atCommandPosition(s[:at])})
	}
	return out
}

// extent is the command starting at s[at]: up to the next unquoted |, ;, &,
// ), backtick, redirection or # comment, or the closing quote of a quote it
// sits in. A $( opens a fresh quoting context, so "$(satelle ... "$id")" is
// read as the command inside the substitution.
func extent(s string, at int, code bool) string {
	var q byte
	nested := false // a 'quoted' part inside a double-quoted string
	for i := 0; i < at; i++ {
		switch c := s[i]; {
		case q != '\'' && c == '$' && i+1 < len(s) && s[i+1] == '(':
			q, nested = 0, false
			i++
		case q == '"' && c == '\'':
			nested = !nested
		case q == 0 && (c == '"' || c == '\''):
			q = c
		case q != 0 && c == q:
			q, nested = 0, false
		}
	}
	if nested {
		// echo "... 'satelle status' ...": the command ends at the inner '.
		if end := strings.IndexByte(s[at:], '\''); end >= 0 {
			return strings.TrimSpace(s[at : at+end])
		}
	}
	inner := q
	q = 0
	depth := 0 // inside <placeholder> or [optional] syntax, where | means "or"
	for i := at; i < len(s); i++ {
		c := s[i]
		if inner != 0 && q == 0 && c == inner {
			return strings.TrimSpace(s[at:i])
		}
		switch {
		case q == 0 && c == '[', q == 0 && c == '<' && i+1 < len(s) && s[i+1] != ' ' && s[i+1] != '<' && s[i+1] != '(':
			depth++
		case q == 0 && depth > 0 && (c == ']' || c == '>'):
			depth--
		case q == 0 && c == '|' && (depth > 0 || !code):
			// An alternative (claude|pi, <a|b>, [F|-]), not a pipe.
		case q == 0 && (c == '"' || c == '\'') && c != inner:
			q = c
		case q != 0 && c == q:
			q = 0
		case q == 0 && (c == '|' || c == ';' || c == '&' || c == ')' || c == '`'):
			return strings.TrimSpace(s[at:i])
		case q == 0 && (c == '#' || c == '(') && i > 0 && s[i-1] == ' ':
			// A # comment, or a parenthesis that opens an explanation.
			return strings.TrimSpace(s[at:i])
		case q == 0 && (c == '>' || c == '<') && i > 0 && (s[i-1] == ' ' || (s[i-1] >= '0' && s[i-1] <= '9' && i > 1 && s[i-2] == ' ')):
			// A redirection (>, 2>, <, but not a <placeholder>): the command
			// ends before it; a fd digit stays with the redirection.
			if c == '<' && i+1 < len(s) && s[i+1] != '<' && s[i+1] != ' ' && s[i+1] != '(' {
				continue
			}
			end := i
			if s[i-1] >= '0' && s[i-1] <= '9' {
				end = i - 1
			}
			return strings.TrimSpace(s[at:end])
		}
	}
	return strings.TrimSpace(s[at:])
}

// resolvePath resolves a command's words against the tree: root flags are
// skipped, <kind> and $kind read as story. It reports the command path, the
// words after it, and false when the first word is not a root subcommand.
func resolvePath(root *cobra.Command, words []string) (*cobra.Command, []string, bool) {
	i := 0
	for i < len(words) {
		w := words[i]
		switch {
		case w == "-C" || w == "--session" || w == "--directory":
			i += 2
			continue
		case strings.HasPrefix(w, "-C") || strings.HasPrefix(w, "--session=") || strings.HasPrefix(w, "--directory="):
			i++
			continue
		}
		break
	}
	if i >= len(words) {
		return nil, nil, false
	}
	cur := root
	first := true
	for ; i < len(words); i++ {
		w := strings.Trim(words[i], `"'`)
		if w == "<kind>" || w == "$kind" || w == "${kind}" {
			w = "story"
		}
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == w || slices.Contains(c.Aliases, w) {
				next = c
				break
			}
		}
		if next == nil {
			if first {
				return nil, nil, false
			}
			return cur, words[i:], true
		}
		cur, first = next, false
	}
	return cur, nil, true
}

// isName reports whether an inline span only names cmd: nothing but flags or
// an elision follows it, and cmd cannot run with no operand.
func isName(cmd *cobra.Command, after []string) bool {
	for _, w := range after {
		if !strings.HasPrefix(w, "-") && w != "…" && w != "..." {
			return false
		}
	}
	if !cmd.Runnable() || len(cmd.Commands()) > 0 && cmd.Args == nil {
		return true
	}
	if cmd.Args != nil && cmd.Args(cmd, nil) != nil {
		return true
	}
	return false
}

// atCommandPosition reports whether a call starting after prefix is at shell
// command position: only if, !, then, do or else between it and the nearest
// $(, |, &, ;, { or the line start.
func atCommandPosition(prefix string) bool {
	cut := -1
	for _, sep := range []string{"$(", "|", "&", ";", "{"} {
		if i := strings.LastIndex(prefix, sep); i+len(sep) > cut && i >= 0 {
			cut = i + len(sep)
		}
	}
	if cut < 0 {
		cut = 0
	}
	for _, w := range strings.Fields(prefix[cut:]) {
		if !slices.Contains([]string{"if", "!", "then", "do", "else", "elif", "while", "until"}, w) {
			return false
		}
	}
	return true
}

// sessionException is a shown command that carries no --session, with why.
type sessionException struct {
	File, Text, Why string
}

// sessionExceptions is the one list (decision 10); a check skill's own calls
// (E1) are excepted by isCheckSkill instead.
var sessionExceptions = []sessionException{
	// E2: the kernel refuses record from a named session.
	{"internal/help/topics/workflows.md", "satelle story record <id> --file F", "E2: the operator's verb; a named session is refused"},
	{".satelle/remotes/satelle-cicd/tasks/tsk_release.md", "satelle execution record", "E2: the operator's verb; a named session is refused"},
	// E3: the harness runs the hook verbs and sends the session in its payload.
	{"*", "satelle hook gate", "E3: the harness's call; the session arrives in its payload"},
	{"*", "satelle hook context", "E3: the harness's call; the session arrives in its payload"},
	{"*", "satelle hook prompt", "E3: the harness's call; the session arrives in its payload"},
	{"*", "satelle hook stopcheck", "E3: the harness's call; the session arrives in its payload"},
	// E4: text that reports what a command printed, not a command.
	{".satelle/skills/satelle-release-installed-check.md", "satelle version is not satelle", "E4: a message about the output"},
	{".satelle/skills/satelle-release-installed-check.md", "satelle service status lacks", "E4: a message about the output"},
	{".satelle/skills/satelle-release-installed-check.md", "satelle service status warns", "E4: a message about the output"},
	{".satelle/skills/satelle-pr-review-publish.md", "satelle project show named no project", "E4: a message about the output"},
}

func excepted(file string, c shownCmd) bool {
	for _, e := range sessionExceptions {
		if (e.File == "*" || e.File == file) && strings.HasPrefix(c.Text, e.Text) {
			return true
		}
	}
	return false
}

// isCheckSkill reports whether a document is a skill whose frontmatter says
// type: check: its own calls (E1) run inside the gate's process and inherit
// the session.
func isCheckSkill(text string) bool {
	if !strings.HasPrefix(text, "---\n") {
		return false
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return false
	}
	return regexp.MustCompile(`(?m)^type:\s*check\s*$`).MatchString(text[4 : 4+end])
}

// missingSession is every shown command in the session docs under root that
// lacks --session and is not excepted, as file:line: text.
func missingSession(t *testing.T, root string) []string {
	t.Helper()
	tree := NewRootCmd()
	var out []string
	for _, rel := range sessionDocs(t, root) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		check := isCheckSkill(string(b))
		for _, c := range scanDoc(tree, string(b)) {
			if c.HasSession() || excepted(rel, c) || (check && c.CmdPos) {
				continue
			}
			out = append(out, rel+":"+strconv.Itoa(c.Line)+": "+c.Text)
		}
	}
	return out
}

func TestEveryShownCommandNamesTheSession(t *testing.T) {
	missing := missingSession(t, repoRoot)
	for _, m := range missing {
		t.Errorf("no --session: %s", m)
	}
	// No stale exception: each still matches a shown command.
	tree := NewRootCmd()
	docs := sessionDocs(t, repoRoot)
	for _, e := range sessionExceptions {
		found := false
		for _, rel := range docs {
			if e.File != "*" && e.File != rel {
				continue
			}
			b, _ := os.ReadFile(filepath.Join(repoRoot, rel))
			for _, c := range scanDoc(tree, string(b)) {
				if strings.HasPrefix(c.Text, e.Text) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("stale exception %q in %s (%s)", e.Text, e.File, e.Why)
		}
	}
}

// The scanned roots are whole directories: a new Markdown file under .satelle
// is scanned with no list to update; run records and fixtures are not.
func TestSessionDocsCoverTheSubstrate(t *testing.T) {
	tmp := t.TempDir()
	for _, rel := range []string{".satelle/README.md", ".satelle/tasks/README.md", ".satelle/queues/q.md", ".satelle/remotes/n/tasks/t.md",
		".satelle/skills/s.md", ".satelle/runs/r/attachments/note.md", ".satelle/skills/testdata/f.md"} {
		p := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := sessionDocs(t, tmp)
	want := []string{".satelle/README.md", ".satelle/queues/q.md", ".satelle/remotes/n/tasks/t.md", ".satelle/skills/s.md", ".satelle/tasks/README.md"}
	if !slices.Equal(got, want) {
		t.Errorf("sessionDocs = %v, want %v", got, want)
	}
	repo := sessionDocs(t, repoRoot)
	for _, must := range []string{".satelle/README.md", ".satelle/tasks/README.md", ".satelle/constitution.md",
		".satelle/remotes/satelle-cicd/tasks/tsk_release.md", ".satelle-market/manifest.md", "internal/kernel/template/README.md",
		"internal/help/topics/edit-gate.md", "README.md"} {
		if !slices.Contains(repo, must) {
			t.Errorf("the repo's session docs lack %s", must)
		}
	}
}

// The scanner's rule over small texts, so it cannot pass by finding nothing.
func TestTheScannerFindsCommands(t *testing.T) {
	tree := NewRootCmd()
	found := func(text string) []shownCmd { return scanDoc(tree, text) }
	cases := []struct {
		name, text string
		n          int  // commands found
		ok         bool // every found command carries --session
	}{
		{"span with a literal operand", "Run `satelle queue show release` now.", 1, false},
		{"span of a no-operand command", "Use `satelle status` or `satelle validate` or `satelle help`.", 3, false},
		{"wrapped span", "Run `satelle help\nworkflows` for more.", 1, false},
		{"indented block", "Text:\n\n    satelle story list\n    satelle story get <id>\n", 2, false},
		{"fenced line", "```sh\nsatelle story list\n```\n", 1, false},
		{"trailing comment", "```sh\nsatelle story list   # the items\n```\n", 1, false},
		{"flag before the comment", "```sh\nsatelle story list --session <session>   # the items\n```\n", 1, true},
		{"flag only in the comment", "```sh\nsatelle story list   # --session\n```\n", 1, false},
		{"printed by echo", "```sh\necho \"run satelle story get <id> first\"\n```\n", 1, false},
		{"satelle to satelle pipe", "```sh\nsatelle task get t --session \"$s\" | satelle execution attach e --file -\n```\n", 2, false},
		{"continued with a backslash", "```sh\nsatelle story create --title t \\\n  --session <session>\n```\n", 1, true},
		{"no group is skipped", "Use `satelle upgrade --from <store> --map <map>` and `satelle story record <id> --file F`.", 2, false},
		{"names", "The verbs `satelle story set`, `satelle story set --status`, `satelle story` and `satelle hook ...`.", 0, true},
		{"not a command of the tree", "Then `satelle pr approve` runs.", 0, true},
		{"-C form", "`satelle -C <dir> story set <id> --session <session>`", 1, true},
		{"flag only after a pipe", "```sh\nsatelle story list | grep --session x\n```\n", 1, false},
		{"redirection ends it", "```sh\nsatelle story list 2>/dev/null --session x\n```\n", 1, false},
	}
	for _, c := range cases {
		got := found(c.text)
		if len(got) != c.n {
			t.Errorf("%s: found %d commands %+v, want %d", c.name, len(got), got, c.n)
			continue
		}
		all := true
		for _, g := range got {
			all = all && g.HasSession()
		}
		if c.n > 0 && all != c.ok {
			t.Errorf("%s: carries --session = %v, want %v (%+v)", c.name, all, c.ok, got)
		}
	}
	if got := found("Run `satelle help\nworkflows` for more."); len(got) == 1 && got[0].Line != 1 {
		t.Errorf("a wrapped span is reported at line %d, want 1", got[0].Line)
	}
	// E1: a check skill's own call is at command position; a printed one is not.
	check := "```sh\nif ! satelle story get x; then echo \"run satelle story get x\"; fi\nv=$(satelle ledger list x --json)\n```\n"
	var pos []bool
	for _, c := range found(check) {
		pos = append(pos, c.CmdPos)
	}
	if !slices.Equal(pos, []bool{true, false, true}) {
		t.Errorf("command positions %v, want [true false true]", pos)
	}
}

// Every type: check skill is E1 because its frontmatter says so, and a block
// that names "$session" defines it.
func TestScriptBlocksDefineTheSession(t *testing.T) {
	for _, rel := range sessionDocs(t, repoRoot) {
		b, _ := os.ReadFile(filepath.Join(repoRoot, rel))
		inFence, defined, uses := false, false, 0
		for i, l := range strings.Split(string(b), "\n") {
			trim := strings.TrimSpace(l)
			if strings.HasPrefix(trim, "```") {
				if inFence && uses > 0 && !defined {
					t.Errorf("%s:%d: a block uses --session \"$session\" with no session= line", rel, i+1)
				}
				inFence, defined, uses = !inFence, false, 0
				continue
			}
			if inFence && strings.HasPrefix(trim, "session=") {
				defined = true
			}
			if inFence && strings.Contains(l, `--session "$session"`) {
				uses++
			}
		}
	}
}

// No document shows the env-var prefix, an export, or an allow rule with a *
// inside an env-var value (sto_2a41b9e4 criterion 4).
func TestDocsShowOnlyTheSessionFlag(t *testing.T) {
	bad := []*regexp.Regexp{
		regexp.MustCompile(`SATELLE_SESSION=\S+\s+satelle\b`),
		regexp.MustCompile(`export\s+SATELLE_SESSION`),
		regexp.MustCompile(`Bash\([A-Z_]+=[^)\s]*\*`),
	}
	for _, rel := range sessionDocs(t, repoRoot) {
		b, _ := os.ReadFile(filepath.Join(repoRoot, rel))
		for i, l := range strings.Split(string(b), "\n") {
			for _, re := range bad {
				if re.MatchString(l) {
					t.Errorf("%s:%d: %s", rel, i+1, strings.TrimSpace(l))
				}
			}
		}
	}
}

// Printed commands nested in quotes end where their own quote or explanation
// does, so a flag after it is not counted.
func TestTheScannerReadsNestedQuotes(t *testing.T) {
	tree := NewRootCmd()
	for _, c := range []struct {
		text string
		ok   bool
	}{
		{"```sh\necho \"deploy: 'satelle status' failed\" --session x\n```\n", false},
		{"```sh\necho \"deploy: 'satelle status --session x' failed\"\n```\n", true},
		{"```sh\necho \"run satelle story attach X --file - (### Added) --session x\"\n```\n", false},
		{"```sh\necho \"run satelle story attach X --file - --session x (### Added)\"\n```\n", true},
		{"```sh\nprintf 'a && satelle service restart\\n --session x' \"$a\"\n```\n", true},
	} {
		got := scanDoc(tree, c.text)
		if len(got) != 1 || got[0].HasSession() != c.ok {
			t.Errorf("%q: %+v, want one command with --session %v", c.text, got, c.ok)
		}
	}
}

// A variable the document sets to the satelle binary is read as satelle,
// and | is an alternative in a span or inside <...> and [...] (code-ac round
// 1 of sto_2a41b9e4).
func TestTheScannerReadsAliasesAndAlternatives(t *testing.T) {
	tree := NewRootCmd()
	for _, c := range []struct {
		text string
		n    int
		ok   bool
	}{
		{"```sh\nsbin=$(command -v satelle)\n\"$sbin\" update --version 1\n```\n", 1, false},
		{"```sh\nsbin=$(command -v satelle)\n\"$sbin\" update --version 1 --session x\n```\n", 1, true},
		{"```sh\nBIN=\"${HOME}/.local/bin/satelle\"\n\"${BIN}\" status >/dev/null\n```\n", 1, false},
		{"Run `satelle agents install claude --session x|pi|grok`.", 1, false},
		{"Run `satelle agents install claude|pi|grok --session x`.", 1, true},
		{"```sh\nsatelle story create --category <a --session x|b>\n```\n", 1, false},
		{"Run `satelle github review --body-file F --session x|-`.", 1, false},
	} {
		got := scanDoc(tree, c.text)
		if len(got) != c.n || (c.n > 0 && got[0].HasSession() != c.ok) {
			t.Errorf("%q: %+v, want %d command(s), --session %v", c.text, got, c.n, c.ok)
		}
	}
}

// inPlaceholder reports whether the text before an occurrence leaves a
// <placeholder> open: a < followed by a word, not yet closed by >.
func inPlaceholder(prefix string) bool {
	open := -1
	for i := 0; i < len(prefix); i++ {
		switch {
		case prefix[i] == '<' && (i+1 == len(prefix) || prefix[i+1] != ' ' && prefix[i+1] != '<' && prefix[i+1] != '(' && prefix[i+1] != '='):
			open = i
		case prefix[i] == '>' && open >= 0:
			open = -1
		}
	}
	return open >= 0
}

// An occurrence inside a <placeholder> is the placeholder's words, so a flag
// appended there is not a command's (code-ac round 2 of sto_2a41b9e4).
func TestTheScannerSkipsPlaceholders(t *testing.T) {
	tree := NewRootCmd()
	for _, text := range []string{
		"```sh\ndbin=<the path in the installed line of satelle service status>\n```\n",
		"List `<satelle runtime path>/archive/` first.",
	} {
		if got := scanDoc(tree, text); len(got) != 0 {
			t.Errorf("%q: found %+v, want none", text, got)
		}
	}
	if got := scanDoc(tree, "Run `satelle story get <id>` now."); len(got) != 1 {
		t.Errorf("a placeholder after the command hid it: %+v", got)
	}
}
