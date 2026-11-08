package hook

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// bashscan.go is a quote-aware, best-effort Bash classifier: it names what a
// command would write so the edit gate can judge a shell command by the same
// predicate as an edit. Honest scope: a reminder and a boundary, not a
// sandbox. Variable expansion beyond a leading ~ / $VAR from the hook's own
// environment, eval, xargs, command substitution and symlink escapes are out
// of scope; an ambiguous form names no target rather than a guess.

// BashTargets is what a Bash command would change.
type BashTargets struct {
	// Paths are absolute write targets the command names: redirects and tee,
	// the -C dir of a mutating git/make/tar, path arguments of mutation verbs,
	// and the same inside a readable sh -c program.
	Paths []string
	// Inline are the working dirs of interpreters running program text the
	// scan cannot read (a code flag, `-`, or stdin fed by a heredoc or pipe):
	// such a program may write anywhere under its working dir.
	Inline []string
	// OperatorRecord is set when the command would run satelle's operator record
	// verb: satelle at any word position of a segment (behind env, command,
	// timeout, xargs and the like) with a later record word, or the two on one
	// line of quoted or heredoc text (sh -c, script -c, an interpreter -c
	// program). It is fail-closed: prose that quotes the verb counts too.
	OperatorRecord bool
}

// ScanBash classifies command walked from cwd. root, when set, is the tree
// whose name in inline program text counts as a write target. isFile tells a
// named script from inline text; nil means no file exists (fail closed).
func ScanBash(command, cwd, root string, isFile func(string) bool) BashTargets {
	var t BashTargets
	seen := map[string]bool{}
	add := func(dst *[]string, p string) {
		p = filepath.Clean(p)
		if p == "" || p == "." || seen[p] {
			return
		}
		seen[p] = true
		*dst = append(*dst, p)
	}
	scanBash(command, filepath.Clean(cwd), root, isFile, 0, func(p string) { add(&t.Paths, p) }, func(p string) { add(&t.Inline, p) },
		func() { t.OperatorRecord = true })
	return t
}

