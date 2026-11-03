package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remotechan"
)

// Attach needs nothing sent to the node: the node already holds its definition and
// Attach returns the report the node gave of it.
func TestAttachReturnsTheReportTheNodeGave(t *testing.T) {
	r := newRig(t)
	want := Report{
		Version: "1.2.3", DefinitionVersion: "v7", Workflows: []string{"default"},
		Allow: []string{"git.example.com", "satelle.test"}, Bindings: []string{"worker"},
	}
	run, _ := r.attach(echoNode(), want, nil)

	if !reflect.DeepEqual(run.Report, want) {
		t.Errorf("report = %+v, want %+v", run.Report, want)
	}
}

// A message sent to the node's agent comes back as the reply of a channel job, and
// the job carries a heartbeat for as long as the node works: the request is still
// alive after twice LostAfter of channel time because the node kept beating, and
// its reply arrives.
func TestAgentRequestHeartbeatsAndReplies(t *testing.T) {
	r := newRig(t)
	release := make(chan struct{})
	started := make(chan struct{})
	node := NewNode()
	node.Handle(AgentJob, func(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
		var req AgentRequest
		_ = json.Unmarshal(body, &req)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.Marshal(AgentReply{Text: "re: " + req.Text})
	})
	run, _ := r.attach(node, ready("1.2.3"), nil)

	type out struct {
		body json.RawMessage
		err  error
	}
	res := make(chan out, 1)
	before := r.remoteFrames()
	go func() {
		b, err := run.Request(testCtx(t), AgentJob, AgentRequest{Text: "hello"})
		res <- out{b, err}
	}()
	<-started
	eventually(t, "the job's first heartbeat", func() bool { return r.remoteFrames() > before })
	base := r.remoteFrames()

	steps := 2 * int(remotechan.LostAfter/remotechan.HeartbeatInterval)
	for i := 1; i <= steps; i++ {
		r.clock.Advance(remotechan.HeartbeatInterval)
		want := base + i
		eventually(t, "a heartbeat for each interval", func() bool { return r.remoteFrames() >= want })
		select {
		case o := <-res:
			t.Fatalf("request ended early at step %d: %v %v", i, o.body, o.err)
		default:
		}
	}
	close(release)
	o := <-res
	if o.err != nil {
		t.Fatalf("Request: %v", o.err)
	}
	rep, err := DecodeAgentReply(o.body)
	if err != nil || rep.Text != "re: hello" {
		t.Errorf("reply = %+v, %v", rep, err)
	}
}

// The channel carries any job type a later story registers, and a type the node has
// no handler for is answered as unsupported.
func TestNamedJobTypes(t *testing.T) {
	r := newRig(t)
	node := echoNode()
	node.Handle("custom.sum", func(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
		var xs []int
		if err := json.Unmarshal(body, &xs); err != nil {
			return nil, err
		}
		n := 0
		for _, x := range xs {
			n += x
		}
		return json.Marshal(n)
	})
	run, _ := r.attach(node, ready("1.2.3"), nil)

	body, err := run.Request(testCtx(t), "custom.sum", []int{1, 2, 3})
	if err != nil || string(body) != "6" {
		t.Fatalf("custom.sum = %s, %v", body, err)
	}
	_, err = run.Request(testCtx(t), "no.such.type", nil)
	if err == nil || !strings.Contains(err.Error(), `unsupported job type "no.such.type"`) {
		t.Errorf("an unregistered type must be answered unsupported, got %v", err)
	}
}

// A node whose host freezes — its channel time stops while the connection stays up
// — has its job reported lost within LostAfter + HeartbeatInterval of its last
// heartbeat, measured on the injected fake clock.
func TestAFrozenNodeReportsTheJobLost(t *testing.T) {
	r := newRig(t)
	nodeClock := &freezable{inner: r.clock}
	started := make(chan struct{})
	node := NewNode()
	node.Handle(AgentJob, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		close(started)
		<-ctx.Done() // the agent works until the channel goes
		return nil, ctx.Err()
	})
	run, _ := r.attach(node, ready("1.2.3"), nodeClock)

	res := make(chan error, 1)
	before := r.remoteFrames()
	go func() {
		_, err := run.Request(testCtx(t), AgentJob, AgentRequest{Text: "work"})
		res <- err
	}()
	<-started
	eventually(t, "the job's first heartbeat", func() bool { return r.remoteFrames() > before })

	// Healthy for a while: the heartbeats keep it alive.
	for i := 1; i <= 2; i++ {
		base := r.remoteFrames()
		r.clock.Advance(remotechan.HeartbeatInterval)
		eventually(t, "a heartbeat", func() bool { return r.remoteFrames() > base })
	}
	select {
	case err := <-res:
		t.Fatalf("job ended while healthy: %v", err)
	default:
	}

	lastBeat := r.clock.Now()
	nodeClock.freeze()

	var lostErr error
	for r.clock.Now().Sub(lastBeat) <= remotechan.LostAfter+remotechan.HeartbeatInterval+remotechan.HeartbeatInterval && lostErr == nil {
		r.clock.Advance(remotechan.HeartbeatInterval)
		select {
		case lostErr = <-res:
		case <-time.After(300 * time.Millisecond):
		}
	}
	if !errors.Is(lostErr, ErrJobLost) {
		t.Fatalf("the job was not reported lost, got %v", lostErr)
	}
	silent := r.clock.Now().Sub(lastBeat)
	if silent > remotechan.LostAfter+remotechan.HeartbeatInterval {
		t.Errorf("reported lost after %v of silence, want within %v", silent, remotechan.LostAfter+remotechan.HeartbeatInterval)
	}
	if silent <= remotechan.LostAfter {
		t.Errorf("reported lost after only %v, before LostAfter %v", silent, remotechan.LostAfter)
	}
}

