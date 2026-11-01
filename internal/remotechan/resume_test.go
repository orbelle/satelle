package remotechan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// Resume (sty_ee78148f): a relay restart closes every session. Both ends of a
// server-minted session redial the same session id and handshake again, within a
// bounded window; a close that means refusal is never retried.

// memRecorder is a Recorder that keeps what it was given, so a test reads the
// resume rows without depending on a database taking two writers at once.
type memRecorder struct {
	mu   sync.Mutex
	rows []ledger.AppendInput
}

func (m *memRecorder) Append(_ context.Context, in ledger.AppendInput, _ time.Time) (ledger.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, in)
	return ledger.Entry{}, nil
}

// payloads are the payloads of the rows of kind.
func (m *memRecorder) payloads(kind string) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []map[string]any
	for _, r := range m.rows {
		if r.Kind != kind {
			continue
		}
		p := map[string]any{}
		_ = json.Unmarshal(r.Payload, &p)
		out = append(out, p)
	}
	return out
}

func (m *memRecorder) count(kind string) int { return len(m.payloads(kind)) }

// pair connects the two ends of a server-minted session over the stand-in relay.
func (h *harness) pair(t *testing.T, session string, rec Recorder, extra ...Option) (local, remote *Session) {
	t.Helper()
	opts := append(h.opts(), WithRecorder(rec))
	opts = append(opts, extra...)
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)
	att, rem := make(chan attachResult, 1), make(chan joined, 1)
	go func() {
		s, err := AttachServerSession(ctx, h.client, h.relay.URL(), session, publishedKey(id.Public()), opts...)
		att <- attachResult{s, err}
	}()
	eventually(t, "the starter on the relay", func() bool { return h.relay.Upgrades() >= 1 })
	go func() {
		s, err := Join(ctx, h.relay.URL(), session, id, "node-token", opts...)
		rem <- joined{s, err}
	}()
	a, r := <-att, <-rem
	if a.err != nil || r.err != nil {
		t.Fatalf("attach = %v, join = %v", a.err, r.err)
	}
	t.Cleanup(func() { a.s.Close(); r.s.Close() })
	return a.s, r.s
}

// drive advances the fake clock by step, again and again, until cond holds: the
// resends and backoffs of a resume are all on the session clock. Fake time runs
// ahead at real-time pace, so drive suits only a wait whose outcome fake time
// running ahead cannot change; only a hang, at hangBound, fails it.
func (h *harness) drive(t *testing.T, step time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hangBound)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out driving the clock until %s", what)
		}
		h.clock.Advance(step)
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitResume waits for both ends to finish redialling, advancing the clock only
// when observable state says a tick is needed, so fake time never runs ahead into
// a redial's deadline however slowly the ends run. mark is the tick-log length
// and upBefore the relay's upgrade count, both taken before the ends redial.
//
// Only the remote re-sends, and it registers its HelloResend ticker after its
// first hello is written (delivered, or dropped because local was not yet on
// the relay). Once both ends are on the relay and that ticker is registered, one
// HelloResend delivers a hello local can answer; the clock is then held while
// the handshake completes.
func (h *harness) awaitResume(t *testing.T, mark, upBefore int, what string, cond func() bool) {
	t.Helper()
	eventually(t, "both ends back on the relay", func() bool { return h.relay.Upgrades() >= upBefore+2 })
	eventually(t, "the remote waiting to re-send its hello", func() bool {
		for _, d := range h.clock.tickLog()[mark:] {
			if d == HelloResend {
				return true
			}
		}
		return false
	})
	if cond() {
		return
	}
	h.clock.Advance(HelloResend)
	eventually(t, what, cond)
}

func bothDone(a, b *Session) func() bool {
	return func() bool {
		select {
		case <-a.Done():
		default:
			return false
		}
		select {
		case <-b.Done():
			return true
		default:
			return false
		}
	}
}

