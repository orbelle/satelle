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
	var b strings.Builder
	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, `\x%02x`, name[i])
		case unsafeRune(r):
			q := strconv.QuoteRuneToASCII(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteString(name[i : i+size])
		}
		i += size
	}
	return shq.Quote(b.String())
}

func Literals(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = Literal(n)
	}
	return strings.Join(out, ", ")
}
