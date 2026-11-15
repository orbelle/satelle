package remotechan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

const maxFrameBytes = 1 << 20

type Role byte

const (
	RoleLocal Role = iota + 1
	RoleRemote
)

func (r Role) String() string {
	if r == RoleRemote {
		return "remote"
	}
	return "local"
}

const (
	msgEnroll    = "enroll"
	msgAdmit     = "admit"
	msgRefuse    = "refuse"
	msgData      = "msg"
	msgHeartbeat = "hb"
	msgDone      = "done"
	msgBye       = "bye"
)

type appMsg struct {
	T      string `json:"t"`
	Job    string `json:"job,omitempty"`
	Body   []byte `json:"body,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type Message struct {
	Job  string
	Body []byte
}

var ErrClosed = errors.New("remotechan: session closed")

type Session struct {
	role  Role
	conn  atomic.Pointer[conn]
	clock Clock
	res   *resumer

	ctx    context.Context
	cancel context.CancelFunc

	wmu  sync.Mutex
	send *sealer
	gate chan struct{}
	recv *opener

	inbox chan Message
	jobs  *jobTable
	lost  chan JobLost

	jmu  sync.Mutex
	open map[*Job]struct{}

	closing atomic.Bool
	done    chan struct{}
	errOnce sync.Once
	err     error
}

func newSession(ctx context.Context, role Role, c *conn, send *sealer, recv *opener, clock Clock, res *resumer) *Session {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &Session{
		role: role, clock: clock, ctx: ctx, cancel: cancel, res: res,
		send: send, recv: recv,
		inbox: make(chan Message, 64), jobs: newJobTable(), lost: make(chan JobLost, 16),
		open: map[*Job]struct{}{}, done: make(chan struct{}),
	}
	s.conn.Store(c)
	go s.readLoop()
	go s.monitor()
	return s
}

func (s *Session) Role() Role { return s.role }

func (s *Session) Send(ctx context.Context, job string, body []byte) error {
	return s.sendApp(ctx, appMsg{T: msgData, Job: job, Body: body})
}

func (s *Session) Recv(ctx context.Context) (Message, error) {
	select {
	case m := <-s.inbox:
		return m, nil
	case <-s.done:
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

func (s *Session) Expect(job string) { s.jobs.beat(job, s.clock.Now()) }

func (s *Session) Lost() <-chan JobLost { return s.lost }

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

func (s *Session) Close() error {
	if !s.closing.CompareAndSwap(false, true) {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, closeGrace)
	err := s.sendApp(ctx, appMsg{T: msgBye})
	cancel()
	s.conn.Load().Close()
	s.finish(ErrClosed)
	if errors.Is(err, ErrClosed) {
		err = nil
	}
	return err
}

func (s *Session) finish(err error) {
	s.errOnce.Do(func() {
		s.err = err
		close(s.done)
		s.cancel()
	})
}

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
		err := s.conn.Load().Write(ctx, s.send.seal(plain))
		s.wmu.Unlock()
		return err
	}
}

func (s *Session) readLoop() {
	for {
		err := s.readFrames()
		if err == nil || !s.resume(err) {
			return
		}
	}
}

func (s *Session) readFrames() error {
	c := s.conn.Load()
	for {
		frame, err := c.Read(s.ctx)
		if err != nil {
			return err
		}
		if s.role == RoleLocal && len(frame) == 1+pubLen && frame[0] == frameHelloRemote {
			continue
		}
		plain, err := s.recv.open(frame)
		if err != nil {
			s.finish(err)
			c.CloseNow()
			return nil
		}
		var m appMsg
		if json.Unmarshal(plain, &m) != nil {
			s.finish(fmt.Errorf("%w: malformed message", ErrFrame))
			c.CloseNow()
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
		case msgBye:
			s.closing.Store(true)
			c.CloseNow()
			s.finish(fmt.Errorf("%w: the other end closed", ErrClosed))
			return nil
		}
	}
}

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
