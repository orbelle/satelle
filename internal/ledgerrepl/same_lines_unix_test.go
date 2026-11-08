//go:build !windows

package ledgerrepl

import (
	"regexp"
	"testing"
)

var hex12 = regexp.MustCompile(`\b[0-9a-f]{12}\b`)

// The diverged and refused lines are the same for satelle.dev and for a
// store command (the reference adapter): one Replicator prints them.
func TestSameLinesBothStores(t *testing.T) {
	scenario := func(t *testing.T, open func() Store) (pushOut, pullOut string) {
		a, b := newStore(t), newStore(t)
		add(t, a, "it", "s", 2)
		pushOn(t, a, open())
		copyChain(t, a, b, "it")
		add(t, a, "it", "a", 1)
		pushOn(t, a, open())
		add(t, b, "it", "b", 1)
		res, pushOut := pushOn(t, b, open())
		if !res.Failed() {
			t.Fatalf("push did not diverge:\n%s", pushOut)
		}
		res, pullOut = pullOn(t, b, open())
		if !res.Failed() {
			t.Fatalf("pull did not diverge:\n%s", pullOut)
		}
		res, refused := pullOn(t, b, open(), "nope")
		if !res.Failed() {
			t.Fatalf("pull of an unknown item was not refused:\n%s", refused)
		}
		return hex12.ReplaceAllString(pushOut, "<h>"), hex12.ReplaceAllString(pullOut+refused, "<h>")
	}
	o := fsObjects(t)
	cmdPush, cmdPull := scenario(t, func() Store { return &ObjectStore{Objects: o, Name: "sh"} })
	w := newWorld(t)
	devPush, devPull := scenario(t, func() Store { return w.dev() })
	if cmdPush != devPush {
		t.Errorf("push lines differ\ncommand:\n%s\nsatelle.dev:\n%s", cmdPush, devPush)
	}
	if cmdPull != devPull {
		t.Errorf("pull lines differ\ncommand:\n%s\nsatelle.dev:\n%s", cmdPull, devPull)
	}
	if cmdPush == "" || cmdPull == "" {
		t.Fatal("no lines to compare")
	}
	t.Logf("push:\n%spull:\n%s", cmdPush, cmdPull)
}
