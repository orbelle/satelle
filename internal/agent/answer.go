package agent

import (
	"encoding/json"
	"strings"
)

// LastObject returns the last JSON object in text that keep accepts. Every
// '{' in text starts a candidate, so an object nested in another is a
// candidate of its own.
func LastObject(text string, keep func(json.RawMessage) bool) (json.RawMessage, bool) {
	return lastObject(text, "", keep)
}

// LastObjectLenient is LastObject that also reads a candidate whose only fault
// is an invalid escape (such as \. or \d) in the string value of member,
// directly inside the candidate's own root brace: the backslash of each such
// escape is doubled, so the value decodes to the characters as written. Any
// other fault, or an invalid escape anywhere else, leaves the candidate out.
// A candidate that parses as it stands is never rewritten.
func LastObjectLenient(text, member string, keep func(json.RawMessage) bool) (json.RawMessage, bool) {
	return lastObject(text, member, keep)
}

func lastObject(text, member string, keep func(json.RawMessage) bool) (json.RawMessage, bool) {
	var found json.RawMessage
	for i := strings.IndexByte(text, '{'); i >= 0; {
		var raw json.RawMessage
		if json.NewDecoder(strings.NewReader(text[i:])).Decode(&raw) == nil {
			if keep(raw) {
				found = raw
			}
		} else if member != "" {
			if fixed, ok := repairMember(text[i:], member); ok && json.Valid([]byte(fixed)) && keep(json.RawMessage(fixed)) {
				found = json.RawMessage(fixed)
			}
		}
		next := strings.IndexByte(text[i+1:], '{')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return found, found != nil
}

// repairMember reads the object that starts at s[0] ('{', the root) up to the
// brace that closes it, and doubles the backslash of every invalid escape in
// the string value of member directly inside the root. Every other byte is
// copied as it is. It reports whether any backslash was doubled; unterminated
// input reports false.
func repairMember(s, member string) (string, bool) {
	if s == "" || s[0] != '{' {
		return "", false
	}
	var b strings.Builder
	depth, repaired := 0, false
	expectValue, lastKey := false, ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '{', '[':
			depth++
			b.WriteByte(c)
		case '}', ']':
			depth--
			b.WriteByte(c)
			if depth == 0 {
				return b.String(), repaired
			}
		case ':':
			if depth == 1 {
				expectValue = true
			}
			b.WriteByte(c)
		case ',':
			if depth == 1 {
				expectValue = false
			}
			b.WriteByte(c)
		case '"':
			isValue := depth == 1 && expectValue
			target := isValue && lastKey == member
			start := i + 1
			b.WriteByte(c)
			i++
			closed := false
			for ; i < len(s); i++ {
				d := s[i]
				if d == '"' {
					b.WriteByte(d)
					closed = true
					break
				}
				if d != '\\' {
					b.WriteByte(d)
					continue
				}
				if i+1 >= len(s) {
					return "", false
				}
				if target && !validEscape(s[i+1:]) {
					b.WriteString(`\\`)
					repaired = true
					continue
				}
				b.WriteByte(d)
				b.WriteByte(s[i+1])
				i++
			}
			if !closed {
				return "", false
			}
			if depth == 1 {
				if isValue {
					expectValue = false
				} else {
					lastKey = s[start:i]
				}
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}

// validEscape reports whether rest, the bytes after a backslash inside a JSON
// string, starts a legal escape.
func validEscape(rest string) bool {
	switch rest[0] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	case 'u':
		if len(rest) < 5 {
			return false
		}
		for _, h := range rest[1:5] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", h) {
				return false
			}
		}
		return true
	}
	return false
}
