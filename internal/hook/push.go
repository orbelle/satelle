package hook

import (
	"path/filepath"
	"regexp"
	"strings"
)

type PushDest struct {
	Branch     string
	Unread     string
	FailClosed bool
	First      string
}

var readPushesFault func()

const pushReaderFault = "an internal error in the push reader"

func plainLine(command string) (bool, string) {
	n := len(command)
	for i := 0; i < n; {
		c := command[i]
		switch {
		case plainByte(c):
			i++
		case c == '\'':
			j := strings.IndexByte(command[i+1:], '\'')
			if j < 0 {
				return false, "an unterminated ' quote"
			}
			i += j + 2
		case c == '"':
			j := i + 1
			for ; j < n && command[j] != '"'; j++ {
				if b := command[j]; b == '$' || b == '`' || b == '\\' || b == '!' {
					return false, unreadRun(command, j)
				}
			}
			if j >= n {
				return false, `an unterminated " quote`
			}
			i = j + 1
		case (c == '&' || c == '|') && i+1 < n && command[i+1] == c:
			i += 2
		case c == ';' && !(i+1 < n && (command[i+1] == ';' || command[i+1] == '&')):
			i++
		case c == '|' && !(i+1 < n && command[i+1] == '&'):
			i++
		default:
			return false, unreadRun(command, i)
		}
	}
	return true, ""
}

func plainByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte(" \t_-./:,+=@%", c) >= 0
}

func unreadRun(s string, i int) string {
	switch c := s[i]; {
	case c == '\n':
		return "a newline"
	case c == '\r':
		return "a carriage return"
	case c >= 0x80:
		return "a non-ASCII byte"
	case c < 0x20 || c == 0x7f:
		return "a control byte"
	}
	j := i + 1
	for j < len(s) && j-i < 32 && s[j] > ' ' && s[j] < 0x7f {
		j++
	}
	return "`" + s[i:j] + "`"
}

var (
	gitWord  = regexp.MustCompile(`\bgit\b`)
	pushWord = regexp.MustCompile(`\bpush\b`)
)

func namesGitPush(command string) bool {
	s := strings.ReplaceAll(command, "\\\n", "")
	s = strings.NewReplacer("\\", "", "'", "", `"`, "").Replace(s)
	s = strings.ToLower(s)
	return gitWord.MatchString(s) && pushWord.MatchString(s)
}

type plainWord struct {
	Text   string
	Quoted bool
	Lead   string
}

func plainWords(seg []bashTok) []plainWord {
	var out []plainWord
	var cur *plainWord
	leadOpen := false
	for i, t := range seg {
		if i == 0 || seg[i-1].End != t.Start {
			out = append(out, plainWord{})
			cur = &out[len(out)-1]
			leadOpen = true
		}
		if t.Kind == "string" {
			cur.Text += unquoteTok(t.Value)
			cur.Quoted = true
			leadOpen = false
			continue
		}
		cur.Text += t.Value
		if leadOpen {
			cur.Lead += t.Value
		}
	}
	return out
}

func pushReservedWord(t string) bool {
	return shellCompoundWords[t] || t == "time"
}

func failClosed(first string) []PushDest {
	return []PushDest{{FailClosed: true, First: first}}
}

func readPushes(command string) (out []PushDest) {
	defer func() {
		if recover() != nil {
			out = failClosed(pushReaderFault)
		}
	}()
	if readPushesFault != nil {
		readPushesFault()
	}
	args, refused := pushSegments(command)
	if refused != nil {
		return []PushDest{*refused}
	}
	for _, a := range args {
		out = append(out, pushDests(a)...)
	}
	return out
}

func pushSegments(command string) ([][]string, *PushDest) {
	if !namesGitPush(command) {
		return nil, nil
	}
	if ok, first := plainLine(command); !ok {
		return nil, &failClosed(first)[0]
	}
	type segment struct {
		texts []string
		n     int
	}
	var segs []segment
	for _, ps := range splitSegments(tokenizeBash(command)) {
		words := plainWords(ps.Toks)
		leads := make([]string, len(words))
		texts := make([]string, len(words))
		for i, w := range words {
			leads[i], texts[i] = w.Lead, w.Text
		}
		n := leadingAssignments(leads)
		if n >= len(words) {
			continue
		}
		if d := segmentUnread(words, texts, n); d != nil {
			return nil, d
		}
		segs = append(segs, segment{texts, n})
	}
	var out [][]string
	for _, s := range segs {
		if filepath.Base(s.texts[s.n]) != "git" {
			continue
		}
		if sub, _, args, ok := gitInvocationArgs(s.texts[s.n:]); ok && sub == "push" {
			out = append(out, args)
		}
	}
	return out, nil
}

