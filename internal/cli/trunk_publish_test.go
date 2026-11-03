package cli

import (
	"strings"
	"testing"
)

// `satelle trunk publish` through the real command (sty_6af229f1): the repo
// under test is the subject clone of a local bare remote, another clone plays
// the machine that pushed first, and the proof is the repo's [trunk] prove.

func TestTrunkPublishFailsNamedWhenNothingProvesTheHead(t *testing.T) {
	repo, r, id := trunkEngageRepo(t, "")
	r.LinearSlice(t)
	before := r.RemoteHead(t)

	out, err := runRoot(t, "trunk", "publish", "--story", id)
	if err == nil || !strings.Contains(err.Error(), "no configured proof") {
		t.Fatalf("publish error = %v, want one naming the missing proof\n%s", err, out)
	}
	if r.RemoteHead(t) != before {
		t.Error("the remote moved though no proof was configured")
	}
	if got := r.Head(t, repo); got == before {
		t.Error("the release commit vanished from the subject")
	}
	led, lerr := runRoot(t, "ledger", "list", "--story", id)
	if lerr != nil || !strings.Contains(led, "trunk_publish") {
		t.Errorf("a failed publish writes no ledger row: %v\n%s", lerr, led)
	}
}
