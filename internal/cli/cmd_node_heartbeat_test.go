//go:build !windows

package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotechan"
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

// A node heartbeats from the moment it joins, not once it has bootstrapped: a
// [host] bootstrap command may run for minutes, far past the window after which
// satelle.dev lists a silent node lost. One held open here keeps sending running
// statuses while it runs, and none reports a run.
func TestNodeJoinHeartbeatsDuringASlowBootstrap(t *testing.T) {
	r := newNodeRig(t)
	scratch := t.TempDir()
	started, release := filepath.Join(scratch, "started"), filepath.Join(scratch, "release")
	r.setBootstrap([][]string{{"sh", "-c", ": > " + started + " && while [ ! -e " + release + " ]; do sleep 0.02; done"}})
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
		t.Fatalf("the bootstrap command never ran\nnode log:\n%s", r.log)
	}
	during := fake.Statuses()
	if len(during) < 3 {
		t.Fatalf("statuses during the bootstrap = %v, want at least 3 heartbeats", during)
	}
	for _, s := range during {
		if s["state"] != remotenode.StateRunning || s["step"] != remotenode.StepWaiting || s["run_state"] != "" {
			t.Errorf("bootstrap heartbeat = %v, want state running, step waiting and no run state", s)
		}
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 0 {
		t.Errorf("end calls = %d while the bootstrap runs, want 0", n)
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

// A task node's join heartbeat runs until its run's own heartbeat starts: a
// bootstrap held open for many resend intervals keeps node heartbeats (no story,
// no run state) arriving, then the run's queued, running and concluded statuses
// follow, and no node heartbeat comes after the run's first.
func TestNodeJoinTaskNodeHeartbeatsThroughAStallBeforeTheRun(t *testing.T) {
	r, subject := taskRig(t, "nightly-sweep", true)
	scratch := t.TempDir()
	started, release := filepath.Join(scratch, "started"), filepath.Join(scratch, "release")
	r.bundle["remote.toml"] += "\n[host]\n"
	r.setBootstrap([][]string{{"sh", "-c", ": > " + started + " && while [ ! -e " + release + " ]; do sleep 0.02; done"}})
	useTaskNode(t, recordingPerformers(), 20*time.Millisecond)

	w := r.wake(t)
	const stalled = 5
	deadline := time.Now().Add(30 * time.Second)
	for (!fileExists(started) || len(w.fake.Statuses()) < stalled) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	during := w.fake.Statuses()
	if !fileExists(started) || len(during) < stalled {
		t.Fatalf("bootstrap started = %v, statuses during it = %v, want at least %d heartbeats\nnode log:\n%s", fileExists(started), during, stalled, r.log)
	}
	for _, s := range during {
		if s["story_id"] != "" || s["run_state"] != "" || s["state"] != remotenode.StateRunning {
			t.Errorf("heartbeat during the stall = %v, want a node heartbeat with no story and no run state", s)
		}
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}

	st := w.fake.Statuses()
	first := -1
	for i, s := range st {
		if s["story_id"] != "" {
			first = i
			break
		}
	}
	if first < stalled {
		t.Fatalf("the run's first status is at %d of %v, want at least %d node heartbeats before it", first, st, stalled)
	}
	if s := st[first]; s["story_id"] != subject.ID() || s["run_state"] != "queued" {
		t.Errorf("the run's first status = %v, want queued for the wake's subject", s)
	}
	for _, s := range st[first:] {
		if s["story_id"] == "" {
			t.Errorf("node heartbeat %v follows the run's first status", s)
		}
	}
	if got, want := runSteps(t, w.fake, subject.ID()), []string{"queued//", "running/in_progress/", "running/done/", "concluded//accept"}; !reflect.DeepEqual(got, want) {
		t.Errorf("run statuses = %v, want %v", got, want)
	}
	if n := w.fake.Count(http.MethodPost, "/end"); n != 1 {
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

// A bootstrap that fails after running statuses are in flight: the failed start's
// concluded status is the last one satelle.dev hears, no running re-send follows
// it, and the node ends once with nothing sent after.
func TestNodeJoinEndsAndStopsHeartbeatOnBootstrapFailure(t *testing.T) {
	r := newNodeRig(t)
	r.setBootstrap([][]string{{"sh", "-c", "sleep 0.3; exit 3"}})
	prev := nodeStatusResend
	nodeStatusResend = 20 * time.Millisecond
	t.Cleanup(func() { nodeStatusResend = prev })
	fake := r.serve("nod_a", "node-token", r.encode())
	jerr := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if jerr == nil || !strings.Contains(jerr.Error(), "bootstrap command 1: exit status 3") {
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

// A performer node whose relay restarts keeps sending status to satelle.dev for as
// long as it redials, and tells satelle.dev once when it gives up (sty_ee78148f):
// the redial happens beside the heartbeat, not in place of it, and the node still
// ends itself.
func TestNodeHeartbeatsThroughRedialAndEnds(t *testing.T) {
	r := newNodeRig(t)
	r.fakeSetup = func(f *devtest.Node) {
		f.Run = map[string]string{"mode": remotenode.ModePerformer, "story_id": "sty_redial"}
	}
	prev := nodeStatusResend
	nodeStatusResend = 20 * time.Millisecond
	t.Cleanup(func() { nodeStatusResend = prev })
	// A short window, so the node gives up inside the test.
	nodeChannelOptions = append(nodeChannelOptions, remotechan.WithResumeWindow(time.Second))

	const session = "nod_redial"
	fake := r.serve(session, "node-token", r.encode())
	joined := make(chan error, 1)
	go func() { joined <- r.joinNow(r.relay.URL(), session, "node-token") }()

	// The starter's end: attached by the key the node published, as satelle.dev's
	// starter does.
	client := hosted.NewClient("https://satelle.test", &nodeTestStore{cred: hosted.Credential{ServerURL: "https://satelle.test", AccessToken: "access-1"}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	sess, err := remotechan.AttachServerSession(ctx, client, r.relay.URL(), session,
		func(context.Context) ([]byte, error) { return fake.ChannelKey(), nil },
		remotechan.WithHTTPClient(r.relay.Client()))
	if err != nil {
		t.Fatalf("AttachServerSession: %v\nnode log:\n%s", err, r.log)
	}
	run, err := remotenode.Attach(ctx, sess, 60*time.Second)
	if err != nil {
		t.Fatalf("Attach: %v\nnode log:\n%s", err, r.log)
	}
	t.Cleanup(func() { _ = run.End() })

	// The relay goes down and stays down. The node redials, and keeps heartbeating.
	r.relay.Down()
	during := fake.Count(http.MethodPut, "/status")
	deadline := time.Now().Add(30 * time.Second)
	for fake.Count(http.MethodPut, "/status") < during+3 {
		select {
		case err := <-joined:
			t.Fatalf("the node ended (%v) before heartbeating through the redial", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("status calls = %d, want at least 3 more than %d while the node redials", fake.Count(http.MethodPut, "/status"), during)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 0 {
		t.Errorf("end calls = %d while the node was still redialling, want 0", n)
	}

	// The window passes: the node gives up, naming why, and tells satelle.dev once.
	if err := waitJoin(t, joined); !errors.Is(err, remotechan.ErrResumeExpired) {
		t.Errorf("node join ended with %v, want ErrResumeExpired\nnode log:\n%s", err, r.log)
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	after := len(fake.Statuses())
	time.Sleep(100 * time.Millisecond)
	if n := len(fake.Statuses()); n != after {
		t.Errorf("status calls grew from %d to %d after the node ended", after, n)
	}
}
