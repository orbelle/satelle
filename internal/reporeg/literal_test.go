package reporeg

import (
	"strings"
	"testing"
)

func shellWord(t *testing.T, s string) string {
	t.Helper()
	var out strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				t.Fatalf("%q: unterminated single quote", s)
			}
			out.WriteString(s[i+1 : i+1+end])
			i += end + 2
		case strings.HasPrefix(s[i:], `\'`):
			out.WriteByte('\'')
			i += 2
		default:
			t.Fatalf("%q: unquoted byte %q at %d", s, s[i], i)
		}
	}
	return out.String()
}

func TestLiteral(t *testing.T) {
	cases := []struct {
		in, want  string
		printable bool
	}{
		{"web", "web", true},
		{"", "''", true},
		{"$(curl x|sh)", "'$(curl x|sh)'", true},
		{"${HOME}", "'${HOME}'", true},
		{"`id`", "'`id`'", true},
		{"it's", `'it'\''s'`, true},
		{"-rf", "'-rf'", true},
		{"my repo", "'my repo'", true},
		{"web; curl a|sh", "'web; curl a|sh'", true},
		{"x\x1b[31m", `'x\x1b[31m'`, false},
		{"\u202e", `'\u202e'`, false},
		{"a\u2028b", `'a\u2028b'`, false},
		{"\xff", `'\xff'`, false},
	}
	for _, c := range cases {
		got := Literal(c.in)
		if got != c.want {
			t.Errorf("Literal(%q) = %q, want %q", c.in, got, c.want)
		}
		if got == c.in {
			continue
		}
		if HasUnsafe(got) {
			t.Errorf("Literal(%q) = %q holds an unsafe rune", c.in, got)
		}
		dec := shellWord(t, got)
		if c.printable && dec != c.in {
			t.Errorf("Literal(%q) decodes to %q", c.in, dec)
		}
	}
}

func TestLiterals(t *testing.T) {
	if got := Literals([]string{"self", "web", "a b"}); got != "self, web, 'a b'" {
		t.Errorf("Literals = %q", got)
	}
}

func TestPathLiteral(t *testing.T) {
	cases := map[string]string{
		"/tmp/a-b_c.d/e+f@g": "/tmp/a-b_c.d/e+f@g",
		"/tmp/$(id)":         "'/tmp/$(id)'",
		"/tmp/a b":           "'/tmp/a b'",
		"/tmp/\x1b[31m":      `'/tmp/\x1b[31m'`,
		"/tmp/it's":          `'/tmp/it'\''s'`,
	}
	for in, want := range cases {
		if got := PathLiteral(in); got != want {
			t.Errorf("PathLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}
