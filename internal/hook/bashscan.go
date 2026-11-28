package hook

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

type BashTargets struct {
	Paths          []string
	Inline         []string
	Raw            []string
	InlineRaw      []string
	OperatorRecord bool
	Unreadable     string
	Pushes         []PushDest
}

func ScanBash(command, cwd, root string, isFile func(string) bool) BashTargets {
	var t BashTargets
	seen := map[*[]string]map[string]bool{&t.Paths: {}, &t.Inline: {}, &t.Raw: {}, &t.InlineRaw: {}}
	put := func(dst *[]string, p string) {
		if seen[dst][p] {
			return
		}
		seen[dst][p] = true
		*dst = append(*dst, p)
	}
	add := func(dst, raw *[]string, p string) {
		c := path.Clean(p)
		if c == "" || c == "." {
			return
		}
		put(dst, c)
		put(raw, p)
	}
	base := path.Clean(filepath.ToSlash(cwd))
	for _, logical := range []bool{true, false} {
		scanBash(command, base, root, isFile, logical, 0, func(p string) { add(&t.Paths, &t.Raw, p) }, func(p string) { add(&t.Inline, &t.InlineRaw, p) })
	}
	t.OperatorRecord = operatorRecordCommand(command, 0)
	segs, why := readPlainLine(command)
	if why == "" {
		var dirs []string
		dirs, why = plainGitDirs(segs, base)
		for _, d := range dirs {
			add(&t.Paths, &t.Raw, d)
		}
	}
	if why != "" && namesGit(command) {
		t.Unreadable = why
	}
	if why == "" {
		t.Pushes = readPushes(segs)
	}
	return t
}

func scanBash(command, cwd, root string, isFile func(string) bool, logical bool, depth int, path, inline func(string)) {
	var segs []bashSegment
	forEachSegment(command, cwd, logical, func(s bashSegment) { segs = append(segs, s) })
	for j, sg := range segs {
		for _, r := range redirectTargets(sg.Tokens, sg.Cwd) {
			path(r)
		}
		words := sg.Words
		if len(words) == 0 {
			continue
		}
		if depth < maxShellProgramDepth {
			if prog, ok := shellProgram(sg); ok {
				scanBash(prog, sg.Cwd, root, isFile, logical, depth+1, path, inline)
				continue
			}
		}
		cmd := filepath.Base(words[0])
		if cmd == "cd" {
			continue
		}
		if flags, ok := dirFlagTable[cmd]; ok {
			args := commandArgWords(commandTokens(sg.Tokens, sg.Idx))
			if cmd != "git" || !gitReadOnlyInvocation(args) {
				for i := 1; i < len(args); i++ {
					for _, f := range flags {
						if args[i] == f && i+1 < len(args) {
							path(JoinRaw(sg.Cwd, args[i+1]))
							i++
							break
						}
					}
				}
			}
		}
		if mutationVerbs[cmd] {
			var paths []string
			if cmd == "sed" {
				paths = sedPathArgs(commandTokens(sg.Tokens, sg.Idx), sg.Cwd)
			} else {
				paths = mutationPathArgs(commandArgWords(commandTokens(sg.Tokens, sg.Idx)), sg.Cwd)
			}
			for _, p := range paths {
				path(p)
			}
		}
		if !interpreterInlineProgram(sg, isFile) {
			continue
		}
		inline(sg.Cwd)
		if root == "" {
			continue
		}
		for _, p := range programSegments(segs, j) {
			for _, t := range p.Tokens {
				text := t.Value
				if t.Raw != "" {
					text = t.Raw
				}
				for _, run := range pathRefs(text, []string{root}) {
					if abs := JoinRaw(p.Cwd, run); abs != "" {
						path(abs)
					}
				}
			}
		}
	}
}

type bashTok struct {
	Kind       string
	Value      string
	Op         string
	Raw        string
	Start, End int
}

type plainWord struct {
	Value    string
	Unquoted string
}

type plainSegment struct {
	Words []plainWord
}

func readPlainLine(command string) ([]plainSegment, string) {
	var segs []plainSegment
	var cur []plainWord
	var val, unq strings.Builder
	inWord, quoted := false, false
	pending := ""
	endWord := func() {
		if inWord {
			cur = append(cur, plainWord{Value: val.String(), Unquoted: unq.String()})
		}
		val.Reset()
		unq.Reset()
		inWord, quoted = false, false
	}
	endSeg := func(at int) string {
		endWord()
		if len(cur) == 0 {
			return fmt.Sprintf("an empty command at byte %d", at)
		}
		if why := plainCommand(cur); why != "" {
			return why
		}
		segs = append(segs, plainSegment{Words: cur})
		cur = nil
		return ""
	}
	for i := 0; i < len(command); {
		c := command[i]
		var next byte
		if i+1 < len(command) {
			next = command[i+1]
		}
		switch {
		case c == ' ' || c == '\t':
			endWord()
			i++
		case (c == '&' && next == '&') || (c == '|' && next == '|') || c == ';' || (c == '|' && next != '&'):
			op := command[i : i+1]
			if c != ';' && next == c {
				op = command[i : i+2]
			}
			if why := endSeg(i); why != "" {
				return nil, why
			}
			pending = op
			i += len(op)
		case c == '\'':
			end := strings.IndexByte(command[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Sprintf("an unterminated single quote at byte %d", i)
			}
			val.WriteString(command[i+1 : i+1+end])
			inWord, quoted, pending = true, true, ""
			i += end + 2
		case c == '"':
			j := i + 1
			for ; j < len(command) && command[j] != '"'; j++ {
				switch d := command[j]; d {
				case '$', '`', '!':
					return nil, fmt.Sprintf("%s inside double quotes at byte %d", describeShellByte(d), j)
				case '\\':
					if j+1 < len(command) && strings.IndexByte("$`\"\\\n", command[j+1]) >= 0 {
						return nil, fmt.Sprintf("a backslash escape inside double quotes at byte %d", j)
					}
				}
				val.WriteByte(command[j])
			}
			if j == len(command) {
				return nil, fmt.Sprintf("an unterminated double quote at byte %d", i)
			}
			inWord, quoted, pending = true, true, ""
			i = j + 1
		case plainWordByte(c) || (c == '~' && inWord && command[i-1] != '=' && command[i-1] != ':'):
			val.WriteByte(c)
			if !quoted {
				unq.WriteByte(c)
			}
			inWord, pending = true, ""
			i++
		case c == '\\' && next == '\n':
			return nil, fmt.Sprintf("a line continuation at byte %d", i)
		case c == '|':
			return nil, fmt.Sprintf("the token |& at byte %d", i)
		default:
			return nil, fmt.Sprintf("%s at byte %d", describeShellByte(c), i)
		}
	}
	endWord()
	if len(cur) == 0 {
		if pending != "" && pending != ";" {
			return nil, fmt.Sprintf("a trailing %s", pending)
		}
		return segs, ""
	}
	if why := endSeg(len(command)); why != "" {
		return nil, why
	}
	return segs, ""
}

func plainWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		strings.IndexByte("-_./,:+@^=%", c) >= 0
}

