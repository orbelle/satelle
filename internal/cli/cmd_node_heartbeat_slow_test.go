//go:build clislow && !windows

package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

// A task node's join heartbeat runs until its run's own heartbeat starts: a
// setup command held open for many resend intervals keeps node heartbeats (no story,
// no run state) arriving, then the run's queued, running and concluded statuses
// follow, and no node heartbeat comes after the run's first.
func TestSlowNodeJoinTaskNodeHeartbeatsThroughAStallBeforeTheRun(t *testing.T) {
	r, subject := taskRig(t, "nightly-sweep", true)
	scratch := t.TempDir()
	started, release := filepath.Join(scratch, "started"), filepath.Join(scratch, "release")
	r.setSetup([]string{": > " + started + " && while [ ! -e " + release + " ]; do sleep 0.02; done"}, nil)
	useTaskNode(t, recordingPerformers(), 20*time.Millisecond)

	w := r.wake(t)
	const stalled = 5
	deadline := time.Now().Add(30 * time.Second)
	for (!fileExists(started) || len(w.fake.Statuses()) < stalled) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	during := w.fake.Statuses()
	if !fileExists(started) || len(during) < stalled {
		t.Fatalf("setup started = %v, statuses during it = %v, want at least %d heartbeats\nnode log:\n%s", fileExists(started), during, stalled, r.log)
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

// A performer node whose relay restarts keeps sending status to satelle.dev for as
// long as it redials, and tells satelle.dev once when it gives up (sty_ee78148f):
// the redial happens beside the heartbeat, not in place of it, and the node still
// ends itself.
func TestSlowNodeHeartbeatsThroughRedialAndEnds(t *testing.T) {
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