// exchange proves the channel carries a message each way.
func exchange(t *testing.T, local, remote *Session) {
	t.Helper()
	ctx := testCtx(t)
	if err := local.Send(ctx, "j", []byte("ping")); err != nil {
		t.Fatalf("local Send: %v", err)
	}
	if m, err := remote.Recv(ctx); err != nil || string(m.Body) != "ping" {
		t.Fatalf("remote Recv = %q, %v; want ping", m.Body, err)
	}
	if err := remote.Send(ctx, "j", []byte("pong")); err != nil {
		t.Fatalf("remote Send: %v", err)
	}
	if m, err := local.Recv(ctx); err != nil || string(m.Body) != "pong" {
		t.Fatalf("local Recv = %q, %v; want pong", m.Body, err)
	}
}

// (AC1) The relay restarts mid-session: both ends redial the same session, the
// sessions stay up, and the channel carries messages again.
func TestResumeAfterRelayRestart(t *testing.T) {
	h := newHarness(t, true)
	rec := &memRecorder{}
	local, remote := h.pair(t, "nod_restart", rec)
	exchange(t, local, remote)
	before := h.relay.Upgrades()
	mark := len(h.clock.tickLog())

	h.relay.Restart()
	h.awaitResume(t, mark, before, "both ends resumed", func() bool { return rec.count(ledger.KindRemoteChannelResumed) >= 2 })

	if got := h.relay.Upgrades(); got < before+2 {
		t.Errorf("upgrades = %d, want both ends to have redialled (at least %d)", got, before+2)
	}
	for name, s := range map[string]*Session{"local": local, "remote": remote} {
		select {
		case <-s.Done():
			t.Fatalf("%s session ended: %v", name, s.Err())
		default:
		}
	}
	exchange(t, local, remote)
	roles := map[any]bool{}
	for _, p := range rec.payloads(ledger.KindRemoteChannelResumed) {
		roles[p["role"]] = true
		if p["session"] != "nod_restart" {
			t.Errorf("resume row session = %v", p["session"])
		}
	}
	if !roles["local"] || !roles["remote"] {
		t.Errorf("resume rows cover roles %v, want both ends", roles)
	}
}