func describeShellByte(c byte) string {
	switch {
	case c == '\n':
		return "a newline"
	case c == '\r':
		return "a carriage return"
	case c == '&':
		return "a lone &"
	case c == '<' || c == '>':
		return "a redirection " + string(c)
	case c >= 0x80 || c < 0x20:
		return fmt.Sprintf("the byte 0x%02x", c)
	}
	return "the token " + string(c)
}

func plainCommand(words []plainWord) string {
	w := words[0]
	switch {
	case isAssignmentPrefix(w.Unquoted):
		return "an assignment to " + w.Unquoted[:strings.IndexAny(w.Unquoted, "+=")]
	case w.Value == "cd":
		if len(words) != 2 || words[1].Value == "" || strings.HasPrefix(words[1].Value, "-") {
			return "a cd whose target is not one plain literal path"
		}
	case bashBuiltinWords[w.Value]:
		return "the builtin or keyword " + w.Value
	}
	return ""
}

const maxPlainDirs = 64

func plainGitDirs(segs []plainSegment, base string) ([]string, string) {
	cands := []string{base}
	var dirs []string
	for _, s := range segs {
		values := make([]string, len(s.Words))
		for i, w := range s.Words {
			values[i] = w.Value
		}
		if values[0] == "cd" {
			for _, c := range slices.Clone(cands) {
				raw := JoinRaw(c, values[1])
				if raw == "" {
					continue
				}
				for _, d := range []string{path.Clean(raw), raw} {
					if !slices.Contains(cands, d) {
						cands = append(cands, d)
					}
				}
			}
			if len(cands) > maxPlainDirs {
				return nil, fmt.Sprintf("more than %d directories", maxPlainDirs)
			}
			continue
		}
		_, gitDirs, ok := gitInvocation(values)
		if !ok || gitReadOnlyInvocation(values) {
			continue
		}
		for _, c := range cands {
			d, moved := c, false
			for _, g := range gitDirs {
				if g != "" {
					d, moved = JoinRaw(d, g), true
				}
			}
			if moved || c != base {
				dirs = append(dirs, d)
			}
		}
	}
	return dirs, ""
}

func namesGit(command string) bool {
	text := strings.NewReplacer("\\\r\n", "", "\\\n", "").Replace(command)
	text = strings.NewReplacer("\\", "", "'", "", "\"", "").Replace(text)
	edge := func(c byte) bool {
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
	}
	for off := 0; ; {
		i := strings.Index(text[off:], "git")
		if i < 0 {
			return false
		}
		i += off
		if (i == 0 || !edge(text[i-1])) && (i+3 == len(text) || !edge(text[i+3])) {
			return true
		}
		off = i + 1
	}
}

func tokenizeBash(command string) []bashTok {
	var out []bashTok
	emit := func(t bashTok, start, end int) {
		t.Start, t.End = start, end
		out = append(out, t)
	}
	i := 0
	n := len(command)
	for i < n {
		if command[i] == ' ' || command[i] == '\t' || command[i] == '\r' {
			i++
			continue
		}
		if k := lineContinuation(command, i); k > 0 {
			i += k
			continue
		}
		if command[i] == '\n' {
			emit(bashTok{Kind: "op", Op: "\n"}, i, i+1)
			i++
			continue
		}
		if command[i] == '&' && i+1 < n && command[i+1] == '&' {
			emit(bashTok{Kind: "op", Op: "&&"}, i, i+2)
			i += 2
			continue
		}
		if command[i] == '|' && i+1 < n && command[i+1] == '|' {
			emit(bashTok{Kind: "op", Op: "||"}, i, i+2)
			i += 2
			continue
		}
		if command[i] == ';' {
			emit(bashTok{Kind: "op", Op: ";"}, i, i+1)
			i++
			continue
		}
		if command[i] == '|' {
			emit(bashTok{Kind: "op", Op: "|"}, i, i+1)
			i++
			continue
		}
		if command[i] == '<' && i+1 < n && command[i+1] == '<' {
			end, restStart, rest, ok := consumeHeredoc(command, i)
			if ok {
				emit(bashTok{Kind: "string", Value: "HEREDOC", Raw: command[i:end]}, i, restStart)
				for _, t := range tokenizeBash(rest) {
					emit(t, t.Start+restStart, t.End+restStart)
				}
				emit(bashTok{Kind: "op", Op: "\n"}, end, end)
				i = end
				continue
			}
		}
		if command[i] == '\'' {
			j := i + 1
			for j < n && command[j] != '\'' {
				j++
			}
			if j < n {
				j++
			}
			emit(bashTok{Kind: "string", Value: command[i:j]}, i, j)
			i = j
			continue
		}
		if command[i] == '"' {
			j := skipDoubleQuoted(command, i+1)
			emit(bashTok{Kind: "string", Value: command[i:j]}, i, j)
			i = j
			continue
		}
		if command[i] == '$' && i+1 < n && command[i+1] == '\'' {
			j := i + 2
			for j < n {
				if command[j] == '\\' && j+1 < n {
					j += 2
					continue
				}
				if command[j] == '\'' {
					j++
					break
				}
				j++
			}
			emit(bashTok{Kind: "string", Value: command[i:j]}, i, j)
			i = j
			continue
		}
		j := i
		for j < n {
			c := command[j]
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' ||
				c == ';' || c == '|' || c == '&' ||
				c == '\'' || c == '"' {
				break
			}
			if c == '\\' {
				j = min(j+2, n)
				continue
			}
			if c == '$' && j > i && j+1 < n && (command[j+1] == '\'' || command[j+1] == '"') {
				break
			}
			if c == '&' || (c == '|' && j+1 < n && command[j+1] == '|') {
				break
			}
			if c == '<' && j+1 < n && command[j+1] == '<' {
				break
			}
			j++
		}
		if j > i {
			emit(bashTok{Kind: "word", Value: command[i:j]}, i, j)
			i = j
			continue
		}
		emit(bashTok{Kind: "word", Value: string(command[i])}, i, i+1)
		i++
	}
	return out
}

func lineContinuation(s string, i int) int {
	switch {
	case s[i] != '\\':
		return 0
	case strings.HasPrefix(s[i+1:], "\n"):
		return 2
	}
	return 0
}