func scanBash(command, cwd, root string, isFile func(string) bool, depth int, path, inline func(string), record func()) {
	var segs []bashSegment
	forEachSegment(command, cwd, func(s bashSegment) { segs = append(segs, s) })
	for j, sg := range segs {
		for _, r := range redirectTargets(sg.Tokens, sg.Cwd) {
			path(r)
		}
		if operatorRecordSegment(sg) {
			record()
		}
		words := sg.Words
		if len(words) == 0 {
			continue
		}
		if depth < maxShellProgramDepth {
			if prog, ok := shellProgram(sg); ok {
				scanBash(prog, sg.Cwd, root, isFile, depth+1, path, inline, record)
				continue
			}
		}
		cmd := filepath.Base(words[0])
		if cmd == "cd" {
			continue
		}
		if flags, ok := dirFlagTable[cmd]; ok && (cmd != "git" || !gitReadOnlyInvocation(words)) {
			for i := 1; i < len(words); i++ {
				for _, f := range flags {
					if words[i] == f && i+1 < len(words) {
						path(resolvePath(sg.Cwd, words[i+1]))
						i++
						break
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
					if abs := resolvePath(p.Cwd, run); abs != "" {
						path(abs)
					}
				}
			}
		}
	}
}

// bashTok is one lexical token from a best-effort scan.
type bashTok struct {
	// Kind is "word" for an unquoted shell word, "string" for a quoted span
	// (single/double/dollar-quote/heredoc body collapsed to one opaque token),
	// or "op" for a segment operator (;, &&, ||, |, newline).
	Kind  string
	Value string // raw value for word; empty or placeholder for string/op
	Op    string // for Kind=="op": ";", "&&", "||", "|", "\n"
	// Raw is the full source text of a heredoc token (opener line and body), which
	// Value collapses to a placeholder. Text scans that must see what an
	// interpreter is fed (interpreterPathRefs) read it; word classifiers never do.
	Raw string
	// Start and End are the token's byte offsets in the tokenized text. Two
	// tokens with no gap between them (End == next Start) are one shell word
	// built from pieces (`'a'\''b'`), which shellProgram must not read as one.
	Start, End int
}

// tokenizeBash splits a Bash payload into tokens. Quoted spans and heredoc
// bodies become a single opaque "string" token so prose (e.g. a --body argument)
// cannot contribute matching word tokens to commit/push detection.
func tokenizeBash(command string) []bashTok {
	var out []bashTok
	emit := func(t bashTok, start, end int) {
		t.Start, t.End = start, end
		out = append(out, t)
	}
	i := 0
	n := len(command)
	for i < n {
		// Whitespace (except newline, which is a segment op).
		if command[i] == ' ' || command[i] == '\t' || command[i] == '\r' {
			i++
			continue
		}
		// Newline segment boundary.
		if command[i] == '\n' {
			emit(bashTok{Kind: "op", Op: "\n"}, i, i+1)
			i++
			continue
		}
		// Operators: && || ; |
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
		// Heredoc: <<[-]['"]WORD['"] … WORD
		if command[i] == '<' && i+1 < n && command[i+1] == '<' {
			end, restStart, rest, ok := consumeHeredoc(command, i)
			if ok {
				emit(bashTok{Kind: "string", Value: "HEREDOC", Raw: command[i:end]}, i, restStart)
				// What follows the delimiter on the opener line (`<<EOF > f`,
				// `<<EOF | python3 -`) is ordinary shell and is classified as such;
				// the line after the closing delimiter starts a new command.
				for _, t := range tokenizeBash(rest) {
					emit(t, t.Start+restStart, t.End+restStart)
				}
				emit(bashTok{Kind: "op", Op: "\n"}, end, end)
				i = end
				continue
			}
		}
		// Single-quoted string.
		if command[i] == '\'' {
			j := i + 1
			for j < n && command[j] != '\'' {
				j++
			}
			if j < n {
				j++ // closing quote
			}
			emit(bashTok{Kind: "string", Value: command[i:j]}, i, j)
			i = j
			continue
		}
		// Double-quoted string (best-effort: \" and \\ escapes, and a $( … )
		// substitution inside it nests its own quotes and heredocs).
		if command[i] == '"' {
			j := skipDoubleQuoted(command, i+1)
			emit(bashTok{Kind: "string", Value: command[i:j]}, i, j)
			i = j
			continue
		}
		// Dollar-single-quote $'...'
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
		// Unquoted word: runs until whitespace, op, quote, or redirect marker start
		// that begins a separate token (we keep > / >> / < glued to the word when
		// they are part of a redirect like >file without space; spaced redirects
		// are handled as separate words).
		j := i
		for j < n {
			c := command[j]
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' ||
				c == ';' || c == '|' || c == '&' ||
				c == '\'' || c == '"' {
				break
			}
			// Stop before && / || (already handled if at start).
			if c == '&' || (c == '|' && j+1 < n && command[j+1] == '|') {
				break
			}
			// Heredoc start inside a word is rare; treat < as word content unless <<.
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
		// Fallback: single unknown char as word.
		emit(bashTok{Kind: "word", Value: string(command[i])}, i, i+1)
		i++
	}
	return out
}

// skipDoubleQuoted returns the index after the double-quoted string whose body
// starts at i (just past the opening quote), or len(s) when it never closes. A
// `$(` opens a substitution whose own quotes and heredoc bodies belong to it, so
// an odd inner quote (`the "x file` inside `$(cat <<'EOF' … EOF)`) does not end the
// outer string. A substitution that never closes is read as plain text, which is the
// pre-nesting behaviour.
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

// skipSubstitution returns the index after the `)` that closes a `$(` whose body
// starts at i, and false when it never closes. Single and double quotes, nested
// substitutions and `<<` heredocs inside it are skipped as units.
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
			// The opener line can close the substitution (`<<EOF | wc)`).
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

// parenDelta is the net `(` minus `)` in text outside single and double quotes.
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

// consumeHeredoc advances past a heredoc opener and its body. Returns the index
// after the terminating delimiter line, the offset and text of the rest of the
// opener line after the delimiter word, or (start, start, "", false) if it cannot
// parse.
func consumeHeredoc(s string, start int) (int, int, string, bool) {
	// start points at first '<' of <<
	i := start + 2
	n := len(s)
	if i < n && s[i] == '-' {
		i++ // <<-
	}
	// Optional quotes around delimiter.
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
	// The rest of the opener line, up to the newline that starts the body.
	restStart := i
	for i < n && s[i] != '\n' {
		i++
	}
	rest := s[restStart:i]
	if i < n {
		i++ // past newline
	}
	// Body until a line that is exactly delim.
	for i < n {
		lineStart := i
		for i < n && s[i] != '\n' {
			i++
		}
		line := s[lineStart:i]
		if line == delim {
			if i < n {
				i++ // consume newline after delimiter
			}
			return i, restStart, rest, true
		}
		if i < n {
			i++
		}
	}
	return i, restStart, rest, true // unterminated: consume rest
}

// pipedSegment is one command segment with whether a pipe feeds it.
type pipedSegment struct {
	Toks  []bashTok
	Piped bool // the operators between this segment and the previous one include "|"
}

// splitSegments splits tokens into command segments at ; && || | newline,
// remembering which ones a pipe feeds.
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

// wordsOnly returns the Value of word-kind tokens in a segment (for classifiers
// that need bare shell words). String tokens are omitted so quoted prose never
// matches as a command word.
func wordsOnly(seg []bashTok) []string {
	var w []string
	for _, t := range seg {
		if t.Kind == "word" && t.Value != "" {
			w = append(w, t.Value)
		}
	}
	return w
}

// gitInvocation parses a git segment's global options. It returns the subcommand
// (lowercased) and the -C DIR operands that precede it, in order; ok is false when
// the words are not a git invocation with a subcommand.
func gitInvocation(words []string) (sub string, dirs []string, ok bool) {
	sub, dirs, _, ok = gitInvocationArgs(words)
	return sub, dirs, ok
}

// gitInvocationArgs is gitInvocation that also returns the words after the
// subcommand.
func gitInvocationArgs(words []string) (sub string, dirs, args []string, ok bool) {
	if len(words) == 0 {
		return "", nil, nil, false
	}
	cmd := words[0]
	base := filepath.Base(cmd)
	// Strip a trailing path form: /usr/bin/git or ./git
	if base != "git" && cmd != "git" {
		return "", nil, nil, false
	}
	i := 1
	for i < len(words) {
		w := words[i]
		switch {
		case w == "-C" || w == "-c":
			// Takes a following argument.
			if w == "-C" && i+1 < len(words) {
				dirs = append(dirs, words[i+1])
			}
			i += 2
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
			// Unknown global option: skip conservatively without consuming an arg
			// (avoids treating a verb as an option value).
			i++
			continue
		default:
			// First non-option word is the subcommand.
			return strings.ToLower(w), dirs, words[i+1:], true
		}
	}
	return "", nil, nil, false
}

// dirFlagTable maps command basename → option letters that take a directory
// argument (e.g. git -C DIR). Only named tools; never guessed per tool.
var dirFlagTable = map[string][]string{
	"git":  {"-C"},
	"make": {"-C"},
	"tar":  {"-C"},
}

// gitReadOnlySubcommands is the closed list of git subcommands that only read.
// A `git -C DIR` naming one of them does not mutate DIR; any other subcommand, or
// one that cannot be parsed, stays a mutation, so the check fails closed.
var gitReadOnlySubcommands = map[string]bool{
	"diff": true, "status": true, "log": true, "show": true,
	"rev-parse": true, "rev-list": true, "ls-files": true,
}

// gitReadOnlyInvocation reports whether words are a git command whose subcommand
// only reads. A --output flag writes its operand, so it is never read-only.
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

// mutationVerbs are simple path-mutating commands whose path arguments (when
// they resolve outside the anchor) are containment targets.
var mutationVerbs = map[string]bool{
	"rm": true, "mv": true, "cp": true, "touch": true, "mkdir": true,
	"install": true, "truncate": true, "dd": true, "sed": true,
	"chmod": true, "chown": true, "ln": true, "rsync": true,
}

// commandArgWords is a command segment's words with each quoted argument
// unquoted into its slot. A quoted path is as much a target as a bare one —
// a harness's shell tool may emit `rm "/abs/other.txt"` — and the
// word list drops string tokens, so `rm "f"` named no target at all. A heredoc
// body is not an argument and stays out.
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

// bashSegment is one non-empty command segment as forEachSegment yields it.
type bashSegment struct {
	Tokens []bashTok // the segment's raw tokens (redirects, sed scripts)
	Words  []string  // bare words after any leading NAME=value assignments; empty for an env-only segment
	Idx    int       // number of assignment words stripped (commandTokens(Tokens, Idx))
	Cwd    string    // working dir in effect BEFORE this segment's own cd applies
	Piped  bool      // a pipe feeds this segment from the one before it
}

// forEachSegment walks a Bash payload's command segments in order, seeding the
// working dir at base and updating it after each `cd DIR`. It yields every segment
// that has a word, cd and env-only segments included, so a caller that must see
// redirects on them still does. It is the one cd/env-assignment walk shared by the
// containment classifier (bashMutationTargets) and commit attribution
// (gitCommandDir), so the two cannot drift.
func forEachSegment(command, base string, fn func(s bashSegment)) {
	cwd := base
	for _, ps := range splitSegments(tokenizeBash(command)) {
		seg := ps.Toks
		all := wordsOnly(seg)
		if len(all) == 0 {
			continue
		}
		// Strip env assignments: FOO=bar cmd …
		idx := leadingAssignments(all)
		words := all[idx:]
		fn(bashSegment{Tokens: seg, Words: words, Idx: idx, Cwd: cwd, Piped: ps.Piped})
		if len(words) >= 2 && filepath.Base(words[0]) == "cd" {
			cwd = resolvePath(cwd, words[1])
		}
	}
}

// leadingAssignments is the number of NAME=value words that open a segment's word
// list, before its command word.
func leadingAssignments(words []string) int {
	n := 0
	for n < len(words) && isAssignmentWord(words[n]) {
		n++
	}
	return n
}

// isAssignmentWord reports whether w reads as a NAME=value shell assignment.
func isAssignmentWord(w string) bool {
	eq := strings.IndexByte(w, '=')
	return eq > 0 && !strings.HasPrefix(w, "-") && isShellIdent(w[:eq])
}

// programSegments is the segment at j and the segments piping into it, nearest
// first: everything that supplies or is the interpreter's program text.
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

// The interpreters (interpreterBase: python, node, perl, ruby and the shells in
// shellInterpreters) run a program whose writes the classifier cannot see from the
// command line alone. The tables here only say which commands carry
// program text. A versioned python (python3.11) counts as python.

// shellInterpreters run a program from a c cluster (-c), from -s or `-`, or from
// stdin when no script file is named.
var shellInterpreters = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}

// interpreterBase sees through a leading `env [-opts] [NAME=val]…` and names the
// interpreter family of the command word ("python", "node", "perl", "ruby" or
// "shell"), with the words after it. ok is false for any other command.
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

// interpreterCodeFlag reports whether one flag word hands the interpreter its
// program on the command line. Shells and python: a single-dash cluster containing
// c (python's rest of the word is the code, so -BcCODE counts). Node: -e/-p, alone
// or in a letters-only cluster, and --eval/--print. Perl and ruby: e (perl also E)
// reached while reading the cluster left to right (see perlRubyCode).
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

// perlRubyCode reads a perl or ruby flag cluster left to right and reports whether
// it reaches a code letter. A letter that takes an attached value ends the
// cluster, because the rest of the word is that value (perl -I/x, -Mfeature=say,
// -i.bak, -xdir, -Fre, -Dflags, -d:Mod; ruby -rlib, -Cdir, -Ikdir).
// l and 0 (and ruby T) consume only following digits; perl C consumes only the
// characters of its unicode flags; every other letter is a plain switch, so
// -lpe, -0pe, -Te, -de and -CSDAe all reach their e.
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

// interpreterInlineProgram reports whether a segment runs an interpreter on
// program text that is not in a named script file, so what it writes cannot be
// read off the command line. The argument words are walked in order (quoted ones
// included): `-`, a code flag (interpreterCodeFlag) or, for a shell, an s in a
// cluster means inline; a flag that takes a separate value skips that value
// unless it starts with `-` (then it is read as a flag); python -m names a script
// module; the first non-flag word the injected isFile reports as an existing
// regular file (resolved against the segment's working dir) is the script, after
// which nothing is inline; any other non-flag word is skipped. With the words
// exhausted and no script, the program is read from stdin, which is inline only
// when a heredoc or a pipe feeds it (a bare interpreter is a REPL).
//
// Accepted fail-closed limits: an unknown --long flag on a shell or node takes the
// next word, which can be the script; a script that does not exist yet reads as
// inline; a python cluster with an attached -W/-X/-m value that contains a c reads
// as code.
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
			if isFile != nil && isFile(resolvePath(seg.Cwd, w)) {
				return false
			}
			continue
		case interpreterCodeFlag(family, w):
			return true
		case strings.HasPrefix(w, "--"):
			// Python, perl and ruby long flags take no separate value.
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
						return false // python -m <module>: a named script
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

// maxShellProgramDepth bounds how many shell -c programs nested in one another are
// classified as shell; a deeper one is read as unparseable.
const maxShellProgramDepth = 4

// shellOpaqueWords are command words behind which the classifier cannot see the
// command that runs: wrappers that run their arguments as a command, and reserved
// words that open a compound command. A shell -c program that has one as a command
// word is not parseable.
var shellOpaqueWords = map[string]bool{
	"command": true, "builtin": true, "exec": true, "env": true, "nohup": true, "time": true,
	"sudo": true, "doas": true, "xargs": true, "nice": true, "timeout": true, "stdbuf": true, "setsid": true,
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "do": true, "done": true,
	"while": true, "until": true, "for": true, "case": true, "esac": true, "select": true,
	"function": true, "coproc": true, "!": true, "{": true, "}": true, "[[": true,
}

// shellProgram returns the program of a shell segment run as `sh|bash|zsh|dash|ksh
// [opts] -c PROGRAM` when the classifier reads it exactly as the shell will run it,
// so the program can be judged as shell — by what it writes — rather than as
// opaque interpreter text. ok is false, and the caller keeps the conservative rule,
// unless all of these hold:
//   - the segment has no stdin program: no pipe feeds it, and it carries no heredoc,
//     here-string or `<` redirect;
//   - the program word is one complete token, not glued to another piece (a
//     quote spliced into it), and is single-quoted, or double-quoted or bare with no
//     backslash, `$` or backtick (a bare word also has no glob or brace);
//   - the program has no `$(` or backtick anywhere;
//   - every command word in it is a bare word with no `$`, backslash, paren, brace
//     or redirect, is not `.` and is not in shellOpaqueWords; no word is eval or
//     source; and no `&` runs a command in the background.
func shellProgram(seg bashSegment) (string, bool) {
	_, prog, ok := shellProgramToken(seg)
	return prog, ok
}

// shellProgramToken is shellProgram that also returns the index of the program
// token in seg.Tokens, so a text scan can skip exactly that token.
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
	var at []int // index in seg.Tokens of each word
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
			i++ // takes a value
		case w == "-":
			return -1, "", false
		case strings.HasPrefix(w, "--"):
		case len(w) > 1 && (w[0] == '-' || w[0] == '+'):
			if strings.ContainsAny(w[1:], "soO") {
				return -1, "", false
			}
			coded = coded || interpreterCodeFlag(family, w)
		case !coded:
			return -1, "", false // a script, or a word the classifier cannot place
		default:
			return readShellProgram(seg.Tokens, at[i])
		}
	}
	return -1, "", false
}

