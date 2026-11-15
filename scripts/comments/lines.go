package main

import (
	"bytes"
	"strings"
)

type span struct{ start, end int }

func lineStart(src []byte, i int) int {
	return bytes.LastIndexByte(src[:i], '\n') + 1
}

func lineEnd(src []byte, i int) int {
	if j := bytes.IndexByte(src[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(src)
}

func blank(b []byte) bool {
	return len(bytes.TrimSpace(b)) == 0
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return s
}

func lineNumber(src []byte, i int) int {
	return bytes.Count(src[:i], []byte("\n")) + 1
}

func stripLines(src []byte, scan func([]byte) []span) ([]byte, []finding, error) {
	spans := scan(src)
	if len(spans) == 0 {
		return src, nil, nil
	}
	found := make([]finding, len(spans))
	for i, s := range spans {
		found[i] = finding{lineNumber(src, s.start), firstLine(string(src[s.start:s.end]))}
	}
	return removeHashSpans(src, spans), found, nil
}

func removeHashSpans(src []byte, spans []span) []byte {
	type cut struct {
		start, end int
		whole      bool
	}
	var cuts []cut
	for _, s := range spans {
		ls := lineStart(src, s.start)
		le := lineEnd(src, s.end)
		if blank(src[ls:s.start]) {
			cuts = append(cuts, cut{ls, min(le+1, len(src)), true})
			continue
		}
		st := s.start
		for st > ls && (src[st-1] == ' ' || src[st-1] == '\t') {
			st--
		}
		cuts = append(cuts, cut{st, le, false})
	}
	var merged []cut
	for _, c := range cuts {
		if n := len(merged); n > 0 && merged[n-1].whole && c.whole && merged[n-1].end == c.start {
			merged[n-1].end = c.end
			continue
		}
		merged = append(merged, c)
	}
	for i, c := range merged {
		if !c.whole {
			continue
		}
		pls := 0
		if c.start > 0 {
			pls = lineStart(src, c.start-1)
			if !blank(src[pls : c.start-1]) {
				continue
			}
		}
		if c.end >= len(src) {
			merged[i].start = pls
			continue
		}
		nle := lineEnd(src, c.end)
		if blank(src[c.end:nle]) && (i+1 >= len(merged) || merged[i+1].start > nle) {
			merged[i].end = min(nle+1, len(src))
		}
	}
	var out bytes.Buffer
	prev := 0
	for _, c := range merged {
		if c.start < prev {
			c.start = prev
		}
		out.Write(src[prev:c.start])
		prev = max(prev, c.end)
	}
	out.Write(src[prev:])
	return out.Bytes()
}

func hashLineComments(src []byte) []span {
	var spans []span
	for i := 0; i < len(src); {
		le := lineEnd(src, i)
		line := src[i:le]
		t := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(t, []byte("#")) {
			st := le - len(t)
			spans = append(spans, span{st, le})
		}
		i = le + 1
	}
	return spans
}

func tomlComments(src []byte) []span {
	var spans []span
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case bytes.HasPrefix(src[i:], []byte(`"""`)):
			i += 3
			for i < len(src) && !bytes.HasPrefix(src[i:], []byte(`"""`)) {
				if src[i] == '\\' {
					i++
				}
				i++
			}
			i += 3
			for i < len(src) && src[i] == '"' {
				i++
			}
		case bytes.HasPrefix(src[i:], []byte(`'''`)):
			i += 3
			for i < len(src) && !bytes.HasPrefix(src[i:], []byte(`'''`)) {
				i++
			}
			i += 3
			for i < len(src) && src[i] == '\'' {
				i++
			}
		case c == '"':
			i++
			for i < len(src) && src[i] != '"' && src[i] != '\n' {
				if src[i] == '\\' {
					i++
				}
				i++
			}
			i++
		case c == '\'':
			i++
			for i < len(src) && src[i] != '\'' && src[i] != '\n' {
				i++
			}
			i++
		case c == '#':
			le := lineEnd(src, i)
			spans = append(spans, span{i, le})
			i = le
		default:
			i++
		}
	}
	return spans
}

func makeComments(src []byte) []span {
	var spans []span
	inDefine := false
	for i := 0; i < len(src); {
		le := lineEnd(src, i)
		line := src[i:le]
		t := strings.TrimSpace(string(line))
		switch {
		case inDefine:
			if t == "endef" {
				inDefine = false
			}
		case strings.HasPrefix(t, "define ") || t == "define":
			inDefine = true
		case len(line) > 0 && line[0] == '\t':
			for _, s := range shellComments(line) {
				spans = append(spans, span{i + s.start, i + s.end})
			}
		default:
			depth := 0
			for j := 0; j < len(line); j++ {
				switch line[j] {
				case '\\':
					j++
				case '$':
					if j+1 < len(line) && (line[j+1] == '(' || line[j+1] == '{') {
						depth++
						j++
					}
				case '(', '{':
					if depth > 0 {
						depth++
					}
				case ')', '}':
					if depth > 0 {
						depth--
					}
				case '#':
					if depth == 0 {
						spans = append(spans, span{i + j, le})
						j = len(line)
					}
				}
			}
		}
		i = le + 1
	}
	return spans
}