func skipDoubleQuoted(s string, i int) int {
	n := len(s)
	for i < n {
		switch {
		case s[i] == '\\' && i+1 < n:
			i += 2
		case s[i] == '"':
			return i + 1
		case s[i] == '$' && i+1 < n && s[i+1] == '(':
			if end, ok := skipSubstitution(s, i+2); ok {
				i = end
			} else {
				i += 2
			}
		default:
			i++
		}
	}
	return n
}

func skipSubstitution(s string, i int) (int, bool) {
	n, depth := len(s), 1
	for i < n {
		switch c := s[i]; {
		case c == '\\' && i+1 < n:
			i += 2
		case c == '\'':
			j := i + 1
			for j < n && s[j] != '\'' {
				j++
			}
			i = j + 1
		case c == '"':
			i = skipDoubleQuoted(s, i+1)
		case c == '<' && i+1 < n && s[i+1] == '<' && !(i+2 < n && s[i+2] == '<'):
			end, _, rest, ok := consumeHeredoc(s, i)
			if !ok {
				i += 2
				continue
			}
			if depth += parenDelta(rest); depth <= 0 {
				return end, true
			}
			i = end
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth--; depth == 0 {
				return i + 1, true
			}
			i++
		default:
			i++
		}
	}
	return n, false
}

func parenDelta(text string) int {
	d := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '(':
			d++
		case ')':
			d--
		case '\'', '"':
			q := text[i]
			for i++; i < len(text) && text[i] != q; i++ {
			}
		}
	}
	return d
}

func consumeHeredoc(s string, start int) (int, int, string, bool) {
	i := start + 2
	n := len(s)
	if i < n && s[i] == '-' {
		i++
	}
	for i < n && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= n {
		return start, start, "", false
	}
	delim := ""
	if s[i] == '\'' || s[i] == '"' {
		q := s[i]
		i++
		j := i
		for j < n && s[j] != q {
			j++
		}
		delim = s[i:j]
		if j < n {
			j++
		}
		i = j
	} else {
		j := i
		for j < n && !unicode.IsSpace(rune(s[j])) && s[j] != ';' && s[j] != '|' && s[j] != '&' {
			j++
		}
		delim = s[i:j]
		i = j
	}
	if delim == "" {
		return start, start, "", false
	}
	restStart := i
	for i < n && s[i] != '\n' {
		i++
	}
	rest := s[restStart:i]
	if i < n {
		i++
	}
	for i < n {
		lineStart := i
		for i < n && s[i] != '\n' {
			i++
		}
		line := s[lineStart:i]
		if line == delim {
			if i < n {
				i++
			}
			return i, restStart, rest, true
		}
		if i < n {
			i++
		}
	}
	return i, restStart, rest, true
}

type pipedSegment struct {
	Toks  []bashTok
	Piped bool
}

func splitSegments(toks []bashTok) []pipedSegment {
	var segs []pipedSegment
	var cur []bashTok
	piped, curPiped := false, false
	flush := func() {
		if len(cur) > 0 {
			segs = append(segs, pipedSegment{Toks: cur, Piped: curPiped})
			cur = nil
			piped = false
		}
	}
	for _, t := range toks {
		if t.Kind == "op" {
			flush()
			piped = piped || t.Op == "|"
			continue
		}
		if len(cur) == 0 {
			curPiped = piped
		}
		cur = append(cur, t)
	}
	flush()
	return segs
}

func wordsOnly(seg []bashTok) []string {
	var w []string
	for _, t := range seg {
		if t.Kind == "word" && t.Value != "" {
			w = append(w, t.Value)
		}
	}
	return w
}

func gitInvocation(words []string) (sub string, dirs []string, ok bool) {
	sub, dirs, _, ok = gitInvocationArgs(words)
	return sub, dirs, ok
}

func gitInvocationArgs(words []string) (sub string, dirs, args []string, ok bool) {
	if len(words) == 0 {
		return "", nil, nil, false
	}
	cmd := words[0]
	base := filepath.Base(cmd)
	if base != "git" && cmd != "git" {
		return "", nil, nil, false
	}
	i := 1
	for i < len(words) {
		w := words[i]
		switch {
		case w == "-C" || w == "-c":
			if w == "-C" && i+1 < len(words) {
				dirs = append(dirs, words[i+1])
			}
			i += 2
			continue
		case strings.HasPrefix(w, "-C"):
			dirs = append(dirs, w[2:])
			i++
			continue
		case strings.HasPrefix(w, "--git-dir=") ||
			strings.HasPrefix(w, "--work-tree=") ||
			strings.HasPrefix(w, "--exec-path=") ||
			strings.HasPrefix(w, "--namespace="):
			i++
			continue
		case w == "--git-dir" || w == "--work-tree" || w == "--exec-path" || w == "--namespace":
			i += 2
			continue
		case w == "-p" || w == "--paginate" || w == "--no-pager" ||
			w == "--bare" || w == "--literal-pathspecs" ||
			w == "--glob-pathspecs" || w == "--noglob-pathspecs" ||
			w == "--icase-pathspecs" || w == "--no-optional-locks" ||
			w == "--no-replace-objects":
			i++
			continue
		case strings.HasPrefix(w, "-") && w != "--":
			i++
			continue
		default:
			return strings.ToLower(w), dirs, words[i+1:], true
		}
	}
	return "", nil, nil, false
}

var dirFlagTable = map[string][]string{
	"git":  {"-C"},
	"make": {"-C"},
	"tar":  {"-C"},
}

var gitReadOnlySubcommands = map[string]bool{
	"diff": true, "status": true, "log": true, "show": true,
	"rev-parse": true, "rev-list": true, "ls-files": true,
}

func gitReadOnlyInvocation(words []string) bool {
	sub, _, ok := gitInvocation(words)
	if !ok || !gitReadOnlySubcommands[sub] {
		return false
	}
	for _, w := range words {
		if w == "--output" || strings.HasPrefix(w, "--output=") {
			return false
		}
	}
	return true
}

var mutationVerbs = map[string]bool{
	"rm": true, "mv": true, "cp": true, "touch": true, "mkdir": true,
	"install": true, "truncate": true, "dd": true, "sed": true,
	"chmod": true, "chown": true, "ln": true, "rsync": true,
}

func commandArgWords(toks []bashTok) []string {
	var out []string
	for _, t := range toks {
		switch {
		case t.Kind == "word" && t.Value != "":
			out = append(out, t.Value)
		case t.Kind == "string" && t.Value != "HEREDOC" && t.Raw == "":
			out = append(out, unquoteTok(t.Value))
		}
	}
	return out
}

type bashSegment struct {
	Tokens []bashTok
	Words  []string
	Idx    int
	Cwd    string
	Piped  bool
}