// (AC1) With the relay down, each end redials after 500ms, 1s, 2s … and the
// channel resumes when the relay is back.
func TestResumeBackoffIntervals(t *testing.T) {
	h := newHarness(t, true)
	rec := &memRecorder{}
	local, remote := h.pair(t, "nod_backoff", rec)
	mark := len(h.clock.tickLog())

	// Read before Down: Down closes both connections, and each end's first redial
	// is at once, so a count read after it may already include one or both.
	base := h.relay.Attempts()
	h.relay.Down()
	count := func(d time.Duration) int {
		n := 0
		for _, got := range h.clock.tickLog()[mark:] {
			if got == d {
				n++
			}
		}
		return n
	}
	// Each step waits for both ends to register their next backoff wait on the
	// fake clock: an end registers it only after its redial attempt has returned,
	// so no attempt is counted against a real-time window.
	intervals := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	for i, want := range intervals {
		eventually(t, fmt.Sprintf("both ends waiting %v", want), func() bool { return count(want) >= 2 })
		if got := count(want); got != 2 {
			t.Fatalf("%v waited %d times, want once per end", want, got)
		}
		h.clock.Advance(want)
		next := 2 * want
		eventually(t, fmt.Sprintf("both ends waiting %v after the %v wait", next, want), func() bool { return count(next) >= 2 })
		// The first redial is at once; each wait that ends makes one more attempt per end.
		if got, wantAttempts := h.relay.Attempts(), base+2*(i+2); got < wantAttempts {
			t.Fatalf("after the %v wait: %d attempts, want at least %d", want, got, wantAttempts)
		}
	}
	// The waits, in the order the ends registered them, are the sequence once per end.
	inOrder := []time.Duration{500 * time.Millisecond, 500 * time.Millisecond, time.Second, time.Second,
		2 * time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	var waits []time.Duration
	for _, d := range h.clock.tickLog()[mark:] {
		if slices.Contains(inOrder, d) {
			waits = append(waits, d)
		}
	}
	if !slices.Equal(waits, inOrder) {
		t.Fatalf("backoff waits in order = %v, want %v", waits, inOrder)
	}

	upMark, upBefore := len(h.clock.tickLog()), h.relay.Upgrades()
	h.relay.Up()
	h.clock.Advance(2 * intervals[len(intervals)-1]) // both 8s waits are registered: each end redials once
	h.awaitResume(t, upMark, upBefore, "both ends resumed", func() bool { return rec.count(ledger.KindRemoteChannelResumed) >= 2 })
	exchange(t, local, remote)
}

// (AC1) A relay that never comes back ends both sessions, naming why, once the
// window has passed; nothing hangs.
func TestResumeExpires(t *testing.T) {
	h := newHarness(t, true)
	rec := &memRecorder{}
	local, remote := h.pair(t, "nod_expires", rec, WithResumeWindow(time.Minute))

	h.relay.Down()
	h.drive(t, 5*time.Second, "both sessions ended", bothDone(local, remote))

	for name, s := range map[string]*Session{"local": local, "remote": remote} {
		if err := s.Err(); !errors.Is(err, ErrResumeExpired) {
			t.Errorf("%s Err = %v, want ErrResumeExpired", name, err)
		}
		if _, err := s.Recv(testCtx(t)); !errors.Is(err, ErrResumeExpired) {
			t.Errorf("%s Recv = %v, want ErrResumeExpired", name, err)
		}
		if err := s.Send(testCtx(t), "", []byte("x")); !errors.Is(err, ErrClosed) {
			t.Errorf("%s Send = %v, want ErrClosed", name, err)
		}
	}
	rows := rec.payloads(ledger.KindRemoteChannelResumeFailed)
	if len(rows) != 2 || rows[0]["reason"] != "expired" || rows[1]["reason"] != "expired" {
		t.Errorf("resume_failed rows = %v, want two expired", rows)
	}
}

func helloFrames(h *harness) int {
	n := 0
	for _, f := range h.relay.Frames() {
		if f.From == "remote" && len(f.Data) == 1+pubLen && f.Data[0] == frameHelloRemote {
			n++
		}
	}
	return n
}

// (AC1) The remote re-sends its key half until local's arrives, so a hello the
// relay dropped (no local was there) is asked again; it stops when answered; and
// local ignores a hello it has already answered.
func TestHelloResendAndDuplicateDiscard(t *testing.T) {
	h := newHarness(t, true)
	id, _ := NewIdentity()
	o := newOptions(h.opts())
	ctx := testCtx(t)
	const session = "nod_hello"

	rconn, err := dialRemote(ctx, o, h.relay.URL(), session, "node-token")
	if err != nil {
		t.Fatalf("dial remote: %v", err)
	}
	type hs struct {
		send *sealer
		recv *opener
		err  error
	}
	remoteDone := make(chan hs, 1)
	go func() {
		send, recv, err := joinHandshake(ctx, rconn, session, id, "node-token", o, HelloResend)
		remoteDone <- hs{send, recv, err}
	}()

	// No local end: every hello is dropped, and asked again each HelloResend.
	eventually(t, "the remote's first hello", func() bool { return h.relay.Dropped() >= 1 })
	for want := 2; want <= 4; want++ {
		eventually(t, "the resend ticker", func() bool {
			for _, d := range h.clock.tickLog() {
				if d == HelloResend {
					return true
				}
			}
			return false
		})
		h.clock.Advance(HelloResend)
		eventually(t, fmt.Sprintf("hello number %d", want), func() bool { return h.relay.Dropped() >= want })
	}

	lconn, err := dialLocal(ctx, h.client, o, h.relay.URL(), session)
	if err != nil {
		t.Fatalf("dial local: %v", err)
	}
	eventually(t, "local on the relay", func() bool { return h.relay.Upgrades() >= 2 })
	localDone := make(chan hs, 1)
	go func() {
		send, recv, err := handshakeLocal(ctx, lconn, session, id.Public(), o, func(appMsg) error { return nil })
		localDone <- hs{send, recv, err}
	}()
	var r, l hs
	h.drive(t, HelloResend, "both handshakes done", func() bool {
		select {
		case r = <-remoteDone:
			remoteDone = nil
		default:
		}
		select {
		case l = <-localDone:
			localDone = nil
		default:
		}
		return remoteDone == nil && localDone == nil
	})
	if r.err != nil || l.err != nil {
		t.Fatalf("remote handshake = %v, local handshake = %v", r.err, l.err)
	}

	// Answered: the remote has stopped asking.
	sent := helloFrames(h)
	h.clock.Advance(10 * HelloResend)
	time.Sleep(50 * time.Millisecond)
	if got := helloFrames(h); got != sent {
		t.Errorf("remote hellos went from %d to %d after local answered", sent, got)
	}

	// A stray hello on an established session is ignored, not an error.
	sl := newSession(ctx, RoleLocal, lconn, l.send, l.recv, o.clock, nil)
	sr := newSession(ctx, RoleRemote, rconn, r.send, r.recv, o.clock, nil)
	t.Cleanup(func() { sl.Close(); sr.Close() })
	stray := append([]byte{frameHelloRemote}, make([]byte, pubLen)...)
	if err := rconn.Write(ctx, websocket.MessageBinary, stray); err != nil {
		t.Fatal(err)
	}
	if err := sr.Send(ctx, "j", []byte("after-stray")); err != nil {
		t.Fatal(err)
	}
	if m, err := sl.Recv(ctx); err != nil || string(m.Body) != "after-stray" {
		t.Fatalf("Recv = %q, %v; want after-stray", m.Body, err)
	}
	if sl.Err() != nil {
		t.Errorf("a stray hello ended the session: %v", sl.Err())
	}
}

// (AC1) Local answers a key half only once: a duplicate of the one it answered
// is dropped in the middle of the handshake and the enrollment still lands.
func TestHandshakeLocalIgnoresADuplicateHello(t *testing.T) {
	h := newHarness(t, true)
	id, _ := NewIdentity()
	o := newOptions(h.opts())
	ctx := testCtx(t)
	const session = "nod_dup"

	lconn, err := dialLocal(ctx, h.client, o, h.relay.URL(), session)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "local on the relay", func() bool { return h.relay.Upgrades() >= 1 })
	rconn, err := dialRemote(ctx, o, h.relay.URL(), session, "node-token")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "remote on the relay", func() bool { return h.relay.Upgrades() >= 2 })
	done := make(chan error, 1)
	go func() {
		_, _, err := handshakeLocal(ctx, lconn, session, id.Public(), o, func(appMsg) error { return nil })
		done <- err
	}()

	eR, _ := newEphemeral()
	hello := append([]byte{frameHelloRemote}, eR.PublicKey().Bytes()...)
	if err := rconn.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	localHello, err := readFrame(ctx, rconn)
	if err != nil {
		t.Fatal(err)
	}
	if err := rconn.Write(ctx, websocket.MessageBinary, hello); err != nil { // the duplicate
		t.Fatal(err)
	}
	send, recv, err := remoteKeys(session, id, eR, localHello[1:])
	if err != nil {
		t.Fatal(err)
	}
	enroll, _ := json.Marshal(appMsg{T: msgEnroll, Token: "t"})
	if err := rconn.Write(ctx, websocket.MessageBinary, send.seal(enroll)); err != nil {
		t.Fatal(err)
	}
	reply, err := readFrame(ctx, rconn)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := recv.open(reply)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("handshakeLocal: %v", err)
	}
	if string(plain) != `{"t":"admit"}` {
		t.Errorf("reply = %s, want admit", plain)
	}
}

