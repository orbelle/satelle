package ledgerrepl

import (
	"bytes"
	"testing"
)

// A pull, of every item or of the one item, whose local chain is ahead of a
// store chain that is a pure prefix of it records no conflict and imports
// nothing; the next push writes the rest.
func TestPullOfAPurePrefixOnEveryStore(t *testing.T) {
	forEach(t, func(t *testing.T, open func() Store) {
		a := newStore(t)
		add(t, a, "it", "a", 3)
		pushOn(t, a, open())
		add(t, a, "it", "b", 2)
		before := chainFile(t, a, "it")
		for _, items := range [][]string{nil, {"it"}} {
			res, out := pullOn(t, a, open(), items...)
			if res.Failed() || res.Events != 0 || res.Items != 0 {
				t.Fatalf("pull %v: %+v\n%s", items, res, out)
			}
			if !bytes.Equal(before, chainFile(t, a, "it")) {
				t.Fatal("the local chain changed")
			}
		}
		if res, out := pushOn(t, a, open()); res.Events != 2 || res.Failed() {
			t.Fatalf("the next push %+v\n%s", res, out)
		}
		if got := held(t, open(), "it"); len(got) != 5 {
			t.Fatalf("store holds %d events", len(got))
		}
	})
}