// readShellProgram reads the program token at k of toks under shellProgram's
// quoting and parse rules.
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

// shellProgramPlain reports whether every command word of prog is one the
// classifier can name (see shellProgram).
func shellProgramPlain(prog string) bool {
	for _, ps := range splitSegments(tokenizeBash(prog)) {
		toks := ps.Toks
		for j, t := range toks {
			if t.Kind != "word" {
				continue
			}
			if t.Value == "eval" || t.Value == "source" {
				return false
			}
			// A lone & is a background separator unless it is glued into a
			// redirect (2>&1, &>f).
			if t.Value == "&" && !(j > 0 && toks[j-1].End == t.Start && strings.HasSuffix(toks[j-1].Value, ">")) &&
				!(j+1 < len(toks) && toks[j+1].Start == t.End && strings.HasPrefix(toks[j+1].Value, ">")) {
				return false
			}
		}
		i := 0
		for i < len(toks) && toks[i].Kind == "word" && isAssignmentWord(toks[i].Value) {
			for i++; i < len(toks) && toks[i].Start == toks[i-1].End; i++ {
			}
		}
		if i == len(toks) {
			continue // assignments only
		}
		c := toks[i]
		if c.Kind != "word" || c.Value == "." || shellOpaqueWords[c.Value] || strings.HasPrefix(c.Value, "-") ||
			strings.ContainsAny(c.Value, "$`\\(){}<>") || (i+1 < len(toks) && toks[i+1].Start == c.End) {
			return false
		}
	}
	return true
}