// A node whose channel ends mid-job fails the request rather than leaving it
// waiting.
func TestChannelEndFailsTheRequest(t *testing.T) {
	r := newRig(t)
	started := make(chan struct{})
	node := NewNode()
	node.Handle(AgentJob, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	run, h := r.attach(node, ready("1.2.3"), nil)
	res := make(chan error, 1)
	go func() {
		_, err := run.Request(testCtx(t), AgentJob, AgentRequest{Text: "x"})
		res <- err
	}()
	<-started
	h.dropChannel()
	select {
	case err := <-res:
		if !errors.Is(err, ErrJobLost) {
			t.Errorf("err = %v, want ErrJobLost", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the request never failed")
	}
}

// End closes the channel; ending twice does nothing more, and a request after the
// end is a lost job rather than a hang.
func TestEndClosesTheChannelOnce(t *testing.T) {
	r := newRig(t)
	run, h := r.attach(echoNode(), ready("1.2.3"), nil)
	if err := run.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	if err := run.End(); err != nil {
		t.Errorf("second End: %v", err)
	}
	if _, err := run.Request(testCtx(t), AgentJob, AgentRequest{Text: "late"}); !errors.Is(err, ErrJobLost) {
		t.Errorf("a request after End = %v, want ErrJobLost", err)
	}
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Error("the node kept serving after the controller closed the channel")
	}
}

// AC8: a job that fails because the node token was refused is still answered, and
// then the node stops serving with that refusal.
func TestARefusedTokenInAJobStopsTheNodeAfterTheReply(t *testing.T) {
	r := newRig(t)
	node := NewNode()
	node.Handle(AgentJob, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, &TokenRefusedError{Route: "POST /api/v1/remote-nodes/nod_a/secrets/K"}
	})
	run, h := r.attach(node, ready("1.2.3"), nil)

	_, err := run.Request(testCtx(t), AgentJob, AgentRequest{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "refused or revoked") || !strings.Contains(err.Error(), "/secrets/K") {
		t.Errorf("Request = %v, want the refusal naming the route", err)
	}
	select {
	case err := <-h.done:
		var tr *TokenRefusedError
		if !errors.As(err, &tr) {
			t.Errorf("Serve = %v, want the *TokenRefusedError", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the node kept serving after its token was refused")
	}
}

// A node that cannot become ready fails the attach naming the cause, and the
// controller's end of the channel is closed rather than left behind.
func TestAttachFailsNamingTheCause(t *testing.T) {
	r := newRig(t)
	sess, _ := r.open(echoNode(), Report{Error: `binding "worker": command "no-such-agent" is not executable on this machine`}, r.clock)
	run, err := Attach(testCtx(t), sess, 20*time.Second)
	if err == nil {
		_ = run.End()
		t.Fatal("Attach must fail")
	}
	for _, w := range []string{`binding "worker"`, "no-such-agent"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q should name %q", err, w)
		}
	}
	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Error("a failed attach left the channel open")
	}
}

// sty_ee78148f: the relay restarts mid-session; both ends redial the same
// session and a new job completes over the resumed channel.
func TestRelayRestartRepairsAndJobCompletes(t *testing.T) {
	r := newRig(t)
	run, h := r.attachServer(echoNode(), ready("1.2.3"))
	if _, err := run.Request(testCtx(t), AgentJob, AgentRequest{Text: "before"}); err != nil {
		t.Fatalf("Request before the restart: %v", err)
	}
	before := r.relay.Upgrades()

	r.relay.Restart()
	// Restart returns once the relay has closed its connections, not once either
	// session has seen the drop. A job sent before then is open across the drop and
	// is rightly cut as "relay restarted mid-job"; an end redials only after it has
	// cut its jobs, so wait for both redials before sending the job after the restart.
	r.driveUntil("both ends redialled", func() bool { return r.relay.Upgrades() >= before+2 })
	var got requested
	res := r.request(run, AgentJob, AgentRequest{Text: "after"})
	r.driveUntil("the request over the resumed channel", settled(res, &got))

	if got.err != nil {
		t.Fatalf("Request after the restart: %v", got.err)
	}
	if rep, err := DecodeAgentReply(got.body); err != nil || rep.Text != "echo: after" {
		t.Errorf("reply = %+v, %v", rep, err)
	}
	if n := r.relay.Upgrades(); n < before+2 {
		t.Errorf("upgrades = %d, want both ends to have redialled (at least %d)", n, before+2)
	}
	select {
	case err := <-h.done:
		t.Fatalf("the node stopped serving: %v", err)
	default:
	}
}

// sty_ee78148f: both ends redial at the same backoff step but the relay admits the
// node first and local a second later, so the node's key half reaches a relay with
// no local end and is dropped. The node asks again, the channel resumes inside the
// window with exactly one redial per end, and a job completes. The relay's delay is
// real time, which is what makes the order deterministic.
func TestRelayRestartRepairsNodeFirstSimultaneous(t *testing.T) {
	r := newRig(t)
	run, _ := r.attachServer(echoNode(), ready("1.2.3"))
	before, attempts, dropped := r.relay.Upgrades(), r.relay.Attempts(), r.relay.Dropped()

	r.relay.DelayAdmit("local", 600*time.Millisecond)
	r.relay.Restart()
	// Restart returns before either session has seen the drop, and a job sent
	// meanwhile is rightly cut as "relay restarted mid-job". Each end redials only
	// after cutting its jobs, so the job after the restart waits for both redials.
	r.driveUntil("both ends redialled", func() bool { return r.relay.Upgrades() >= before+2 })
	var got requested
	res := r.request(run, AgentJob, AgentRequest{Text: "after"})
	r.driveUntil("the request over the resumed channel", settled(res, &got))

	if got.err != nil {
		t.Fatalf("Request after the restart: %v", got.err)
	}
	if n := r.relay.Dropped(); n <= dropped {
		t.Errorf("dropped = %d (was %d): the node's key half should have reached a relay with no local end", n, dropped)
	}
	if n := r.relay.Upgrades(); n != before+2 {
		t.Errorf("upgrades = %d, want exactly one redial per end (%d)", n, before+2)
	}
	if n := r.relay.Attempts(); n != attempts+2 {
		t.Errorf("attempts = %d, want exactly one per end (%d): the redial must not livelock", n, attempts+2)
	}
}

// sty_ee78148f: a close that means refusal is not retried, and both ends end naming
// it.
func TestRelayRefusalNotRetried(t *testing.T) {
	cases := []struct {
		name   string
		refuse func(r *rig)
	}{
		{"4401 signed out", func(r *rig) { r.relay.CloseWith(rigSession, 4401, "signed out") }},
		{"404 bad_session", func(r *rig) {
			r.relay.RefuseSession(rigSession, 404, `{"error":"bad_session"}`)
			r.relay.Restart()
		}},
		{"409 slot_taken", func(r *rig) {
			r.relay.RefuseSession(rigSession, 409, `{"error":"slot_taken"}`)
			r.relay.Restart()
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			run, h := r.attachServer(echoNode(), ready("1.2.3"))
			base := r.relay.Attempts()

			c.refuse(r)
			select {
			case err := <-h.done:
				if !errors.Is(err, remotechan.ErrResumeRefused) {
					t.Errorf("node Serve = %v, want ErrResumeRefused", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the node kept waiting after a refusal")
			}
			select {
			case <-run.sess.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("the controller kept waiting after a refusal")
			}
			if err := run.sess.Err(); !errors.Is(err, remotechan.ErrResumeRefused) {
				t.Errorf("controller Err = %v, want ErrResumeRefused", err)
			}
			after := r.relay.Attempts()
			if after-base > 2 {
				t.Errorf("the refusal cost %d upgrade requests, want at most one per end", after-base)
			}
			for i := 0; i < 20; i++ { // past the window
				r.clock.Advance(15 * time.Second)
			}
			time.Sleep(100 * time.Millisecond)
			if n := r.relay.Attempts(); n != after {
				t.Errorf("attempts went from %d to %d: the refusal was retried", after, n)
			}
			if _, err := run.Request(testCtx(t), AgentJob, AgentRequest{Text: "x"}); !errors.Is(err, ErrJobLost) {
				t.Errorf("a request on a refused channel = %v, want ErrJobLost", err)
			}
		})
	}
}

// sty_ee78148f: a job open when the relay restarts is failed on the controller
// naming the restart, not left to time out.
func TestRequestFailsNamedOnRelayRestart(t *testing.T) {
	r := newRig(t)
	started := make(chan struct{})
	node := NewNode()
	node.Handle(AgentJob, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	run, _ := r.attachServer(node, ready("1.2.3"))
	res := r.request(run, AgentJob, AgentRequest{Text: "work"})
	<-started

	r.relay.Restart()
	select {
	case o := <-res:
		if !errors.Is(o.err, ErrJobLost) || !strings.Contains(o.err.Error(), "relay restarted mid-job") {
			t.Errorf("err = %v, want ErrJobLost naming the relay restart", o.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the request was left hanging by the restart")
	}
}
