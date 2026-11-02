// Package remotechan is the sealed channel between local satelle and an agent on
// a remote it started. Both ends dial OUT to a relay (wss on 443) and neither
// listens, so a corporate network needs no extra rule. The relay pairs the two
// ends by session id and forwards bytes; it cannot read them.
//
// # Relay contract (what the satelle-server relay must do)
//
// A relay serves GET <relayURL>/<session>?role=local|remote as a WebSocket
// upgrade. The local end sends "Authorization: Bearer <satelle.dev access
// token>"; the relay answers 401 for a bearer it does not accept, and the local
// end then refreshes through hosted.Client.WithBearer and retries once. The
// remote end sends "Authorization: Bearer <node token>", the token satelle.dev
// gave the node it started; the relay answers 401 for one it does not accept and
// admits an accepted remote into its session whether or not a local end is
// waiting. The remote still proves itself to the local end, with its pinned key
// and the enrollment token, not to the relay. Every binary message from one end
// is forwarded unchanged to the other; the relay never needs to parse one.
//
// A relay restart closes every session and keeps none. The two ends of a
// server-minted session (AttachServerSession, Join) therefore redial the same
// session id with backoff, for at most ResumeWindow, and handshake again; the
// relay admits either end first. A going-away or abnormal close, or a network
// error, is a restart; a 4401 close, a bad_session or slot_taken answer, or a
// 401, 403, 404 or 409 on the upgrade is a refusal and is never retried (see
// classifyClose). A job open at the drop is failed, not resumed.
//
// # Wire protocol
//
// Binary messages only. remote → local: [1][remote ephemeral X25519 public
// key]; local → remote: [2][local ephemeral public key]; then both ways
// [3][seq, 8 bytes][AES-256-GCM ciphertext]. The keys come from HKDF-SHA256
// over the ephemeral–ephemeral DH and the local ephemeral–node-static DH, the
// latter against the key local satelle pinned at launch (seal.go). Everything
// after the two key halves is sealed, including the enrollment token and the
// heartbeats; the relay and any proxy see only types, lengths and sequence
// numbers.
//
// Enrollment: the remote's first sealed message is {t:enroll, token}. Local
// satelle opens it (a failure means the remote does not hold the pinned key:
// ErrKeyMismatch), redeems the token (single use, EnrollTTL), and answers
// {t:admit} or {t:refuse}. Nothing the remote sends before it is admitted is
// delivered to the caller.
package remotechan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// maxFrameBytes bounds one relay message in either direction.
const maxFrameBytes = 1 << 20

// Role is which end of the channel a Session is.
type Role byte

const (
	// RoleLocal is local satelle, the end that launched the node.
	RoleLocal Role = iota + 1
	// RoleRemote is the agent on the remote.
	RoleRemote
)

func (r Role) String() string {
	if r == RoleRemote {
		return "remote"
	}
	return "local"
}

// The application messages carried inside sealed frames.
const (
	msgEnroll    = "enroll"
	msgAdmit     = "admit"
	msgRefuse    = "refuse"
	msgData      = "msg"
	msgHeartbeat = "hb"
	msgDone      = "done"
)