// isNameByte is a byte that continues a file or identifier name. A root token
// is a whole token only when neither neighbour is one.
func isNameByte(c byte) bool {
	return c == '_' || c == '-' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isRunByte is a byte of a path-like run around a root token. Looking back it
// also accepts the characters of an unexpanded $VAR / ${VAR} / ~ prefix.
func isRunByte(c byte, back bool) bool {
	switch c {
	case '/', '~', '+', '@', '%':
		return true
	case '$', '{', '}':
		return back
	}
	return isNameByte(c)
}

// pathRefs returns the path-like runs in text that name one of roots: the
// root with its trailing slash removed as a whole token, so both `<root>/x.md`
// and a bare `'<root>'` (os.path.join, Path(...)/, D=<root>) match. A token preceded by `/` is the tail of a longer path, so the
// run extends backward to its start; the run is unresolved text.
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

// destLastVerbs treat only the last non-option positional as a mutation target;
// earlier positionals are SOURCES (reads) and must not trip containment.
// With -t/--target-directory DIR, DIR is the sole target.
var destLastVerbs = map[string]bool{
	"cp": true, "mv": true, "rsync": true, "ln": true, "install": true,
}

// mutationPathArgs extracts DEST path arguments from a mutation verb segment.
// For dest-last verbs (cp/mv/rsync/ln/install), only the destination contributes;
// sources are legitimate cross-repo reads the fence must not deny.
func mutationPathArgs(words []string, cwd string) []string {
	if len(words) < 2 {
		return nil
	}
	cmd := filepath.Base(words[0])
	var out []string
	// dd of=PATH
	if cmd == "dd" {
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "of=") {
				out = append(out, resolvePath(cwd, strings.TrimPrefix(w, "of=")))
			}
		}
		return out
	}
	if cmd == "chmod" {
		return chmodPathArgs(words[1:], cwd)
	}

	// dest-last family: collect -t DIR first; else only the last positional.
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
						out = append(out, resolvePath(cwd, words[i+1]))
						skipNext = true
					}
					continue
				}
				// Options that take a value (best-effort skip).
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
		// -t already populated out; do not also emit positionals as targets.
		if len(out) > 0 {
			return out
		}
		// ln with a single positional links into cwd — no outside target.
		if len(positionals) == 0 {
			return nil
		}
		if len(positionals) == 1 {
			// Destination is cwd-relative implied link name, or a single dest —
			// treat as dest under cwd (in-home filter drops it if so).
			return []string{resolvePath(cwd, positionals[0])}
		}
		// Last positional is the destination; sources are ignored.
		return []string{resolvePath(cwd, positionals[len(positionals)-1])}
	}

	// All-args family: rm/touch/mkdir/chown/truncate — every non-option arg
	// is a mutation target (sed and chmod have their own extractors).
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
		out = append(out, resolvePath(cwd, w))
	}
	return out
}