func forEachSegment(command, base string, logical bool, fn func(s bashSegment)) {
	cwd := base
	for _, ps := range splitSegments(tokenizeBash(command)) {
		seg := ps.Toks
		all := wordsOnly(seg)
		if len(all) == 0 {
			continue
		}
		idx := leadingAssignments(all)
		words := all[idx:]
		fn(bashSegment{Tokens: seg, Words: words, Idx: idx, Cwd: cwd, Piped: ps.Piped})
		if len(words) > 0 && filepath.Base(words[0]) == "cd" {
			cwd = cdDir(cwd, words[1:], logical)
		}
	}
}

func cdDir(cwd string, args []string, logical bool) string {
	physical := false
	for len(args) > 0 && len(args[0]) > 1 && strings.HasPrefix(args[0], "-") {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		if strings.Trim(args[0][1:], "LPe@") != "" {
			break
		}
		if i := strings.LastIndexAny(args[0], "LP"); i > 0 {
			physical = args[0][i] == 'P'
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return cwd
	}
	d := JoinRaw(cwd, args[0])
	switch {
	case d == "" || !logical:
		return d
	case physical:
		return filepath.ToSlash(CanonicalPath(native(d)))
	}
	return path.Clean(d)
}

func leadingAssignments(words []string) int {
	n := 0
	for n < len(words) && isAssignmentWord(words[n]) {
		n++
	}
	return n
}

func isAssignmentWord(w string) bool {
	eq := strings.IndexByte(w, '=')
	return eq > 0 && !strings.HasPrefix(w, "-") && isShellIdent(w[:eq])
}

func isAssignmentPrefix(w string) bool {
	if isAssignmentWord(w) {
		return true
	}
	plus := strings.Index(w, "+=")
	return plus > 0 && isShellIdent(w[:plus])
}

func programSegments(segs []bashSegment, j int) []bashSegment {
	var out []bashSegment
	for k := j; k >= 0; k-- {
		out = append(out, segs[k])
		if !segs[k].Piped {
			break
		}
	}
	return out
}

var shellInterpreters = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}

func interpreterBase(words []string) (family string, rest []string, ok bool) {
	i := 0
	if len(words) > 0 && filepath.Base(words[0]) == "env" {
		for i = 1; i < len(words); i++ {
			w := words[i]
			if !strings.HasPrefix(w, "-") && !strings.Contains(w, "=") {
				break
			}
		}
	}
	if i >= len(words) {
		return "", nil, false
	}
	base := filepath.Base(words[i])
	rest = words[i+1:]
	if v, isPy := strings.CutPrefix(base, "python"); isPy && strings.Trim(v, "0123456789.") == "" {
		return "python", rest, true
	}
	switch {
	case base == "node" || base == "nodejs":
		return "node", rest, true
	case base == "perl" || base == "ruby":
		return base, rest, true
	case shellInterpreters[base]:
		return "shell", rest, true
	}
	return "", nil, false
}

func interpreterCodeFlag(family, w string) bool {
	if len(w) < 2 || w[0] != '-' {
		return false
	}
	long := strings.HasPrefix(w, "--")
	switch family {
	case "shell", "python":
		return !long && strings.ContainsRune(w[1:], 'c')
	case "node":
		if long {
			name, _, _ := strings.Cut(w[2:], "=")
			return name == "eval" || name == "print"
		}
		cl := w[1:]
		if cl[0] == 'r' || strings.Trim(cl, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			return false
		}
		return strings.ContainsAny(cl, "ep")
	case "perl", "ruby":
		return !long && perlRubyCode(family == "perl", w[1:])
	}
	return false
}

func perlRubyCode(perl bool, cl string) bool {
	for i := 0; i < len(cl); i++ {
		switch c := cl[i]; c {
		case 'e':
			return true
		case 'E':
			if perl {
				return true
			}
		case 'I', 'M', 'm', 'r', 'i', 'x', 'F':
			return false
		case 'C':
			if !perl {
				return false
			}
			for i+1 < len(cl) && strings.IndexByte("IOESioDALa0123456789", cl[i+1]) >= 0 {
				i++
			}
		case 'D':
			if perl {
				return false
			}
		case 'd':
			if perl && i+1 < len(cl) && cl[i+1] == ':' {
				return false
			}
		case 'l', '0', 'T':
			for i+1 < len(cl) && cl[i+1] >= '0' && cl[i+1] <= '9' {
				i++
			}
		}
	}
	return false
}

func interpreterInlineProgram(seg bashSegment, isFile func(string) bool) bool {
	family, args, ok := interpreterBase(commandArgWords(commandTokens(seg.Tokens, seg.Idx)))
	if !ok {
		return false
	}
	for i := 0; i < len(args); i++ {
		w := args[i]
		if w == "-" {
			return true
		}
		takesValue := false
		switch {
		case family == "shell" && (w == "+o" || w == "+O"):
			takesValue = true
		case w == "--":
		case !strings.HasPrefix(w, "-"):
			if isFile != nil && isFile(JoinRaw(seg.Cwd, w)) {
				return false
			}
			continue
		case interpreterCodeFlag(family, w):
			return true
		case strings.HasPrefix(w, "--"):
			takesValue = (family == "shell" || family == "node") && !strings.Contains(w, "=")
		default:
			cl := w[1:]
			switch family {
			case "shell":
				if strings.ContainsRune(cl, 's') {
					return true
				}
				takesValue = strings.ContainsAny(cl, "oO")
			case "python":
				for k := 0; k < len(cl); k++ {
					if cl[k] == 'm' {
						return false
					}
					if cl[k] == 'W' || cl[k] == 'X' {
						takesValue = k == len(cl)-1
						break
					}
				}
			case "node":
				takesValue = w == "-r"
			}
		}
		if takesValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	if seg.Piped {
		return true
	}
	for _, t := range seg.Tokens {
		if t.Kind == "string" && t.Value == "HEREDOC" {
			return true
		}
	}
	return false
}

const maxShellProgramDepth = 4

var shellOpaqueWords = map[string]bool{
	"command": true, "builtin": true, "exec": true, "env": true, "nohup": true, "time": true,
	"sudo": true, "doas": true, "xargs": true, "nice": true, "timeout": true, "stdbuf": true, "setsid": true,
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "do": true, "done": true,
	"while": true, "until": true, "for": true, "case": true, "esac": true, "select": true,
	"function": true, "coproc": true, "!": true, "{": true, "}": true, "[[": true,
}

var bashBuiltinWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(". : [ alias bg bind break builtin caller cd command compgen complete compopt continue " +
		"declare dirs disown echo enable eval exec exit export false fc fg getopts hash help history jobs kill let local " +
		"logout mapfile popd printf pushd pwd read readarray readonly return set shift shopt source suspend test times " +
		"trap true type typeset ulimit umask unalias unset wait " +
		"! [[ ]] { } case coproc do done elif else esac fi for function if in select then time until while") {
		m[w] = true
	}
	return m
}()

