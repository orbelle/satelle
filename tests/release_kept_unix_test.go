//go:build integration && unix

package tests

import (
	"strings"
	"testing"
)

// attachKept attaches body to run as its release-kept list, as release.md
// step 4 does.
func (d *driftSandbox) attachKept(run, body string) {
	d.t.Helper()
	if r := d.exec(satelleBin, body, "execution", "attach", run, "--name", "release-kept", "--type", "text/plain", "--file", "-"); r.code != 0 {
		d.t.Fatalf("attach release-kept to %s: %s%s", run, r.out, r.err)
	}
}

// AC1: the repo's runbook refuses a run's done while an item its release-kept
// list names is not in a terminal state of its own workflow, naming the item
// and its state; once the item is terminal the run closes. A run with no
// release-kept landed nothing and closes; an id that cannot be read refuses.
func TestReleaseRunDoneWaitsForItsKeptItems(t *testing.T) {
	t.Parallel()
	d := newDriftSandbox(t, nil)
	seat := stepSeat(t)
	s := d.create("story", "--title", "landed").ID
	if st := d.get(s).Status; st != "defined" {
		t.Fatalf("setup: %s is %s, want defined (not terminal)", s, st)
	}

	run := d.start(d.other, seat)
	d.attachKept(run, s+" 0123456789abcdef0123456789abcdef01234567\n")
	if r := d.run("execution", "set", run, "--status", "done"); r.code == 0 {
		t.Fatalf("a run whose kept item is open reached done:\n%s%s", r.out, r.err)
	}
	if st := d.status(run); st != "in_progress" {
		t.Fatalf("run %s is %s, want in_progress", run, st)
	}
	if g := latestRun(d.sandbox, run, "release-kept"); g.Result != "reject" || !strings.Contains(g.Notes, s+" is defined, not terminal") {
		t.Fatalf("release-kept on %s: %+v", run, g)
	}

	d.must("story", "set", s, "--status", "retired")
	if r := d.run("execution", "set", run, "--status", "done"); r.code != 0 {
		t.Fatalf("a run whose kept item is terminal: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if st := d.status(run); st != "done" {
		t.Fatalf("run %s is %s, want done", run, st)
	}
	if g := latestRun(d.sandbox, run, "release-kept"); g.Result != "accept" || !strings.Contains(g.Notes, s+" retired") {
		t.Fatalf("release-kept on %s: %+v", run, g)
	}

	none := d.start(d.other, seat)
	d.must("execution", "set", none, "--status", "done")
	if g := latestRun(d.sandbox, none, "release-kept"); g.Result != "accept" || !strings.Contains(g.Notes, "landed no item") {
		t.Fatalf("release-kept on a run with no list: %+v", g)
	}

	bad := d.start(d.other, seat)
	d.attachKept(bad, "sto_00000000 0123456789abcdef0123456789abcdef01234567\n")
	if r := d.run("execution", "set", bad, "--status", "done"); r.code == 0 {
		t.Fatalf("a run naming an unreadable item reached done:\n%s%s", r.out, r.err)
	}
	if g := latestRun(d.sandbox, bad, "release-kept"); g.Result != "reject" || !strings.Contains(g.Notes, "cannot read sto_00000000") {
		t.Fatalf("release-kept naming an unreadable id: %+v", g)
	}
}
