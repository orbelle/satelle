package ledger

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

// chainOf builds n linked events on item, each payload tagged.
func chainOf(t *testing.T, item string, tags ...string) []Event {
	t.Helper()
	var out []Event
	prev := ""
	for i, tag := range tags {
		e, err := NewEventWith(fmt.Sprintf("e%d%s", i, tag), fixedTime, item, "k", "me", map[string]string{"t": tag}, prev)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
		prev = e.Hash
	}
	return out
}

func linear(a, b []Event) int {
	n := 0
	for n < len(a) && n < len(b) && a[n].Hash == b[n].Hash {
		n++
	}
	return n
}

func TestSharedPrefix(t *testing.T) {
	cases := map[string]struct{ local, other []string }{
		"equal":              {[]string{"a", "b", "c"}, []string{"a", "b", "c"}},
		"other a prefix":     {[]string{"a", "b", "c", "d"}, []string{"a", "b"}},
		"local a prefix":     {[]string{"a", "b"}, []string{"a", "b", "c", "d"}},
		"fork at 0":          {[]string{"a", "b", "c"}, []string{"x", "y", "z"}},
		"fork in the middle": {[]string{"a", "b", "c", "d", "e", "f"}, []string{"a", "b", "x", "y", "z", "w", "v"}},
		"fork at the last":   {[]string{"a", "b", "c", "d"}, []string{"a", "b", "c", "x"}},
		"fork, other short":  {[]string{"a", "b", "c", "d", "e", "f"}, []string{"a", "x", "y", "z"}},
		"empty local":        {nil, []string{"a"}},
		"empty other":        {[]string{"a"}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			local, other := chainOf(t, "it", c.local...), chainOf(t, "it", c.other...)
			calls := 0
			got, err := SharedPrefix(local, len(other), func(seq int) (string, error) {
				calls++
				return other[seq].Hash, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if want := linear(local, other); got != want {
				t.Errorf("SharedPrefix = %d, linear scan %d", got, want)
			}
			if m := min(len(local), len(other)); calls > 1+bitsLen(m) {
				t.Errorf("%d hash lookups for %d shared positions", calls, m)
			}
		})
	}
}

func bitsLen(n int) int {
	b := 0
	for ; n > 0; n >>= 1 {
		b++
	}
	return b
}

func TestSharedPrefixReturnsTheLookupError(t *testing.T) {
	local := chainOf(t, "it", "a", "b")
	if _, err := SharedPrefix(local, 2, func(int) (string, error) { return "", os.ErrClosed }); err != os.ErrClosed {
		t.Fatalf("err %v", err)
	}
}

func TestEncodeIsTheChainLine(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Append("it", "k", "me", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	line, err := os.ReadFile(s.Path("it"))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := Encode(e)
	if err != nil || !bytes.Equal(append(enc, '\n'), line) {
		t.Fatalf("Encode %s, line %s (%v)", enc, line, err)
	}
}
