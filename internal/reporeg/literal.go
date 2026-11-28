package reporeg

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/shq"
)

func Literal(name string) string {
	if ValidName(name) && !HasUnsafe(name) {
		return name
	}
	return shq.Quote(escaped(name))
}

func PathLiteral(p string) string {
	if ShellSafePath(p) {
		return p
	}
	return shq.Quote(escaped(p))
}

func escaped(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case unsafeRune(r):
			q := strconv.QuoteRuneToASCII(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func Literals(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = Literal(n)
	}
	return strings.Join(out, ", ")
}