func shellProgram(seg bashSegment) (string, bool) {
	_, prog, ok := shellProgramToken(seg)
	return prog, ok
}

func shellProgramToken(seg bashSegment) (int, string, bool) {
	if seg.Piped {
		return -1, "", false
	}
	for _, t := range seg.Tokens {
		if (t.Kind == "string" && t.Raw != "") || (t.Kind == "word" && strings.Contains(t.Value, "<")) {
			return -1, "", false
		}
	}
	toks := commandTokens(seg.Tokens, seg.Idx)
	off := len(seg.Tokens) - len(toks)
	var words []string
	var at []int
	for k, t := range toks {
		switch {
		case t.Kind == "word" && t.Value != "":
			words = append(words, t.Value)
		case t.Kind == "string":
			words = append(words, unquoteTok(t.Value))
		default:
			continue
		}
		at = append(at, off+k)
	}
	family, rest, ok := interpreterBase(words)
	if !ok || family != "shell" {
		return -1, "", false
	}
	coded := false
	for i := len(words) - len(rest); i < len(words); i++ {
		w := words[i]
		switch {
		case w == "-o" || w == "+o" || w == "-O" || w == "+O" || w == "--rcfile" || w == "--init-file":
			i++
		case w == "-":
			return -1, "", false
		case strings.HasPrefix(w, "--"):
		case len(w) > 1 && (w[0] == '-' || w[0] == '+'):
			if strings.ContainsAny(w[1:], "soO") {
				return -1, "", false
			}
			coded = coded || interpreterCodeFlag(family, w)
		case !coded:
			return -1, "", false
		default:
			return readShellProgram(seg.Tokens, at[i])
		}
	}
	return -1, "", false
}

func readShellProgram(toks []bashTok, k int) (int, string, bool) {
	t := toks[k]
	if (k > 0 && toks[k-1].End == t.Start) || (k+1 < len(toks) && toks[k+1].Start == t.End) {
		return -1, "", false
	}
	v, prog := t.Value, ""
	switch {
	case t.Kind == "word":
		if strings.ContainsAny(v, "\\$`*?[{~") {
			return -1, "", false
		}
		prog = v
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		prog = v[1 : len(v)-1]
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' && !strings.ContainsAny(v[1:len(v)-1], "\\$`"):
		prog = v[1 : len(v)-1]
	default:
		return -1, "", false
	}
	if strings.Contains(prog, "$(") || strings.Contains(prog, "`") || !shellProgramPlain(prog) {
		return -1, "", false
	}
	return k, prog, true
}

func shellProgramPlain(prog string) bool {
	for _, ps := range splitSegments(tokenizeBash(prog)) {
		toks := ps.Toks
		for j, t := range toks {
			if t.Kind != "word" {
				continue
			}
			if t.Value == "eval" || t.Value == "source" || backgroundAmp(toks, j) {
				return false
			}
		}
		i := 0
		for i < len(toks) && toks[i].Kind == "word" && isAssignmentWord(toks[i].Value) {
			for i++; i < len(toks) && toks[i].Start == toks[i-1].End; i++ {
			}
		}
		if i == len(toks) {
			continue
		}
		c := toks[i]
		if c.Kind != "word" || c.Value == "." || shellOpaqueWords[c.Value] || strings.HasPrefix(c.Value, "-") ||
			strings.ContainsAny(c.Value, "$`\\(){}<>") || (i+1 < len(toks) && toks[i+1].Start == c.End) {
			return false
		}
	}
	return true
}

func backgroundAmp(toks []bashTok, j int) bool {
	t := toks[j]
	return t.Kind == "word" && t.Value == "&" &&
		!(j > 0 && toks[j-1].End == t.Start && strings.HasSuffix(toks[j-1].Value, ">")) &&
		!(j+1 < len(toks) && toks[j+1].Start == t.End && strings.HasPrefix(toks[j+1].Value, ">"))
}

func isNameByte(c byte) bool {
	return c == '_' || c == '-' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isRunByte(c byte, back bool) bool {
	switch c {
	case '/', '~', '+', '@', '%':
		return true
	case '$', '{', '}':
		return back
	}
	return isNameByte(c)
}

func pathRefs(text string, roots []string) []string {
	var out []string
	for _, p := range roots {
		root := strings.TrimRight(strings.TrimSpace(p), "/")
		if root == "" {
			continue
		}
		for from := 0; from < len(text); {
			k := strings.Index(text[from:], root)
			if k < 0 {
				break
			}
			k += from
			end := k + len(root)
			from = end
			if (k > 0 && isNameByte(text[k-1])) || (end < len(text) && isNameByte(text[end])) {
				continue
			}
			start := k
			if k > 0 && text[k-1] == '/' {
				for start > 0 && isRunByte(text[start-1], true) {
					start--
				}
			}
			stop := end
			for stop < len(text) && isRunByte(text[stop], false) {
				stop++
			}
			out = append(out, text[start:stop])
		}
	}
	return out
}

var destLastVerbs = map[string]bool{
	"cp": true, "mv": true, "rsync": true, "ln": true, "install": true,
}

func mutationPathArgs(words []string, cwd string) []string {
	if len(words) < 2 {
		return nil
	}
	cmd := filepath.Base(words[0])
	var out []string
	if cmd == "dd" {
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "of=") {
				out = append(out, JoinRaw(cwd, strings.TrimPrefix(w, "of=")))
			}
		}
		return out
	}
	if cmd == "chmod" {
		return chmodPathArgs(words[1:], cwd)
	}

	if destLastVerbs[cmd] {
		var positionals []string
		skipNext := false
		for i := 1; i < len(words); i++ {
			w := words[i]
			if skipNext {
				skipNext = false
				continue
			}
			if strings.HasPrefix(w, "-") {
				if w == "-t" || w == "--target-directory" {
					if i+1 < len(words) {
						out = append(out, JoinRaw(cwd, words[i+1]))
						skipNext = true
					}
					continue
				}
				if w == "--backup" || w == "-S" || w == "--suffix" ||
					w == "-t" || strings.HasPrefix(w, "--target-directory=") {
					if !strings.Contains(w, "=") {
						skipNext = true
					}
				}
				continue
			}
			positionals = append(positionals, w)
		}
		if len(out) > 0 {
			return out
		}
		if len(positionals) == 0 {
			return nil
		}
		if len(positionals) == 1 {
			return []string{JoinRaw(cwd, positionals[0])}
		}
		return []string{JoinRaw(cwd, positionals[len(positionals)-1])}
	}

	skipNext := false
	for i := 1; i < len(words); i++ {
		w := words[i]
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(w, "-") {
			if (cmd == "rm" || cmd == "mkdir" || cmd == "chown") &&
				(w == "-t" || w == "--target-directory") {
				skipNext = true
			}
			continue
		}
		out = append(out, JoinRaw(cwd, w))
	}
	return out
}