func segmentUnread(words []plainWord, texts []string, n int) *PushDest {
	for _, w := range []plainWord{words[0], words[n]} {
		if !w.Quoted && pushReservedWord(w.Text) {
			return &failClosed("`" + w.Text + "`")[0]
		}
	}
	cmd := texts[n]
	if strings.Contains(cmd, "=") {
		return &failClosed("`" + cmd + "`")[0]
	}
	if filepath.Base(cmd) != "git" {
		for i := n + 1; i < len(texts); i++ {
			if filepath.Base(texts[i]) != "git" {
				continue
			}
			for _, w := range texts[i+1:] {
				if w == "push" {
					return &failClosed("`" + cmd + "`")[0]
				}
			}
		}
		return nil
	}
	_, _, args, ok := gitInvocationArgs(texts[n:])
	if !ok {
		return nil
	}
	globals := texts[n+1 : len(texts)-len(args)-1]
	for i := 0; i < len(globals); i++ {
		switch w := globals[i]; {
		case w == "-C" && i+1 < len(globals):
			i++
		case gitPlainGlobals[w]:
		default:
			return &PushDest{Unread: "git option " + w + " before the subcommand is not read"}
		}
	}
	return nil
}

var gitPlainGlobals = map[string]bool{
	"-p": true, "--paginate": true, "--no-pager": true, "--bare": true,
	"--literal-pathspecs": true, "--glob-pathspecs": true, "--noglob-pathspecs": true,
	"--icase-pathspecs": true, "--no-optional-locks": true, "--no-replace-objects": true,
}

var pushPlainOptions = map[string]bool{
	"-u": true, "--set-upstream": true, "-q": true, "--quiet": true, "-v": true, "--verbose": true,
	"-d": true, "--delete": true, "-f": true, "--force": true, "--force-with-lease": true,
	"--force-if-includes": true, "--follow-tags": true, "--atomic": true, "--porcelain": true,
	"-n": true, "--dry-run": true, "--no-verify": true,
}

const pushNotWritten = "its destination is not written"

func pushPositionals(args []string) (pos []string, del bool, unread string) {
	optsDone := false
	for _, w := range args {
		switch {
		case optsDone:
			pos = append(pos, w)
		case w == "--":
			optsDone = true
		case strings.HasPrefix(w, "-"):
			if !pushPlainOptions[w] && !strings.HasPrefix(w, "--force-with-lease=") {
				return nil, false, "git push option " + w + " is not read"
			}
			del = del || w == "-d" || w == "--delete"
		default:
			pos = append(pos, w)
		}
	}
	return pos, del, ""
}

func pushDests(args []string) []PushDest {
	pos, del, unread := pushPositionals(args)
	if unread != "" {
		return []PushDest{{Unread: unread}}
	}
	if len(pos) < 2 {
		return []PushDest{{Unread: pushNotWritten}}
	}
	var out []PushDest
	for _, ref := range pos[1:] {
		out = append(out, refspecDest(ref, del))
	}
	return out
}

func refspecDest(ref string, del bool) PushDest {
	if strings.Contains(ref, "*") {
		return PushDest{Unread: ref + " is a pattern refspec"}
	}
	dst := ref
	if !del {
		spec := strings.TrimPrefix(ref, "+")
		switch strings.Count(spec, ":") {
		case 0:
			dst = spec
		case 1:
			_, dst, _ = strings.Cut(spec, ":")
		default:
			return PushDest{Unread: "refspec " + ref + " is not read"}
		}
	}
	switch {
	case dst == "" || dst == "HEAD" || dst == "@":
		return PushDest{Unread: pushNotWritten}
	case strings.HasPrefix(dst, "refs/heads/") && len(dst) > len("refs/heads/"):
		return PushDest{Branch: strings.TrimPrefix(dst, "refs/heads/")}
	case strings.HasPrefix(dst, "heads/") && len(dst) > len("heads/"):
		return PushDest{Branch: strings.TrimPrefix(dst, "heads/")}
	case strings.HasPrefix(dst, "refs/"):
		return PushDest{Unread: "destination " + dst + " is not a branch"}
	}
	return PushDest{Branch: dst}
}