// chmodMode matches a chmod mode as GNU chmod reads one: comma-separated
// clauses, each symbolic ([ugoa]*([-+=]([rwxXst]*|[ugo]))+, as in +x, u+w,
// a=r, go-w, o=) or numeric ([-+=]?[0-7]{1,4}, as in 755, 0644, =755).
var chmodMode = regexp.MustCompile(`^(?:[ugoa]*(?:[-+=](?:[rwxXst]*|[ugo]))+|[-+=]?[0-7]{1,4})(?:,(?:[ugoa]*(?:[-+=](?:[rwxXst]*|[ugo]))+|[-+=]?[0-7]{1,4}))*$`)

// chmodPathArgs extracts the files a chmod invocation writes from its args
// (command word excluded). chmod reads its mode from one place only, so its
// mode slot is decided once and never reopens: by a dash-mode option (-x, -w,
// -t: a single-dash word matching chmodMode, which no chmod flag letter
// c/f/v/R does), by --reference (no mode operand; the reference file is only
// read), or by the first positional. That first positional is dropped only
// when it parses as a mode; otherwise it fails closed and stays a target, as
// does every later positional, mode-shaped or not, because chmod treats it as
// a file. Words after `--` are positionals.
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
				// other long options take no value
			case chmodMode.MatchString(w):
				slotClosed = true
			}
		case !slotClosed:
			slotClosed = true
			if !chmodMode.MatchString(w) {
				out = append(out, resolvePath(cwd, w))
			}
		default:
			out = append(out, resolvePath(cwd, w))
		}
	}
	return out
}

