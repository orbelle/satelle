package main

import (
	"errors"
	"fmt"
)

var jsRegexKeywords = map[string]bool{
	"return": true, "typeof": true, "instanceof": true, "case": true, "in": true, "of": true,
	"new": true, "delete": true, "void": true, "throw": true, "else": true, "do": true, "yield": true, "await": true,
}

func stripWeb(src []byte, scan func([]byte) ([]span, error)) ([]byte, []finding, error) {
	spans, err := scan(src)
	if err != nil {
		return nil, nil, err
	}
	if len(spans) == 0 {
		return src, nil, nil
	}
	found := make([]finding, len(spans))
	for i, s := range spans {
		found[i] = finding{lineNumber(src, s.start), firstLine(string(src[s.start:s.end]))}
	}
	out := removeGoSpans(src, spans)
	again, err := scan(out)
	if err != nil {
		return nil, nil, fmt.Errorf("stripped source does not scan: %w", err)
	}
	if len(again) != 0 {
		return nil, nil, errors.New("stripped source still holds a comment")
	}
	return out, found, nil
}

func cssComments(src []byte) ([]span, error) {
	var spans []span
	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '"' || c == '\'':
			end, err := quoted(src, i)
			if err != nil {
				return nil, err
			}
			i = end
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end, err := blockEnd(src, i)
			if err != nil {
				return nil, err
			}
			spans = append(spans, span{i, end})
			i = end
		default:
			i++
		}
	}
	return spans, nil
}

func jsComments(src []byte) ([]span, error) {
	var spans []span
	var braces []bool
	regexOK := true
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			end := lineEnd(src, i)
			spans = append(spans, span{i, end})
			i = end
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end, err := blockEnd(src, i)
			if err != nil {
				return nil, err
			}
			spans = append(spans, span{i, end})
			i = end
		case c == '/' && regexOK:
			end, err := regexEnd(src, i)
			if err != nil {
				return nil, err
			}
			i = end
			regexOK = false
		case c == '"' || c == '\'':
			end, err := quoted(src, i)
			if err != nil {
				return nil, err
			}
			i = end
			regexOK = false
		case c == '`':
			end, err := templateEnd(src, i+1)
			if err != nil {
				return nil, err
			}
			if end < 0 {
				braces = append(braces, true)
				i = -end
				regexOK = true
				continue
			}
			i = end
			regexOK = false
		case c == '{':
			braces = append(braces, false)
			i++
			regexOK = true
		case c == '}':
			n := len(braces)
			if n > 0 && braces[n-1] {
				braces = braces[:n-1]
				end, err := templateEnd(src, i+1)
				if err != nil {
					return nil, err
				}
				if end < 0 {
					braces = append(braces, true)
					i = -end
					regexOK = true
					continue
				}
				i = end
				regexOK = false
				continue
			}
			if n > 0 {
				braces = braces[:n-1]
			}
			i++
			regexOK = false
		case isIdentByte(c):
			j := i
			for j < len(src) && isIdentByte(src[j]) {
				j++
			}
			regexOK = jsRegexKeywords[string(src[i:j])]
			i = j
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == ')' || c == ']':
			i++
			regexOK = false
		case (c == '+' || c == '-') && i+1 < len(src) && src[i+1] == c:
			i += 2
		default:
			i++
			regexOK = true
		}
	}
	return spans, nil
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func quoted(src []byte, i int) (int, error) {
	q := src[i]
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case q:
			return j + 1, nil
		case '\n':
			return 0, fmt.Errorf("line %d: unterminated string", lineNumber(src, i))
		}
	}
	return 0, fmt.Errorf("line %d: unterminated string", lineNumber(src, i))
}

func blockEnd(src []byte, i int) (int, error) {
	for j := i + 2; j+1 < len(src); j++ {
		if src[j] == '*' && src[j+1] == '/' {
			return j + 2, nil
		}
	}
	return 0, fmt.Errorf("line %d: unterminated block comment", lineNumber(src, i))
}

func regexEnd(src []byte, i int) (int, error) {
	class := false
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '[':
			class = true
		case ']':
			class = false
		case '/':
			if !class {
				j++
				for j < len(src) && isIdentByte(src[j]) {
					j++
				}
				return j, nil
			}
		case '\n':
			return 0, fmt.Errorf("line %d: unterminated regular expression", lineNumber(src, i))
		}
	}
	return 0, fmt.Errorf("line %d: unterminated regular expression", lineNumber(src, i))
}

func templateEnd(src []byte, j int) (int, error) {
	start := j
	for ; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '`':
			return j + 1, nil
		case '$':
			if j+1 < len(src) && src[j+1] == '{' {
				return -(j + 2), nil
			}
		}
	}
	return 0, fmt.Errorf("line %d: unterminated template literal", lineNumber(src, start))
}