// (AC1) A job open at the restart is failed once on the waiting end, naming the
// restart; on the sending end its context is cancelled and its heartbeat stops;
// a stray late beat does not bring it back.
func TestResumeFailsOpenJobOnce(t *testing.T) {
	h := newHarness(t, true)
	rec := &memRecorder{}
	local, remote := h.pair(t, "nod_job", rec)
	ctx := testCtx(t)

	local.Expect("j1")
	job := remote.StartJob(ctx, "j1")
	mark, before := len(h.clock.tickLog()), h.relay.Upgrades()

	h.relay.Restart()
	h.awaitResume(t, mark, before, "both ends resumed", func() bool { return rec.count(ledger.KindRemoteChannelResumed) >= 2 })

	select {
	case l := <-local.Lost():
		if l.Job != "j1" || l.Reason != LostRelayRestarted {
			t.Fatalf("Lost = %+v, want j1 with reason %q", l, LostRelayRestarted)
		}
	case <-time.After(hangBound):
		t.Fatal("the open job was never reported lost")
	}
	if cause := context.Cause(job.Context()); !errors.Is(cause, ErrRelayRestarted) {
		t.Errorf("job context cause = %v, want ErrRelayRestarted", cause)
	}
	select {
	case <-job.exit:
	case <-time.After(hangBound):
		t.Fatal("the cut job kept heartbeating")
	}

	// A late beat for the failed job is not a second loss, and not a revival.
	if err := remote.sendApp(ctx, appMsg{T: msgHeartbeat, Job: "j1"}); err != nil {
		t.Fatal(err)
	}
	exchange(t, local, remote) // the forged beat has been read by now: frames arrive in order
	for i := 0; i < 6; i++ {
		h.clock.Advance(HeartbeatInterval)
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case l := <-local.Lost():
		t.Fatalf("Lost delivered again: %+v", l)
	default:
	}
	if _, ok := lastBeat(local, "j1"); ok {
		t.Error("a stray beat revived the failed job")
	}
	if err := job.Done(); err != nil {
		t.Errorf("Done on a cut job = %v, want nil", err)
	}
}