// commandTokens returns the segment tokens from the command word on, where
// skipWords is the number of leading word tokens (env assignments) before it.
// Unlike wordsOnly it keeps quoted string tokens in position.
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

// unquoteTok returns the text of a quoted string token without its quotes.
func unquoteTok(v string) string {
	v = strings.TrimPrefix(v, "$")
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// sedPathArgs extracts the files a sed invocation edits from its segment tokens
// (command word first). sed only writes its input files when run in place (-i,
// -i<suffix>, --in-place[=<suffix>], or a single-dash cluster containing i such
// as -ni); otherwise it reads them and prints to stdout, so it contributes no
// target. The script is never a target: it is the value of -e/-f/--expression/
// --file, or else the first positional. A quoted token is never an option but
// does occupy a positional or option-value slot, so `sed -i 's/a/b/' f` and
// `sed -i -e 's/a/b/' f` both yield f. Best-effort like the rest of the
// classifier: a `w <file>` command inside the script of a non-in-place run is
// not tracked.
func sedPathArgs(toks []bashTok, cwd string) []string {
	if len(toks) < 2 {
		return nil
	}
	inPlace, hasScript, skipNext, optsDone := false, false, false, false
	var positionals []string
	for _, t := range toks[1:] {
		if t.Kind == "string" && t.Value == "HEREDOC" {
			continue // stdin body, not an argument
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
			// Single-dash cluster, scanned left to right. i, e, f and l consume
			// the rest of the cluster (a suffix, a glued script or a number).
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
		positionals = positionals[1:] // the script
	}
	out := make([]string, 0, len(positionals))
	for _, p := range positionals {
		out = append(out, resolvePath(cwd, p))
	}
	return out
}

// redirectTargets finds >file, >>file, > file, and tee destinations.
//
// The redirect walk reads tokens, not bare words, so a quoted word in the target
// slot is seen: `> "/tmp/x.log"` names /tmp/x.log. Dropping string tokens (as
// wordsOnly does) lost that target and took the next bare word instead — for
// `> "f" 2>&1` the operator `2>` — which resolved inside the repo and made a
// scratch redirect look like a source edit. The target is unquoted with unquoteTok
// and resolved exactly as a mutation-verb argument is (commandArgWords), so one
// quoted path has one verdict in every slot.
//
// An operator (isRedirectOp) is never a target, and a heredoc token never is.
// Fd-duplication forms (n>&m, n>&-, >&m) contribute no file target — tokenizeBash
// splits on '&', so "2>&1" arrives as words "2>", "&", "1". An
// unresolvable target ($UNSET, $(cmd), a variable assigned earlier in the same
// command) resolves onto cwd and stays gated; nothing is treated as foreign.
func redirectTargets(seg []bashTok, cwd string) []string {
	var out []string
	// target is the file named by the token in a target slot: a bare word or a
	// quoted word unquoted. Operators, heredocs and empty words name none.
	target := func(t bashTok) (string, bool) {
		switch {
		case t.Kind == "word" && t.Value != "" && !isRedirectOp(t.Value):
			return t.Value, true
		case t.Kind == "string" && t.Value != "HEREDOC" && t.Raw == "":
			return unquoteTok(t.Value), true
		}
		return "", false
	}
	// tee [opts] FILE… — the one place that decides what tee writes, whether tee
	// is the command word or runs behind a wrapper (env, sudo, nohup, xargs,
	// stdbuf, …). Each destination goes through target, so a quoted FILE is the
	// same target as a bare one. A quoted word is never an option, and a redirect
	// after tee is left to the redirect walk below.
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
						j++ // the redirect's own target
					}
					continue
				}
			}
			if p, ok := target(a); ok {
				out = append(out, resolvePath(cwd, p))
			}
		}
	}
	for i := 0; i < len(seg); i++ {
		t := seg[i]
		if t.Kind != "word" || t.Value == "" {
			continue
		}
		w := t.Value
		// Glued: >file or >>file or 2>file
		if strings.Contains(w, ">") && !isRedirectOp(w) {
			if p := gluedRedirectPath(w); p != "" {
				out = append(out, resolvePath(cwd, p))
			}
		}
		// Spaced: > file / >> file / 2> file / 2> & 1 (fd dup) / > & file (csh)
		if !isRedirectOp(w) || i+1 >= len(seg) {
			continue
		}
		next := seg[i+1]
		if next.Kind == "word" && strings.HasPrefix(next.Value, ">") {
			continue // glued target, or a following operator: handled on its own turn
		}
		// After a redirect op, "&" is either n>&m (fd duplication) or csh
		// ">& file" / "> & file" (file target is the word after "&").
		if next.Kind == "word" && next.Value == "&" {
			if i+2 < len(seg) {
				if after := seg[i+2]; after.Kind == "word" && isFdDupTarget(after.Value) {
					i += 2 // skip "&" and the fd / "-"
					continue
				}
				if p, ok := target(seg[i+2]); ok {
					out = append(out, resolvePath(cwd, p))
					i += 2
					continue
				}
			}
			i++ // trailing bare "&" after redirect — no file
			continue
		}
		// One-token ">&" followed by fd or path (defensive; tokenizer usually
		// splits ">&m" at '&').
		if strings.HasSuffix(w, ">&") && next.Kind == "word" && isFdDupTarget(next.Value) {
			i++
			continue
		}
		if p, ok := target(next); ok {
			out = append(out, resolvePath(cwd, p))
			i++
		}
	}
	return out
}

