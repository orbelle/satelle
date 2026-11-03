//go:build clislow

package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gatehandle"
)

// sty_9f3e51d1 AC6: in an agent session the engage runs as a detached gate whose
// stderr nobody delivers; the session receives only the run's verdict. The
// engage-time trunk line must therefore be in the verdict the driver is handed.
// (The in-process path still printing to stderr is
// TestStorySetEngageTrunkLineStaysOnStderrOutsideAGateRun.)
func TestSlowStorySetEngageTrunkLineReachesTheDeliveredVerdict(t *testing.T) {
	repo, r, id := trunkEngageRepo(t, "")
	r.PublishFromPusher(t, "a.txt")
	before := r.Head(t, repo)
	tip := r.Git(t, r.Remote, "rev-parse", "refs/heads/main")
	want := "satelle: trunk fast-forwarded main by 1 commit(s) " + before[:8] + ".." + tip[:8]

	rt := t.TempDir()
	store := gatehandle.New(rt)
	m, err := store.Create(gatehandle.Meta{ID: "gw_trunk", Verb: "story set", Story: id})
	if err != nil {
		t.Fatal(err)
	}
	old := gateRun
	gateRun.id, gateRun.runtime = m.ID, rt
	t.Cleanup(func() { gateRun = old })

	_, stderr, err := runRootSplit(t, "", "story", "set", id, "--status", "in_progress")
	if err != nil {
		t.Fatalf("engage: %v\n%s", err, stderr)
	}
	if err := store.Finish(m.ID, gatehandle.Result{}); err != nil {
		t.Fatal(err)
	}
	v, done := store.Load(m.ID)
	if !done {
		t.Fatal("the finished run was not loadable")
	}
	if block := verdictBlock(v); !strings.Contains(block, want) {
		t.Fatalf("delivered verdict = %q, want it to contain %q", block, want)
	}
	if n := strings.Count(v.Lines, "satelle: trunk"); n != 1 {
		t.Fatalf("verdict log carries %d trunk lines, want 1:\n%s", n, v.Lines)
	}
}