var chmodMode = regexp.MustCompile(`^(?:[ugoa]*(?:[-+=](?:[rwxXst]*|[ugo]))+|[-+=]?[0-7]{1,4})(?:,(?:[ugoa]*(?:[-+=](?:[rwxXst]*|[ugo]))+|[-+=]?[0-7]{1,4}))*$`)

func chmodPathArgs(args []string, cwd string) []string {
	var out []string
	slotClosed, optsDone, skipNext := false, false, false
	for _, w := range args {
		switch {
		case skipNext:
			skipNext = false
		case w == "--" && !optsDone:
			optsDone = true
		case strings.HasPrefix(w, "-") && !optsDone:
			switch {
			case strings.HasPrefix(w, "--reference="):
				slotClosed = true
			case w == "--reference":
				slotClosed, skipNext = true, true
			case strings.HasPrefix(w, "--"):
			case chmodMode.MatchString(w):
				slotClosed = true
			}
		case !slotClosed:
			slotClosed = true
			if !chmodMode.MatchString(w) {
				out = append(out, JoinRaw(cwd, w))
			}
		default:
			out = append(out, JoinRaw(cwd, w))
		}
	}
	return out
}

func commandTokens(seg []bashTok, skipWords int) []bashTok {
	seen := 0
	for k, t := range seg {
		if t.Kind != "word" || t.Value == "" {
			continue
		}
		if seen == skipWords {
			return seg[k:]
		}
		seen++
	}
	return nil
}