func TestClassifyClose(t *testing.T) {
	closeErr := func(code websocket.StatusCode, reason string) error {
		return fmt.Errorf("failed to get reader: %w", websocket.CloseError{Code: code, Reason: reason})
	}
	cases := []struct {
		name string
		err  error
		want closeClass
	}{
		{"going away", closeErr(websocket.StatusGoingAway, "server shutting down"), closeRestart},
		{"abnormal", closeErr(websocket.StatusAbnormalClosure, ""), closeRestart},
		{"internal error", closeErr(websocket.StatusInternalError, ""), closeRestart},
		{"service restart", closeErr(websocket.StatusServiceRestart, ""), closeRestart},
		{"try again later", closeErr(websocket.StatusTryAgainLater, ""), closeRestart},
		{"EOF", fmt.Errorf("failed to read frame header: %w", io.EOF), closeRestart},
		{"unexpected EOF", io.ErrUnexpectedEOF, closeRestart},
		{"net error", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, closeRestart},
		{"refused dial", fmt.Errorf("remotechan: dial relay: %w", syscall.ECONNREFUSED), closeRestart},
		{"503", &RelayStatusError{Status: 503}, closeRestart},
		{"502", &RelayStatusError{Status: 502}, closeRestart},
		{"429", &RelayStatusError{Status: 429}, closeRestart},

		{"4401 signed out", closeErr(4401, "signed out"), closeRefusal},
		{"bad_session close", closeErr(4404, "bad_session"), closeRefusal},
		{"slot_taken close", closeErr(websocket.StatusGoingAway, "slot_taken"), closeRefusal},
		{"policy violation", closeErr(websocket.StatusPolicyViolation, ""), closeRefusal},
		{"401", &RelayStatusError{Status: 401}, closeRefusal},
		{"403", &RelayStatusError{Status: 403}, closeRefusal},
		{"404", &RelayStatusError{Status: 404, Body: "bad_session"}, closeRefusal},
		{"409", &RelayStatusError{Status: 409}, closeRefusal},
		{"slot_taken body", &RelayStatusError{Status: 400, Body: `{"error":"slot_taken"}`}, closeRefusal},
		{"login required", hosted.ErrLoginRequired, closeRefusal},
		{"session token refused", fmt.Errorf("dial: %w", hosted.ErrSessionTokenRefused), closeRefusal},

		{"normal close", closeErr(websocket.StatusNormalClosure, "peer left"), closeOther},
		{"400", &RelayStatusError{Status: 400}, closeOther},
		{"cancelled", context.Canceled, closeOther},
		{"deadline", fmt.Errorf("dial: %w", context.DeadlineExceeded), closeOther},
		{"key mismatch", ErrKeyMismatch, closeOther},
		{"bad frame", fmt.Errorf("%w: text message", ErrFrame), closeOther},
		{"enroll refused", fmt.Errorf("%w: token_used", ErrEnrollRefused), closeOther},
	}
	for _, c := range cases {
		if got := classifyClose(c.err); got != c.want {
			t.Errorf("%s: class = %d, want %d", c.name, got, c.want)
		}
	}
	if !(&RelayStatusError{Status: 404}).Refusal() || (&RelayStatusError{Status: 503}).Refusal() {
		t.Error("Refusal: 404 must be a refusal and 503 must not")
	}
}

