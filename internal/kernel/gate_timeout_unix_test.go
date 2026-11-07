//go:build !windows

package kernel

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
)

// A check gate cut at its own timeout rejects the move, and the gate.run on
// the ledger names the bound and where it came from.
func TestGateTimeoutRecordedOnLedger(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"flow\"\n")
	write(t, filepath.Join(root, "skills", "slow.md"), "---\ntype: check\n---\n```sh\nsleep 30\n```\n")
	write(t, filepath.Join(root, "workflows", "flow.toml"), `
[states.a]
start = true
[states.z]
terminal = true

[gates.slow]
skill = "slow"
timeout = "200ms"

[[transition]]
from = ["a"]
to = "z"
groups = [{ gates = ["slow"] }]
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	k.Git = fakeGit{}
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := k.Transition(context.Background(), it.ID, "z", "me")
	if err != nil || res.Moved || gates(res) != "slow=reject" || time.Since(start) > 5*time.Second {
		t.Fatalf("transition: %v %s %v", err, gates(res), time.Since(start))
	}
	if got, _ := k.Get(it.ID); got.Status != "a" {
		t.Fatalf("item moved to %q", got.Status)
	}
	recorded, _ := k.Cache.Events(it.ID, gate.KindRun)
	var r gate.Result
	if len(recorded) != 1 || recorded[0].Decode(&r) != nil {
		t.Fatalf("gate.run events: %d", len(recorded))
	}
	if r.Result != gate.Reject || !strings.HasPrefix(r.Notes, "timed out after 200ms (gates.slow timeout)") {
		t.Fatalf("recorded %+v", r)
	}
}