func unquoteTok(v string) string {
	v = strings.TrimPrefix(v, "$")
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func sedPathArgs(toks []bashTok, cwd string) []string {
	if len(toks) < 2 {
		return nil
	}
	inPlace, hasScript, skipNext, optsDone := false, false, false, false
	var positionals []string
	for _, t := range toks[1:] {
		if t.Kind == "string" && t.Value == "HEREDOC" {
			continue
		}
		w := t.Value
		quoted := t.Kind == "string"
		if quoted {
			w = unquoteTok(w)
		}
		switch {
		case skipNext:
			skipNext = false
		case quoted || optsDone || w == "-" || !strings.HasPrefix(w, "-"):
			positionals = append(positionals, w)
		case w == "--":
			optsDone = true
		case strings.HasPrefix(w, "--"):
			name, _, hasValue := strings.Cut(w, "=")
			switch name {
			case "--in-place":
				inPlace = true
			case "--expression", "--file":
				hasScript = true
				skipNext = !hasValue
			case "--line-length":
				skipNext = !hasValue
			}
		default:
		cluster:
			for j := 1; j < len(w); j++ {
				switch w[j] {
				case 'i':
					inPlace = true
					break cluster
				case 'e', 'f':
					hasScript = true
					skipNext = j == len(w)-1
					break cluster
				case 'l':
					skipNext = j == len(w)-1
					break cluster
				}
			}
		}
	}
	if !inPlace {
		return nil
	}
	if !hasScript && len(positionals) > 0 {
		positionals = positionals[1:]
	}
	out := make([]string, 0, len(positionals))
	for _, p := range positionals {
		out = append(out, JoinRaw(cwd, p))
	}
	return out
}

func redirectTargets(seg []bashTok, cwd string) []string {
	var out []string
	target := func(t bashTok) (string, bool) {
		switch {
		case t.Kind == "word" && t.Value != "" && !isRedirectOp(t.Value):
			return t.Value, true
		case t.Kind == "string" && t.Value != "HEREDOC" && t.Raw == "":
			return unquoteTok(t.Value), true
		}
		return "", false
	}
	for i, t := range seg {
		if t.Kind != "word" || filepath.Base(t.Value) != "tee" {
			continue
		}
		for j := i + 1; j < len(seg); j++ {
			a := seg[j]
			if a.Kind == "word" {
				if strings.HasPrefix(a.Value, "-") {
					continue
				}
				if strings.Contains(a.Value, ">") {
					if isRedirectOp(a.Value) {
						j++
					}
					continue
				}
			}
			if p, ok := target(a); ok {
				out = append(out, JoinRaw(cwd, p))
			}
		}
	}
	for i := 0; i < len(seg); i++ {
		t := seg[i]
		if t.Kind != "word" || t.Value == "" {
			continue
		}
		w := t.Value
		if strings.Contains(w, ">") && !isRedirectOp(w) {
			if p := gluedRedirectPath(w); p != "" {
				out = append(out, JoinRaw(cwd, p))
			}
		}
		if !isRedirectOp(w) || i+1 >= len(seg) {
			continue
		}
		next := seg[i+1]
		if next.Kind == "word" && strings.HasPrefix(next.Value, ">") {
			continue
		}
		if next.Kind == "word" && next.Value == "&" {
			if i+2 < len(seg) {
				if after := seg[i+2]; after.Kind == "word" && isFdDupTarget(after.Value) {
					i += 2
					continue
				}
				if p, ok := target(seg[i+2]); ok {
					out = append(out, JoinRaw(cwd, p))
					i += 2
					continue
				}
			}
			i++
			continue
		}
		if strings.HasSuffix(w, ">&") && next.Kind == "word" && isFdDupTarget(next.Value) {
			i++
			continue
		}
		if p, ok := target(next); ok {
			out = append(out, JoinRaw(cwd, p))
			i++
		}
	}
	return out
}

func isRedirectOp(w string) bool {
	rest := strings.TrimLeft(w, "0123456789")
	if rest == w {
		rest = strings.TrimPrefix(w, "&")
	}
	switch rest {
	case ">", ">>", ">|", ">&":
		return true
	}
	return false
}

func isFdDupTarget(s string) bool {
	if s == "-" {
		return true
	}
	if s == "" {
		return false
	}
	if strings.HasSuffix(s, "-") && len(s) > 1 {
		s = s[:len(s)-1]
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func gluedRedirectPath(w string) string {
	idx := strings.LastIndex(w, ">>")
	if idx >= 0 {
		return gluedRedirectRemainder(w[idx+2:])
	}
	idx = strings.LastIndex(w, ">")
	if idx >= 0 {
		return gluedRedirectRemainder(w[idx+1:])
	}
	return ""
}

func gluedRedirectRemainder(rest string) string {
	if rest == "" || rest == "&" {
		return ""
	}
	if strings.HasPrefix(rest, "&") && isFdDupTarget(rest[1:]) {
		return ""
	}
	return rest
}

func expandShellPath(p string) string {
	var name, rest string
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"):
		name, rest = "HOME", p[1:]
	case strings.HasPrefix(p, "$"+"{"):
		end := strings.IndexByte(p, '}')
		if end < 0 {
			return p
		}
		name, rest = p[2:end], p[end+1:]
	case strings.HasPrefix(p, "$"):
		end := 1
		for end < len(p) && (p[end] == '_' || unicode.IsLetter(rune(p[end])) || unicode.IsDigit(rune(p[end]))) {
			end++
		}
		name, rest = p[1:end], p[end:]
	default:
		return p
	}
	if !isShellIdent(name) || (rest != "" && rest[0] != '/') {
		return p
	}
	val := os.Getenv(name)
	if !filepath.IsAbs(val) {
		return p
	}
	return val + rest
}

func resolvePath(cwd, p string) string {
	raw := JoinRaw(cwd, p)
	if raw == "" {
		return ""
	}
	return path.Clean(raw)
}

func JoinRaw(cwd, p string) string {
	p = expandShellPath(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) || cwd == "" {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(cwd) + "/" + filepath.ToSlash(p)
}

func isShellIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !unicode.IsLetter(r) && r != '_' {
				return false
			}
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

const operatorRecordVerb = "record"

var operatorRecordText = regexp.MustCompile(`(?m)(?:^|[\s'"/,\[(])satelle(?:[\s'",\])][^\n]*?)?[\s'",\[(]` + operatorRecordVerb + `(?:$|[\s'",\])])`)

var dataCommands = map[string]bool{
	"echo": true, "printf": true, "grep": true, "egrep": true, "fgrep": true,
	"cat": true, "head": true, "tail": true, "wc": true, "satelle": true,
}

var gitMessageSubcommands = map[string]bool{"commit": true, "tag": true}

var shellCompoundWords = map[string]bool{
	"{": true, "}": true, "do": true, "done": true, "then": true, "else": true, "elif": true,
	"fi": true, "if": true, "while": true, "until": true, "for": true, "case": true,
	"esac": true, "select": true, "function": true, "coproc": true, "!": true,
}

func operatorRecordCommand(command string, depth int) bool {
	segs := splitSegments(tokenizeBash(command))
	data := make([]bool, len(segs))
	compound := false
	for j, ps := range segs {
		data[j] = dataSegment(ps.Toks)
		if cmd := commandTokens(ps.Toks, leadingAssignments(wordsOnly(ps.Toks))); len(cmd) > 0 && shellCompoundWords[cmd[0].Value] {
			compound = true
		}
		for _, t := range ps.Toks {
			if t.Kind == "word" && strings.ContainsAny(t.Value, "()") {
				compound = true
			}
		}
	}
	sink := make([]bool, len(segs))
	for j := len(segs) - 1; j >= 0; j-- {
		sink[j] = j+1 == len(segs) || !segs[j+1].Piped || (data[j+1] && sink[j+1])
	}
	for j, ps := range segs {
		if operatorRecordSegment(ps.Toks, !compound && data[j] && sink[j], depth) {
			return true
		}
	}
	return false
}

func operatorRecordSegment(toks []bashTok, data bool, depth int) bool {
	cmd := commandTokens(toks, leadingAssignments(wordsOnly(toks)))
	for _, t := range toks[:len(toks)-len(cmd)] {
		if t.Kind == "string" && operatorRecordText.MatchString(tokText(t)) {
			return true
		}
	}
	if !data {
		if operatorRecordWords(toks) {
			return true
		}
		for _, t := range toks {
			if t.Kind == "string" && operatorRecordText.MatchString(tokText(t)) {
				return true
			}
		}
		return false
	}
	if cmd[0].Value == "satelle" && operatorRecordWords(cmd) {
		return true
	}
	for _, t := range cmd {
		if t.Kind == "string" && operatorRecordDataToken(t, depth) {
			return true
		}
	}
	return false
}

func tokText(t bashTok) string {
	if t.Raw != "" {
		return t.Raw
	}
	return t.Value
}

func operatorRecordDataToken(t bashTok, depth int) bool {
	v := t.Value
	switch {
	case t.Raw != "":
		opener := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(t.Raw, "<<"), "-"), " \t")
		if strings.HasPrefix(opener, "'") || strings.HasPrefix(opener, `"`) {
			return false
		}
		if strings.Contains(t.Raw, "$(") || strings.Contains(t.Raw, "`") {
			return operatorRecordText.MatchString(t.Raw)
		}
		return false
	case strings.HasPrefix(v, "'") || strings.HasPrefix(v, "$'"):
		return false
	case !strings.HasPrefix(v, `"`):
		return operatorRecordText.MatchString(v)
	}
	for i := 1; i < len(v); {
		switch {
		case v[i] == '\\':
			i += 2
		case v[i] == '`':
			return operatorRecordText.MatchString(v)
		case v[i] == '$' && i+1 < len(v) && v[i+1] == '(':
			end, ok := skipSubstitution(v, i+2)
			if !ok || depth+1 > maxShellProgramDepth {
				return operatorRecordText.MatchString(v)
			}
			if operatorRecordCommand(v[i+2:end-1], depth+1) {
				return true
			}
			i = end
		default:
			i++
		}
	}
	return false
}

func shellArgWords(toks []bashTok) []string {
	var out []string
	joined := false
	for i, t := range toks {
		var part string
		switch {
		case t.Kind == "word" && t.Value != "":
			part = t.Value
		case t.Kind == "string" && t.Value != "HEREDOC" && t.Raw == "":
			part = quotedPart(t.Value)
		default:
			joined = false
			continue
		}
		if joined && toks[i-1].End == t.Start && !shellWordBoundary(toks[i-1], t) {
			out[len(out)-1] += "\x01" + part
		} else {
			out = append(out, part)
		}
		joined = true
	}
	return out
}

func shellWordBoundary(prev, t bashTok) bool {
	const meta = "&|;<>()`"
	if prev.Kind == "word" && strings.HasSuffix(prev.Value, "$") {
		return true
	}
	if prev.Kind == "word" && prev.Value != "" && strings.ContainsRune(meta, rune(prev.Value[len(prev.Value)-1])) {
		return true
	}
	return t.Kind == "word" && t.Value != "" && (strings.ContainsRune(meta, rune(t.Value[0])) || strings.HasPrefix(t.Value, "$("))
}

func bareWords(toks []bashTok) []string {
	var out []string
	for _, t := range toks {
		if t.Kind == "word" && t.Value != "" {
			out = append(out, t.Value)
		}
	}
	return out
}

func quotedPart(v string) string {
	s := unquoteTok(v)
	if strings.HasPrefix(v, "$'") {
		s = ansiC(s)
	} else if strings.HasPrefix(v, "\"") || strings.HasPrefix(v, "$\"") {
		s = dropExpansions(s)
	}
	return strings.Map(func(r rune) rune {
		if r == '{' || r == '}' || r == ',' || r == '*' || r == '?' || r == '[' {
			return '\x00'
		}
		return r
	}, s)
}

func ansiC(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; {
		case c == 'x' || c == 'u' || c == 'U':
			max := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
			j := i + 1
			for j < len(s) && j-i-1 < max && strings.IndexByte("0123456789abcdefABCDEF", s[j]) >= 0 {
				j++
			}
			if v, err := strconv.ParseUint(s[i+1:j], 16, 32); err == nil {
				b.WriteRune(rune(v))
			}
			i = j - 1
		case c >= '0' && c <= '7':
			j := i
			for j < len(s) && j-i < 3 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			v, _ := strconv.ParseUint(s[i:j], 8, 32)
			b.WriteByte(byte(v))
			i = j - 1
		default:
			if m, ok := map[byte]byte{'n': '\n', 't': '\t', 'r': '\r', 'a': '\a', 'b': '\b', 'e': 0x1b, 'E': 0x1b, 'f': '\f', 'v': '\v'}[c]; ok {
				b.WriteByte(m)
			} else if c == '\'' || c == '"' || c == '\\' || c == '?' {
				b.WriteByte(c)
			} else {
				b.WriteByte('\\')
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

func braceWords(w string) []string {
	depth, open := 0, -1
	for i := 0; i < len(w); i++ {
		switch w[i] {
		case '\\':
			i++
		case '{':
			if i > 0 && w[i-1] == '$' {
				continue
			}
			if depth == 0 {
				open = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			if depth--; depth > 0 {
				continue
			}
			var alts []string
			d, from := 0, open+1
			for k := open + 1; k < i; k++ {
				switch w[k] {
				case '{':
					d++
				case '}':
					d--
				case ',':
					if d == 0 {
						alts = append(alts, w[from:k])
						from = k + 1
					}
				}
			}
			if alts == nil {
				open = -1
				continue
			}
			alts = append(alts, w[from:i])
			var out []string
			for _, a := range alts {
				for _, x := range braceWords(w[:open] + a + w[i+1:]) {
					if len(out) < 64 {
						out = append(out, x)
					}
				}
			}
			return out
		}
	}
	return []string{w}
}

func wordText(w string) string {
	w = dropExpansions(strings.ReplaceAll(w, "\\\n", ""))
	return strings.NewReplacer("\\", "", "\x01", "").Replace(w)
}

func dropExpansions(w string) string {
	var b strings.Builder
	for i := 0; i < len(w); i++ {
		c := w[i]
		if c == '\\' && i+1 < len(w) {
			b.WriteByte(c)
			b.WriteByte(w[i+1])
			i++
			continue
		}
		if c == '`' {
			if k := strings.IndexByte(w[i+1:], '`'); k >= 0 {
				i += k + 1
				continue
			}
		}
		if c != '$' || i+1 >= len(w) {
			b.WriteByte(c)
			continue
		}
		switch d := w[i+1]; {
		case d == '(' || d == '{':
			closer := map[byte]byte{'(': ')', '{': '}'}[d]
			depth, j := 0, i+1
			for ; j < len(w); j++ {
				if w[j] == d {
					depth++
				} else if w[j] == closer {
					if depth--; depth == 0 {
						break
					}
				}
			}
			if j >= len(w) {
				b.WriteByte(c)
				continue
			}
			i = j
		case d == '_' || unicode.IsLetter(rune(d)):
			j := i + 1
			for j < len(w) && (w[j] == '_' || unicode.IsLetter(rune(w[j])) || unicode.IsDigit(rune(w[j]))) {
				j++
			}
			i = j - 1
		case strings.IndexByte("0123456789@*#?$!-", d) >= 0:
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func commandName(w string) string {
	w = wordText(w)
	for _, p := range []string{"$(", "<(", ">(", "(", "`"} {
		if k := strings.LastIndex(w, p); k >= 0 {
			w = w[k+len(p):]
		}
	}
	if k := strings.IndexAny(w, "&|;<>$"); k >= 0 {
		w = w[:k]
	}
	return w
}

func verbWord(w string) string {
	w = wordText(w)
	if k := strings.IndexAny(w, "&|;<>$"); k >= 0 {
		w = w[:k]
	}
	return strings.TrimRight(w, ")`")
}

func globMatches(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return false
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

func operatorRecordWords(toks []bashTok) bool {
	return recordVerbIn(expandedWords(shellArgWords(toks))) || recordVerbIn(expandedWords(bareWords(toks)))
}

func expandedWords(words []string) []string {
	var out []string
	for _, w := range words {
		out = append(out, braceWords(w)...)
	}
	return out
}

func recordVerbIn(words []string) bool {
	for i, w := range words {
		if !globMatches(path.Base(commandName(w)), "satelle") {
			continue
		}
		for j := i + 1; j < len(words); j++ {
			if globMatches(verbWord(words[j]), operatorRecordVerb) && !takesSeparateValue(words[j-1]) {
				return true
			}
		}
	}
	return false
}

func takesSeparateValue(w string) bool {
	if !strings.HasPrefix(w, "-") || strings.Contains(w, "=") {
		return false
	}
	return strings.HasPrefix(w, "--") || len(w) == 2
}

func dataSegment(toks []bashTok) bool {
	cmd := commandTokens(toks, leadingAssignments(wordsOnly(toks)))
	if len(cmd) == 0 {
		return false
	}
	k := len(toks) - len(cmd)
	name := cmd[0].Value
	if (k > 0 && toks[k-1].End == cmd[0].Start) || (len(cmd) > 1 && cmd[1].Start == cmd[0].End) || strings.Contains(name, "/") {
		return false
	}
	words := commandArgWords(cmd)
	switch {
	case name == "git":
		if !gitDataInvocation(words) {
			return false
		}
	case !dataCommands[name]:
		return false
	case name == "printf":
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "-v") {
				return false
			}
		}
	}
	for j, t := range toks {
		switch {
		case t.Kind == "word" && (strings.ContainsAny(t.Value, "`(") || backgroundAmp(toks, j)):
			return false
		case t.Kind == "string" && strings.HasPrefix(t.Raw, "<<<"):
			return false
		}
	}
	return true
}

func gitDataInvocation(words []string) bool {
	sub, _, args, ok := gitInvocationArgs(words)
	if !ok || !(gitReadOnlySubcommands[sub] || gitMessageSubcommands[sub]) {
		return false
	}
	for _, w := range words[1 : len(words)-len(args)-1] {
		if strings.HasPrefix(w, "-c") || strings.HasPrefix(w, "--config-env") || strings.HasPrefix(w, "--exec-path") {
			return false
		}
	}
	return true
}
