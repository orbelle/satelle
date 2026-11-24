package remotedef

import (
	"strconv"
	"strings"
)

type SetupLine struct {
	Line    int
	Key     string
	Command string
}

var setupKeys = map[string]string{"setup.on_create": "on_create", "setup.post_start": "post_start"}

type tomlScan struct {
	s    string
	i    int
	line int
	key  string
	out  []SetupLine
}

func SetupLines(body []byte) []SetupLine {
	sc := &tomlScan{s: string(body), line: 1}
	table := ""
	for {
		sc.skipBlank(true)
		if sc.i >= len(sc.s) {
			return sc.out
		}
		if sc.s[sc.i] == '[' {
			end := strings.IndexByte(sc.s[sc.i:], '\n')
			if end < 0 {
				end = len(sc.s) - sc.i
			}
			table = tableName(sc.s[sc.i : sc.i+end])
			sc.i += end
			continue
		}
		k := sc.readKey()
		if sc.i >= len(sc.s) || sc.s[sc.i] != '=' {
			sc.skipLine()
			continue
		}
		sc.i++
		full := k
		if table != "" {
			full = table + "." + k
		}
		sc.key = setupKeys[full]
		sc.value()
		sc.key = ""
		sc.skipLine()
	}
}

func tableName(header string) string {
	h := strings.TrimLeft(header, "[")
	if j := strings.IndexByte(h, ']'); j >= 0 {
		h = h[:j]
	}
	return dottedKey(h)
}

func dottedKey(k string) string {
	parts := strings.Split(k, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, ".")
}

func (sc *tomlScan) skipBlank(newlines bool) {
	for sc.i < len(sc.s) {
		switch c := sc.s[sc.i]; {
		case c == ' ' || c == '\t' || c == '\r':
			sc.i++
		case c == '\n' && newlines:
			sc.i++
			sc.line++
		case c == '#':
			for sc.i < len(sc.s) && sc.s[sc.i] != '\n' {
				sc.i++
			}
		default:
			return
		}
	}
}

func (sc *tomlScan) skipLine() {
	for sc.i < len(sc.s) && sc.s[sc.i] != '\n' {
		sc.i++
	}
}

func (sc *tomlScan) readKey() string {
	start := sc.i
	for sc.i < len(sc.s) {
		c := sc.s[sc.i]
		if c == '=' || c == '\n' {
			break
		}
		if c == '"' || c == '\'' {
			sc.str()
			continue
		}
		sc.i++
	}
	return dottedKey(sc.s[start:sc.i])
}

func (sc *tomlScan) value() {
	sc.skipBlank(false)
	if sc.i >= len(sc.s) {
		return
	}
	switch sc.s[sc.i] {
	case '"', '\'':
		line := sc.line
		v := sc.str()
		if sc.key != "" {
			sc.out = append(sc.out, SetupLine{Line: line, Key: sc.key, Command: v})
		}
	case '[':
		sc.i++
		for {
			sc.skipBlank(true)
			if sc.i >= len(sc.s) {
				return
			}
			switch sc.s[sc.i] {
			case ']':
				sc.i++
				return
			case ',':
				sc.i++
			default:
				sc.value()
			}
		}
	case '{':
		sc.i++
		key := sc.key
		sc.key = ""
		for {
			sc.skipBlank(false)
			if sc.i >= len(sc.s) || sc.s[sc.i] == '\n' {
				break
			}
			if sc.s[sc.i] == '}' {
				sc.i++
				break
			}
			if sc.s[sc.i] == ',' {
				sc.i++
				continue
			}
			sc.readKey()
			if sc.i < len(sc.s) && sc.s[sc.i] == '=' {
				sc.i++
				sc.value()
			}
		}
		sc.key = key
	default:
		for sc.i < len(sc.s) && !strings.ContainsRune(",]}\n#", rune(sc.s[sc.i])) {
			sc.i++
		}
	}
}

func (sc *tomlScan) str() string {
	q := sc.s[sc.i]
	triple := strings.HasPrefix(sc.s[sc.i:], strings.Repeat(string(q), 3))
	if triple {
		sc.i += 3
		if strings.HasPrefix(sc.s[sc.i:], "\r\n") {
			sc.i += 2
			sc.line++
		} else if strings.HasPrefix(sc.s[sc.i:], "\n") {
			sc.i++
			sc.line++
		}
	} else {
		sc.i++
	}
	var b strings.Builder
	for sc.i < len(sc.s) {
		c := sc.s[sc.i]
		if c == q && (!triple || strings.HasPrefix(sc.s[sc.i:], strings.Repeat(string(q), 3))) {
			if triple {
				sc.i += 3
			} else {
				sc.i++
			}
			return b.String()
		}
		if c == '\n' {
			if !triple {
				return b.String()
			}
			sc.line++
		}
		if c == '\\' && q == '"' && sc.i+1 < len(sc.s) {
			sc.i += sc.escape(&b)
			continue
		}
		b.WriteByte(c)
		sc.i++
	}
	return b.String()
}

func (sc *tomlScan) escape(b *strings.Builder) int {
	switch e := sc.s[sc.i+1]; e {
	case '\n', '\r', ' ', '\t':
		j := sc.i + 1
		for j < len(sc.s) && strings.ContainsRune(" \t\r\n", rune(sc.s[j])) {
			if sc.s[j] == '\n' {
				sc.line++
			}
			j++
		}
		return j - sc.i
	case 'b':
		b.WriteByte('\b')
	case 't':
		b.WriteByte('\t')
	case 'n':
		b.WriteByte('\n')
	case 'f':
		b.WriteByte('\f')
	case 'r':
		b.WriteByte('\r')
	case '"', '\\':
		b.WriteByte(e)
	case 'u', 'U':
		n := 4
		if e == 'U' {
			n = 8
		}
		if sc.i+2+n <= len(sc.s) {
			if r, err := strconv.ParseUint(sc.s[sc.i+2:sc.i+2+n], 16, 32); err == nil {
				b.WriteRune(rune(r))
				return 2 + n
			}
		}
		b.WriteByte(e)
	default:
		b.WriteByte('\\')
		b.WriteByte(e)
	}
	return 2
}