// appMsg is what a sealed frame carries. Only the relay's view of it — a
// ciphertext — leaves the process.
type appMsg struct {
	T      string `json:"t"`
	Job    string `json:"job,omitempty"`
	Body   []byte `json:"body,omitempty"`
	Token  string `json:"token,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Message is an application message exchanged over a Session. Job names the job
// it belongs to, or is empty.
type Message struct {
	Job  string
	Body []byte
}

// ErrClosed is returned by Send and Recv once the Session has ended.
var ErrClosed = errors.New("remotechan: session closed")

// Session is an admitted, sealed channel to the other end. It is safe for
// concurrent use.
type Session struct {
	role  Role
	conn  atomic.Pointer[websocket.Conn] // swapped under wmu by a resume
	clock Clock
	res   *resumer // nil: the session ends with its connection

	ctx    context.Context
	cancel context.CancelFunc

	wmu  sync.Mutex // serialises seal + write so sequence numbers reach the relay in order; guards send, gate and the conn swap
	send *sealer
	gate chan struct{} // non-nil while a resume is in flight; closed when it ends
	recv *opener       // read by readLoop only

	inbox chan Message
	jobs  *jobTable
	lost  chan JobLost

	jmu  sync.Mutex
	open map[*Job]struct{} // sending-end jobs in flight, cut off by a resume

	closing atomic.Bool // set by Close: a read error from here on is our own end, never a restart
	done    chan struct{}
	errOnce sync.Once
	err     error
}

// newSession wraps an admitted connection and starts its loops. The session
// outlives the ctx of the call that opened it (a per-call timeout must not end
// it); Close ends it. res, when set, lets it redial the same session after the
// relay drops the connection.
func newSession(ctx context.Context, role Role, conn *websocket.Conn, send *sealer, recv *opener, clock Clock, res *resumer) *Session {
	conn.SetReadLimit(maxFrameBytes)
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &Session{
		role: role, clock: clock, ctx: ctx, cancel: cancel, res: res,
		send: send, recv: recv,
		inbox: make(chan Message, 64), jobs: newJobTable(), lost: make(chan JobLost, 16),
		open: map[*Job]struct{}{}, done: make(chan struct{}),
	}
	s.conn.Store(conn)
	go s.readLoop()
	go s.monitor()
	return s
}

// Role is which end this is.
func (s *Session) Role() Role { return s.role }

// Send delivers body to the other end, sealed. job names the job it belongs to
// ("" for none).
func (s *Session) Send(ctx context.Context, job string, body []byte) error {
	return s.sendApp(ctx, appMsg{T: msgData, Job: job, Body: body})
}

// Recv returns the next message from the other end.
func (s *Session) Recv(ctx context.Context) (Message, error) {
	select {
	case m := <-s.inbox:
		return m, nil
	case <-s.done:
		// Drain what arrived before the end.
		select {
		case m := <-s.inbox:
			return m, nil
		default:
		}
		return Message{}, s.Err()
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

// Expect starts waiting on job's heartbeats: from now, a job silent for longer
// than LostAfter is reported on Lost. Call it when the job is requested, so a
// job that never sends its first heartbeat is caught too.
func (s *Session) Expect(job string) { s.jobs.beat(job, s.clock.Now()) }

// Lost delivers each job whose heartbeats stopped, once, even while the
// connection stays up.
func (s *Session) Lost() <-chan JobLost { return s.lost }

// Done is closed when the session ends.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the session ended (ErrClosed after Close), or nil while it is up.
func (s *Session) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

// Close ends the session. The normal close frame is written while the read loop's
// context is still live: cancelling that context first makes the library drop the
// socket with no frame, which the relay passes on as a vanished end and the other
// end takes for a relay restart, redialling for up to ResumeWindow.
func (s *Session) Close() error {
	s.closing.Store(true)
	err := s.conn.Load().Close(websocket.StatusNormalClosure, "")
	s.finish(ErrClosed)
	return err
}

// finish records why the session ended, once, and wakes everything waiting on it.
func (s *Session) finish(err error) {
	s.errOnce.Do(func() {
		s.err = err
		close(s.done)
		s.cancel()
	})
}

// sendApp seals one application message and writes it. While the session is
// redialling it waits for the resume, bounded by ctx and by the session's end:
// it never writes to a connection the relay has dropped.
func (s *Session) sendApp(ctx context.Context, m appMsg) error {
	plain, err := json.Marshal(m)
	if err != nil {
		return err
	}
	for {
		s.wmu.Lock()
		select {
		case <-s.done:
			s.wmu.Unlock()
			return ErrClosed
		default:
		}
		if gate := s.gate; gate != nil {
			s.wmu.Unlock()
			select {
			case <-gate:
				continue
			case <-s.done:
				return ErrClosed
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err := s.conn.Load().Write(ctx, websocket.MessageBinary, s.send.seal(plain))
		s.wmu.Unlock()
		return err
	}
}

// readLoop reads the session's frames and, when the relay drops the connection
// in a way a restart explains, redials and reads on.
func (s *Session) readLoop() {
	for {
		err := s.readFrames()
		if err == nil || !s.resume(err) {
			return
		}
	}
}

// readFrames reads the current connection until it fails. A nil return means
// the session itself ended (a bad frame, or its ctx); a non-nil one is the
// connection's own failure.
func (s *Session) readFrames() error {
	conn := s.conn.Load()
	for {
		_, frame, err := conn.Read(s.ctx)
		if err != nil {
			return err
		}
		if s.role == RoleLocal && len(frame) == 1+pubLen && frame[0] == frameHelloRemote {
			continue // a hello the remote re-sent while it waited for us
		}
		plain, err := s.recv.open(frame)
		if err != nil {
			s.finish(err)
			conn.Close(websocket.StatusPolicyViolation, "bad frame")
			return nil
		}
		var m appMsg
		if json.Unmarshal(plain, &m) != nil {
			s.finish(fmt.Errorf("%w: malformed message", ErrFrame))
			conn.Close(websocket.StatusPolicyViolation, "bad message")
			return nil
		}
		switch m.T {
		case msgData:
			select {
			case s.inbox <- Message{Job: m.Job, Body: m.Body}:
			case <-s.ctx.Done():
				return nil
			}
		case msgHeartbeat:
			s.jobs.beat(m.Job, s.clock.Now())
		case msgDone:
			s.jobs.done(m.Job)
		}
	}
}

// monitor reports jobs gone quiet, every HeartbeatInterval.
func (s *Session) monitor() {
	tick, stop := s.clock.Tick(HeartbeatInterval)
	defer stop()
	for {
		select {
		case <-s.done:
			return
		case <-tick:
			for _, l := range s.jobs.lost(s.clock.Now()) {
				select {
				case s.lost <- l:
				case <-s.done:
					return
				}
			}
		}
	}
}
