package hook

import (
	"slices"
	"strings"
)

type PushDest struct {
	Branch string
	Unread string
}

var readPushesFault func()

const pushReaderFault = "an internal error in the push reader"

func readPushes(segs []plainSegment) (out []PushDest) {
	defer func() {
		if recover() != nil {
			out = []PushDest{{Unread: pushReaderFault}}
		}
	}()
	if readPushesFault != nil {
		readPushesFault()
	}
	invs, unread := pushInvocations(segs)
	if unread != "" {
		return []PushDest{{Unread: unread}}
	}
	for _, args := range invs {
		out = append(out, pushDests(args)...)
	}
	return out
}

func pushInvocations(segs []plainSegment) ([][]string, string) {
	var out [][]string
	for _, s := range segs {
		values := make([]string, len(s.Words))
		for i, w := range s.Words {
			values[i] = w.Value
		}
		sub, _, args, ok := gitInvocationArgs(values)
		if !ok {
			continue
		}
		if unread := pushGlobalsUnread(values[1 : len(values)-len(args)-1]); unread != "" {
			if slices.Contains(values[1:], "push") {
				return nil, unread
			}
			continue
		}
		if sub == "push" {
			out = append(out, args)
		}
	}
	return out, ""
}

func pushGlobalsUnread(globals []string) string {
	for i := 0; i < len(globals); i++ {
		switch w := globals[i]; {
		case w == "-C" && i+1 < len(globals):
			i++
		case gitPlainGlobals[w]:
		default:
			return "git option " + w + " before the subcommand is not read"
		}
	}
	return ""
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