// isRedirectOp reports whether w is a bare redirect operator with no glued
// target: >, >>, >|, >&, optionally prefixed by a file descriptor number or &
// (N>, N>>, &>, &>>). Such a word is never a target.
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

// isFdDupTarget reports whether s is a bash fd-duplication target: all digits
// (e.g. "1", "2"), "-" (close the fd: n>&-), or digits with a single trailing
// "-" (fd-move: n>&m-).
func isFdDupTarget(s string) bool {
	if s == "-" {
		return true
	}
	if s == "" {
		return false
	}
	// Fd-move form: "1-", "2-"
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

// gluedRedirectPath extracts the path from tokens like ">file", "2>>file", ">>/abs".
// Bare "&" and "&"-led fd remainders (">&1") are not file paths.
func gluedRedirectPath(w string) string {
	// Find last redirect operator sequence and take the remainder.
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
	// n>&m glued remainder after '>': "&1", "&-" — not a file.
	if strings.HasPrefix(rest, "&") && isFdDupTarget(rest[1:]) {
		return ""
	}
	return rest
}

// expandShellPath expands a leading ~, $NAME or the braced form of NAME from
// the hook's own environment. The word is replaced only when the expansion is
// an absolute path and the name is followed by nothing or a "/"; anything else
// (unset, relative, ~user, $(cmd), $NAMEsuffix) is returned unchanged, so it
// resolves under cwd and stays conservatively in-home. Nothing is executed or
// globbed.
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

// resolvePath makes a path absolute against cwd (no symlink resolution). A
// leading ~ or $VAR that the hook's environment expands to an absolute path is
// expanded first (expandShellPath).
func resolvePath(cwd, p string) string {
	p = expandShellPath(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(cwd, p))
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

// operatorRecordVerb is satelle's operator record verb, the operator's alone.
const operatorRecordVerb = "record"

// operatorRecordText finds satelle and a later record word on one line of
// quoted or heredoc text; the separators may be spaces, quotes, commas or
// brackets, so an argv list (['satelle','story','record']) matches too.
var operatorRecordText = regexp.MustCompile(`(?m)(?:^|[\s'"/,\[(])satelle(?:[\s'",\])][^\n]*?)?[\s'",\[(]` + operatorRecordVerb + `(?:$|[\s'",\])])`)

// operatorRecordSegment reports whether the segment would run satelle's
// operator record verb: satelle at any word position (so behind any wrapper in
// shellOpaqueWords, or after satelle's own flags) with a later record word not
// taken as a flag's value, or the verb named in the segment's quoted or
// heredoc text.
func operatorRecordSegment(sg bashSegment) bool {
	for i, w := range sg.Words {
		if filepath.Base(w) != "satelle" {
			continue
		}
		for j := i + 1; j < len(sg.Words); j++ {
			if sg.Words[j] == operatorRecordVerb && !strings.HasPrefix(sg.Words[j-1], "-") {
				return true
			}
		}
	}
	for _, t := range sg.Tokens {
		if t.Kind != "string" {
			continue
		}
		text := t.Value
		if t.Raw != "" {
			text = t.Raw
		}
		if operatorRecordText.MatchString(text) {
			return true
		}
	}
	return false
}
