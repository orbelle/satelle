package main

import (
	"bytes"
	"strings"
)

type heredoc struct {
	delim     string
	stripTabs bool
}

type shellLexer struct {
	src     []byte
	pos     int
	spans   []span
	pending []heredoc
}

func shellComments(src []byte) []span {
	l := &shellLexer{src: src}
	l.command(0)
	return l.spans
}

func keptShellComment(src []byte, start, end int) bool {
	text := string(src[start:end])
	if start == 0 && strings.HasPrefix(text, "#!") {
		return true
	}
	t := strings.TrimSpace(strings.TrimPrefix(text, "#"))
	return strings.HasPrefix(t, "shellcheck ")
}

func (l *shellLexer) peek(off int) byte {
	if l.pos+off < len(l.src) {
		return l.src[l.pos+off]
	}
	return 0
}

func (l *shellLexer) command(term byte) {
	wordStart := true
	depth := 0
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '\n':
			l.pos++
			l.heredocBodies()
			wordStart = true
		case ' ', '\t', ';', '&', '|':
			l.pos++
			wordStart = true
		case '(':
			depth++
			l.pos++
			wordStart = true
		case ')':
			l.pos++
			if term == ')' && depth == 0 {
				return
			}
			if depth > 0 {
				depth--
			}
			wordStart = true
		case '`':
			l.pos++
			if term == '`' {
				return
			}
			l.command('`')
			wordStart = false
		case '\'':
			l.single()
			wordStart = false
		case '"':
			l.double()
			wordStart = false
		case '\\':
			l.pos += 2
			wordStart = false
		case '$':
			l.dollar(false)
			wordStart = false
		case '#':
			if wordStart {
				end := lineEnd(l.src, l.pos)
				if !keptShellComment(l.src, l.pos, end) {
					l.spans = append(l.spans, span{l.pos, end})
				}
				l.pos = end
				continue
			}
			l.pos++
		case '<':
			switch {
			case l.peek(1) == '<' && l.peek(2) == '<':
				l.pos += 3
			case l.peek(1) == '<':
				l.pos += 2
				l.heredocStart()
			case l.peek(1) == '(':
				l.pos += 2
				l.command(')')
			default:
				l.pos++
			}
			wordStart = false
		case '>':
			if l.peek(1) == '(' {
				l.pos += 2
				l.command(')')
			} else {
				l.pos++
			}
			wordStart = false
		default:
			l.pos++
			wordStart = false
		}
	}
}

func (l *shellLexer) single() {
	l.pos++
	for l.pos < len(l.src) && l.src[l.pos] != '\'' {
		l.pos++
	}
	l.pos++
}

func (l *shellLexer) double() {
	l.pos++
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\\':
			l.pos += 2
		case '"':
			l.pos++
			return
		case '$':
			l.dollar(true)
		case '`':
			l.pos++
			l.command('`')
		default:
			l.pos++
		}
	}
}

func (l *shellLexer) dollar(inDouble bool) {
	switch l.peek(1) {
	case '(':
		if l.peek(2) == '(' {
			l.pos += 3
			l.arith()
			return
		}
		l.pos += 2
		l.command(')')
	case '{':
		l.pos += 2
		l.brace(inDouble)
	case '\'':
		if inDouble {
			l.pos++
			return
		}
		l.pos += 2
		for l.pos < len(l.src) && l.src[l.pos] != '\'' {
			if l.src[l.pos] == '\\' {
				l.pos++
			}
			l.pos++
		}
		l.pos++
	default:
		l.pos++
	}
}

func (l *shellLexer) arith() {
	depth := 0
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '(':
			depth++
		case ')':
			if depth == 0 && l.peek(1) == ')' {
				l.pos += 2
				return
			}
			if depth > 0 {
				depth--
			}
		case '$':
			l.dollar(false)
			continue
		}
		l.pos++
	}
}

func (l *shellLexer) brace(inDouble bool) {
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '}':
			l.pos++
			return
		case '\\':
			l.pos += 2
		case '\'':
			if inDouble {
				l.pos++
			} else {
				l.single()
			}
		case '"':
			l.double()
		case '$':
			l.dollar(inDouble)
		case '`':
			l.pos++
			l.command('`')
		default:
			l.pos++
		}
	}
}

func (l *shellLexer) heredocStart() {
	h := heredoc{}
	if l.peek(0) == '-' {
		h.stripTabs = true
		l.pos++
	}
	for l.pos < len(l.src) && (l.src[l.pos] == ' ' || l.src[l.pos] == '\t') {
		l.pos++
	}
	var delim strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if bytes.IndexByte([]byte(" \t\n;&|<>()"), c) >= 0 {
			break
		}
		switch c {
		case '\'', '"':
			l.pos++
			for l.pos < len(l.src) && l.src[l.pos] != c {
				delim.WriteByte(l.src[l.pos])
				l.pos++
			}
			l.pos++
		case '\\':
			l.pos++
			if l.pos < len(l.src) {
				delim.WriteByte(l.src[l.pos])
			}
			l.pos++
		default:
			delim.WriteByte(c)
			l.pos++
		}
	}
	if delim.Len() > 0 {
		h.delim = delim.String()
		l.pending = append(l.pending, h)
	}
}

func (l *shellLexer) heredocBodies() {
	for len(l.pending) > 0 {
		h := l.pending[0]
		l.pending = l.pending[1:]
		for l.pos < len(l.src) {
			le := lineEnd(l.src, l.pos)
			line := string(l.src[l.pos:le])
			l.pos = min(le+1, len(l.src))
			if h.stripTabs {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				break
			}
		}
	}
}