// (AC2) A close or an answer that means refusal is not retried: the session ends
// naming it, the relay sees no further upgrade, and the clock running past the
// window changes nothing.
func TestResumeRefusalNotRetried(t *testing.T) {
	cases := []struct {
		name        string
		refuse      func(h *harness, session string)
		maxAttempts int // upgrade requests the refusal itself may cost
	}{
		{"4401 close", func(h *harness, s string) { h.relay.CloseWith(s, 4401, "signed out") }, 0},
		{"bad_session close", func(h *harness, s string) { h.relay.CloseWith(s, 4404, "bad_session") }, 0},
		{"404 bad_session", func(h *harness, s string) {
			h.relay.RefuseSession(s, 404, `{"error":"bad_session"}`)
			h.relay.Restart()
		}, 2},
		{"409 slot_taken", func(h *harness, s string) {
			h.relay.RefuseSession(s, 409, `{"error":"slot_taken"}`)
			h.relay.Restart()
		}, 2},
		{"401 signed out", func(h *harness, s string) {
			h.relay.RefuseSession(s, 401, `{"error":"invalid_token"}`)
			h.relay.Restart()
		}, 3}, // local refreshes its bearer and tries once more
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, true)
			rec := &memRecorder{}
			const session = "nod_refused"
			local, remote := h.pair(t, session, rec)
			base := h.relay.Attempts()

			c.refuse(h, session)
			eventually(t, "both sessions ended", bothDone(local, remote))

			for name, s := range map[string]*Session{"local": local, "remote": remote} {
				if err := s.Err(); !errors.Is(err, ErrResumeRefused) {
					t.Errorf("%s Err = %v, want ErrResumeRefused", name, err)
				}
			}
			after := h.relay.Attempts()
			if after-base > c.maxAttempts {
				t.Errorf("the refusal cost %d upgrade requests, want at most %d", after-base, c.maxAttempts)
			}
			for i := 0; i < 30; i++ { // well past the window, in backoff-sized steps
				h.clock.Advance(15 * time.Second)
			}
			time.Sleep(100 * time.Millisecond)
			if got := h.relay.Attempts(); got != after {
				t.Errorf("attempts went from %d to %d after the refusal: it was retried", after, got)
			}
			rows := rec.payloads(ledger.KindRemoteChannelResumeFailed)
			if len(rows) != 2 || rows[0]["reason"] != "refused" || rows[1]["reason"] != "refused" {
				t.Errorf("resume_failed rows = %v, want two refused", rows)
			}
			if n := rec.count(ledger.KindRemoteChannelResumed); n != 0 {
				t.Errorf("%d resumed rows for a refused session", n)
			}
		})
	}
}
