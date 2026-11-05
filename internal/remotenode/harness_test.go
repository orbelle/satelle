package remotenode

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/remotechan/relaytest"
)

// fakeClock is a remotechan.Clock a test advances, the same shape remotechan's own
// tests inject. Each Advance step delivers its ticks with a blocking send, so a
// tick is never dropped under load.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
}

type fakeTicker struct {
	d       time.Duration
	next    time.Time
	c       chan time.Time
	stopped bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Tick(d time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTicker{d: d, next: f.now.Add(d), c: make(chan time.Time, 1)}
	f.tickers = append(f.tickers, t)
	return t.c, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		t.stopped = true
	}
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	target := f.now.Add(d)
	f.mu.Unlock()
	for {
		f.mu.Lock()
		var due *fakeTicker
		for _, t := range f.tickers {
			if !t.stopped && !t.next.After(target) && (due == nil || t.next.Before(due.next)) {
				due = t
			}
		}
		if due == nil {
			f.now = target
			f.mu.Unlock()
			return
		}
		f.now = due.next
		due.next = due.next.Add(due.d)
		at := f.now
		f.mu.Unlock()
		select {
		case due.c <- at:
		case <-time.After(2 * time.Second):
		}
	}
}

// freezable is a Clock whose ticks stop on demand while its time source keeps
// going: the node's clock after its host has frozen, because the last exec
// session on it ended.
type freezable struct {
	inner  remotechan.Clock
	frozen atomic.Bool
}

func (f *freezable) Now() time.Time { return f.inner.Now() }

func (f *freezable) Tick(d time.Duration) (<-chan time.Time, func()) {
	in, stop := f.inner.Tick(d)
	out := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case t := <-in:
				if !f.frozen.Load() {
					select {
					case out <- t:
					case <-done:
						return
					}
				}
			case <-done:
				return
			}
		}
	}()
	return out, func() { stop(); close(done) }
}

func (f *freezable) freeze() { f.frozen.Store(true) }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type memStore struct{ cred hosted.Credential }

func (m *memStore) Load(string) (hosted.Credential, error) { return m.cred, nil }
func (m *memStore) Save(c hosted.Credential) error         { m.cred = c; return nil }
func (m *memStore) Delete(string) error                    { return nil }

// rig is the controller's end of a stand-in relay and an in-process node.
type rig struct {
	t        *testing.T
	relay    *relaytest.Relay
	client   *hosted.Client
	clock    *fakeClock
	chanOpts []remotechan.Option
	// serve, when set, serves the node instead of Node.Serve.
	serve func(ctx context.Context, node *Node, sess *remotechan.Session, report Report) error
}

const rigServer = "https://satelle.test"

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, relay: relaytest.New(t), clock: newFakeClock()}
	c, err := hosted.Dial(rigServer, &memStore{cred: hosted.Credential{ServerURL: rigServer, AccessToken: "access-1"}}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	r.client = c
	r.chanOpts = []remotechan.Option{remotechan.WithHTTPClient(r.relay.Client()), remotechan.WithClock(r.clock)}
	return r
}

// nodeHandle is the in-process node.
type nodeHandle struct {
	done chan error
	mu   sync.Mutex
	sess *remotechan.Session
}

// dropChannel closes the node's end of the channel, as a crashed node would.
func (h *nodeHandle) dropChannel() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sess != nil {
		h.sess.Close()
	}
}

// open plays the launcher: it opens the channel and, once the relay has issued the
// session and token, starts the node in-process joining with them, as
// `satelle node join` does, and serving with report. clock is the node's own time
// source. It returns the controller's session, ready for Attach.
func (r *rig) open(node *Node, report Report, clock remotechan.Clock) (*remotechan.Session, *nodeHandle) {
	r.t.Helper()
	id, err := remotechan.NewIdentity()
	if err != nil {
		r.t.Fatal(err)
	}
	h := &nodeHandle{done: make(chan error, 1)}
	ctx := testCtx(r.t)
	launch := remotechan.LauncherFunc(func(_ context.Context, spec remotechan.LaunchSpec) ([]byte, error) {
		go func() {
			sess, err := remotechan.Join(ctx, spec.RelayURL, spec.Session, id, spec.Token,
				remotechan.WithHTTPClient(r.relay.Client()), remotechan.WithClock(clock))
			if err != nil {
				h.done <- err
				return
			}
			h.mu.Lock()
			h.sess = sess
			h.mu.Unlock()
			defer sess.Close()
			if r.serve != nil {
				h.done <- r.serve(ctx, node, sess, report)
				return
			}
			h.done <- node.Serve(ctx, sess, report)
		}()
		return id.Public(), nil
	})
	sess, err := remotechan.Open(ctx, r.client, r.relay.URL(), launch, r.chanOpts...)
	if err != nil {
		r.t.Fatalf("Open: %v", err)
	}
	return sess, h
}

// attach brings the rig's node up and returns the Run. nodeClock is the node's own
// time source; nil shares the rig's clock.
func (r *rig) attach(node *Node, report Report, nodeClock remotechan.Clock) (*Run, *nodeHandle) {
	r.t.Helper()
	if nodeClock == nil {
		nodeClock = r.clock
	}
	sess, h := r.open(node, report, nodeClock)
	run, err := Attach(testCtx(r.t), sess, 20*time.Second)
	if err != nil {
		r.t.Fatalf("Attach: %v", err)
	}
	r.t.Cleanup(func() { _ = run.End() })
	return run, h
}

// ready is the report of a node that is ready as version.
func ready(version string) Report {
	return Report{Version: version, Workflows: []string{"*"}}
}

func echoNode() *Node {
	n := NewNode()
	n.Handle(AgentJob, func(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
		var req AgentRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return json.Marshal(AgentReply{Text: "echo: " + req.Text})
	})
	return n
}

// remoteFrames counts the frames the relay has forwarded from the node.
func (r *rig) remoteFrames() int {
	n := 0
	for _, f := range r.relay.Frames() {
		if f.From == "remote" {
			n++
		}
	}
	return n
}
