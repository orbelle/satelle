package agent

import (
	"encoding/json"
	"strings"
)

func LastObject(text string, keep func(json.RawMessage) bool) (json.RawMessage, bool) {
	return lastObject(text, "", keep)
}

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