func stripMarkdown(src []byte) ([]byte, []finding, error) {
	var out bytes.Buffer
	var found []finding
	lines := bytes.SplitAfter(src, []byte("\n"))
	lineNo := 0
	inHTML := false
	for idx := 0; idx < len(lines); idx++ {
		line := lines[idx]
		lineNo++
		if kept, had := proseHTML(line, &inHTML); had {
			found = append(found, finding{lineNo, firstLine(string(line))})
			if !blank(kept) || blank(line) {
				out.Write(kept)
			}
			continue
		}
		if indented(line) && (idx == 0 || blank(lines[idx-1])) {
			end := idx
			for end+1 < len(lines) && (indented(lines[end+1]) || blank(lines[end+1])) {
				end++
			}
			for end > idx && blank(lines[end]) {
				end--
			}
			var body bytes.Buffer
			var prefix []string
			for _, l := range lines[idx : end+1] {
				p, rest := splitIndent(l)
				prefix = append(prefix, p)
				body.Write(rest)
			}
			stripped, f, _ := stripLines(body.Bytes(), shellComments)
			for _, x := range f {
				found = append(found, finding{lineNo + x.line - 1, x.text})
			}
			for i, l := range bytes.SplitAfter(stripped, []byte("\n")) {
				if len(l) == 0 {
					continue
				}
				if i < len(prefix) && !blank(l) {
					out.WriteString(prefix[i])
				}
				out.Write(l)
			}
			lineNo += end - idx
			idx = end
			continue
		}
		out.Write(line)
		fence, lang, ok := openFence(line)
		if !ok {
			continue
		}
		startLine := lineNo
		var body bytes.Buffer
		closed := -1
		for j := idx + 1; j < len(lines); j++ {
			if closesFence(lines[j], fence) {
				closed = j
				break
			}
			body.Write(lines[j])
		}
		if closed < 0 {
			continue
		}
		block := body.Bytes()
		if shellLang(lang) {
			stripped, f, _ := stripLines(block, shellComments)
			for _, x := range f {
				found = append(found, finding{startLine + x.line, x.text})
			}
			block = stripped
		}
		out.Write(block)
		out.Write(lines[closed])
		lineNo += closed - idx
		idx = closed
	}
	return out.Bytes(), found, nil
}

func openFence(line []byte) (string, string, bool) {
	t := strings.TrimRight(string(line), "\r\n")
	indent := len(t) - len(strings.TrimLeft(t, " "))
	if indent > 3 {
		return "", "", false
	}
	t = t[indent:]
	for _, ch := range []string{"`", "~"} {
		n := 0
		for n < len(t) && t[n] == ch[0] {
			n++
		}
		if n >= 3 {
			info := strings.TrimSpace(t[n:])
			if ch == "`" && strings.Contains(info, "`") {
				return "", "", false
			}
			lang, _, _ := strings.Cut(info, " ")
			return t[:n], lang, true
		}
	}
	return "", "", false
}

func closesFence(line []byte, fence string) bool {
	t := strings.TrimSpace(string(line))
	if !strings.HasPrefix(t, fence) {
		return false
	}
	return strings.Trim(t, fence[:1]) == ""
}

func shellLang(lang string) bool {
	switch strings.ToLower(lang) {
	case "sh", "bash", "shell", "zsh", "check":
		return true
	}
	return false
}

func indented(line []byte) bool {
	return !blank(line) && (bytes.HasPrefix(line, []byte("    ")) || bytes.HasPrefix(line, []byte("\t")))
}

func splitIndent(line []byte) (string, []byte) {
	switch {
	case bytes.HasPrefix(line, []byte("\t")):
		return "\t", line[1:]
	case bytes.HasPrefix(line, []byte("    ")):
		return "    ", line[4:]
	}
	return "", line
}

func proseHTML(line []byte, inHTML *bool) ([]byte, bool) {
	if !*inHTML && !bytes.Contains(line, []byte("<!--")) {
		return line, false
	}
	var out bytes.Buffer
	had := *inHTML
	code := false
	for i := 0; i < len(line); {
		switch {
		case *inHTML:
			if bytes.HasPrefix(line[i:], []byte("-->")) {
				*inHTML = false
				i += 3
				continue
			}
			if line[i] == '\n' {
				out.WriteByte('\n')
			}
			i++
		case line[i] == '`':
			code = !code
			out.WriteByte(line[i])
			i++
		case !code && bytes.HasPrefix(line[i:], []byte("<!--")):
			*inHTML, had = true, true
			i += 4
		default:
			out.WriteByte(line[i])
			i++
		}
	}
	if !had {
		return line, false
	}
	kept := bytes.TrimRight(out.Bytes(), " \t\n")
	if len(kept) == 0 {
		return nil, true
	}
	return append(kept, '\n'), true
}
