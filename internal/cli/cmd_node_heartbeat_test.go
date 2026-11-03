//go:build !windows

package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

// nonStatusCalls is every call the fake received but the heartbeat's: the node's
// own heartbeat starts at join, before its first fetch.
func nonStatusCalls(fake *devtest.Node) []devtest.Call {
	var out []devtest.Call
	for _, c := range fake.Calls() {
		if c.Method == http.MethodPut && strings.HasSuffix(c.Path, "/status") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// A node heartbeats from the moment it joins, not once it has set up: a
// [setup] command may run for minutes, far past the window after which
// satelle.dev lists a silent node lost. One held open here keeps sending running
// statuses while it runs, and none reports a run.
func TestNodeJoinHeartbeatsDuringASlowSetup(t *testing.T) {
	r := newNodeRig(t)
	scratch := t.TempDir()
	started, release := filepath.Join(scratch, "started"), filepath.Join(scratch, "release")
	r.setSetup([]string{": > " + started + " && while [ ! -e " + release + " ]; do sleep 0.02; done"}, nil)
	prev := nodeStatusResend
	nodeStatusResend = 20 * time.Millisecond
	t.Cleanup(func() { nodeStatusResend = prev })
	fakes := make(chan *devtest.Node, 1)
	r.fakeSetup = func(f *devtest.Node) { fakes <- f }

	opened := make(chan error, 1)
	go func() {
		run, _, joined, _, err := r.tryOpen(t)
		if err == nil {
			err = run.End()
		}
		if err == nil {
			err = waitJoin(t, joined)
		}
		opened <- err
	}()
	fake := <-fakes
	deadline := time.Now().Add(30 * time.Second)
	for (!fileExists(started) || len(fake.Statuses()) < 3) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !fileExists(started) {
		t.Fatalf("the setup command never ran\nnode log:\n%s", r.log)
	}
	during := fake.Statuses()
	if len(during) < 3 {
		t.Fatalf("statuses during the setup = %v, want at least 3 heartbeats", during)
	}
	for _, s := range during {
		if s["state"] != remotenode.StateRunning || s["step"] != remotenode.StepWaiting || s["run_state"] != "" {
			t.Errorf("setup heartbeat = %v, want state running, step waiting and no run state", s)
		}
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 0 {
		t.Errorf("end calls = %d while the setup runs, want 0", n)
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-opened; err != nil {
		t.Fatalf("node join: %v\nnode log:\n%s", err, r.log)
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
}

// assertStartFailureStatuses checks the statuses of a node whose start failed:
// the heartbeats it sent first are running with no run, exactly one concluded
// reject follows them and is the last, and the node ended once with nothing sent
// after it. wantRunning is the least number of heartbeats before the conclusion.
func assertStartFailureStatuses(t *testing.T, fake *devtest.Node, wantRunning int) {
	t.Helper()
	st := fake.Statuses()
	concluded := 0
	for i, s := range st {
		if s["run_state"] == "concluded" {
			concluded++
			if i != len(st)-1 {
				t.Errorf("status %d of %d is concluded, but %d statuses follow it: %v", i+1, len(st), len(st)-1-i, st)
			}
			if s["outcome"] != "reject" {
				t.Errorf("concluded status = %v, want outcome reject", s)
			}
		} else if s["state"] != remotenode.StateRunning || s["run_state"] != "" {
			t.Errorf("status %v before the conclusion is not a plain running heartbeat", s)
		}
	}
	if concluded != 1 {
		t.Errorf("concluded statuses = %d, want exactly 1: %v", concluded, st)
	}
	if running := len(st) - concluded; running < wantRunning {
		t.Errorf("running heartbeats before the conclusion = %d, want at least %d", running, wantRunning)
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(fake.Statuses()); n != len(st) {
		t.Errorf("status calls grew from %d to %d after the node ended", len(st), n)
	}
}

// A setup command that fails after running statuses are in flight: the failed start's
// concluded status is the last one satelle.dev hears, no running re-send follows
// it, and the node ends once with nothing sent after.
func TestNodeJoinEndsAndStopsHeartbeatOnSetupFailure(t *testing.T) {
	r := newNodeRig(t)
	r.setSetup([]string{"sleep 0.3; exit 3"}, nil)
	prev := nodeStatusResend
	nodeStatusResend = 20 * time.Millisecond
	t.Cleanup(func() { nodeStatusResend = prev })
	fake := r.serve("nod_a", "node-token", r.encode())
	jerr := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if jerr == nil || !strings.Contains(jerr.Error(), "setup on_create command 1: exit status 3") {
		t.Errorf("join ended with %v, want the failing command's index and reason", jerr)
	}
	assertStartFailureStatuses(t, fake, 3)
}

// A definition that cannot be installed fails the start the same way: reported
// once as the last status, then ended once.
func TestNodeJoinEndsAndStopsHeartbeatOnInstallFailure(t *testing.T) {
	r := newNodeRig(t)
	prev := nodeStatusResend
	nodeStatusResend = 20 * time.Millisecond
	t.Cleanup(func() { nodeStatusResend = prev })
	fake := r.serve("nod_a", "node-token", []byte("not a bundle"))
	if jerr := r.joinNow(r.relay.URL(), "nod_a", "node-token"); jerr == nil {
		t.Error("join succeeded with a definition that is not a bundle")
	}
	assertStartFailureStatuses(t, fake, 1)
}

// A performer node sends status for as long as it lives, not only at its first
// report: satelle.dev lists a node silent past its window as lost and ends it, so
// one idle on the channel with no job still heartbeats, and still ends itself once
// through satelle.dev when the channel closes.
func TestNodeJoinPerformerHeartbeatsWhileIdleAndStillEnds(t *testing.T) {
	r := newNodeRig(t)
	r.fakeSetup = func(f *devtest.Node) {
		f.Run = map[string]string{"mode": remotenode.ModePerformer, "story_id": "sty_idle"}
	}
	prev := nodeStatusResend
	nodeStatusResend = 20 * time.Millisecond
	t.Cleanup(func() { nodeStatusResend = prev })

	run, fake, joined, _ := r.open(t)
	deadline := time.Now().Add(30 * time.Second)
	for len(fake.Statuses()) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("statuses = %v, want at least 3 heartbeats while idle\nnode log:\n%s", fake.Statuses(), r.log)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, s := range fake.Statuses() {
		if s["state"] != remotenode.StateRunning || s["step"] != remotenode.StepWaiting || s["run_state"] != "" {
			t.Errorf("idle heartbeat = %v, want state running, step waiting and no run state", s)
		}
	}

	if err := run.End(); err != nil {
		t.Errorf("End: %v", err)
	}
	if err := waitJoin(t, joined); err != nil {
		t.Errorf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	// Stopped before the end call: no heartbeat is sent once the node has ended.
	after := len(fake.Statuses())
	time.Sleep(100 * time.Millisecond)
	if n := len(fake.Statuses()); n != after {
		t.Errorf("status calls grew from %d to %d after the node ended", after, n)
	}
}
