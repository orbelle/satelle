package remotechan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/hosted"
)

const (
	ResumeWindow = 2 * time.Minute
	HelloResend  = 250 * time.Millisecond

	resumeBackoffMin = 500 * time.Millisecond
	resumeBackoffMax = 15 * time.Second
)

var (
	ErrResumeRefused = errors.New("remotechan: the relay refused the redial")
	ErrResumeExpired = errors.New("remotechan: the relay did not come back within the resume window")
)

type resumer struct {
	session string
	window  time.Duration
	rec     Recorder
	redial  func(ctx context.Context) (*conn, *sealer, *opener, error)
}

type closeClass int

const (
	closeOther closeClass = iota
	closeRestart
	closeRefusal
)

func classifyClose(err error) closeClass {
	switch {
	case errors.Is(err, hosted.ErrLoginRequired), errors.Is(err, hosted.ErrSessionTokenRefused), errors.Is(err, hosted.ErrNodeTokenRefused):
		return closeRefusal
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, ErrFrame), errors.Is(err, ErrKeyMismatch), errors.Is(err, ErrEnrollRefused), errors.Is(err, errOpen),
		errors.Is(err, ErrClosed):
		return closeOther
	case errors.Is(err, errStreamEnded):
		return closeRestart
	}
	st, ok := status.FromError(err)
	if !ok {
		return closeRestart
	}
	switch st.Code() {
	case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.AlreadyExists,
		codes.FailedPrecondition, codes.InvalidArgument:
		return closeRefusal
	case codes.Unavailable, codes.Aborted, codes.ResourceExhausted, codes.Internal, codes.Unknown:
		return closeRestart
	}
	return closeOther
}

func (s *Session) resume(cause error) bool {
	if s.closing.Load() || s.ctx.Err() != nil {
		return false
	}
	class := classifyClose(cause)
	if s.res == nil || class == closeOther {
		s.finish(fmt.Errorf("%w: %v", ErrClosed, cause))
		return false
	}
	if class == closeRefusal {
		s.giveUp("refused", 0, fmt.Errorf("%w: %v", ErrResumeRefused, cause))
		return false
	}

	s.wmu.Lock()
	s.gate = make(chan struct{})
	s.wmu.Unlock()
	s.conn.Load().CloseNow()
	s.cutJobs()

	deadline := s.clock.Now().Add(s.res.window)
	for attempt := 0; ; attempt++ {
		if attempt > 0 && !s.backoff(attempt-1) {
			return false
		}
		remaining := deadline.Sub(s.clock.Now())
		if remaining <= 0 {
			s.giveUp("expired", attempt, fmt.Errorf("%w (%v)", ErrResumeExpired, s.res.window))
			return false
		}
		actx, cancel := clockTimeout(s.ctx, s.clock, remaining)
		conn, send, recv, err := s.res.redial(actx)
		timedOut := actx.Err() != nil
		cancel()
		if err == nil {
			return s.swap(conn, send, recv, attempt+1)
		}
		if s.ctx.Err() != nil {
			return false
		}
		switch class := classifyClose(err); {
		case class == closeRefusal:
			s.giveUp("refused", attempt+1, fmt.Errorf("%w: %v", ErrResumeRefused, err))
			return false
		case timedOut:
			s.giveUp("expired", attempt+1, fmt.Errorf("%w (%v): %v", ErrResumeExpired, s.res.window, err))
			return false
		case class == closeOther:
			s.giveUp("failed", attempt+1, fmt.Errorf("%w: %v", ErrClosed, err))
			return false
		}
	}
}

func (s *Session) backoff(n int) bool {
	d := resumeBackoffMin
	for i := 0; i < n && d < resumeBackoffMax; i++ {
		d *= 2
	}
	d = min(d, resumeBackoffMax)
	tick, stop := s.clock.Tick(d)
	defer stop()
	select {
	case <-tick:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *Session) swap(c *conn, send *sealer, recv *opener, attempts int) bool {
	s.wmu.Lock()
	select {
	case <-s.done:
		s.wmu.Unlock()
		c.CloseNow()
		return false
	default:
	}
	s.send, s.recv = send, recv
	s.conn.Store(c)
	close(s.gate)
	s.gate = nil
	s.wmu.Unlock()
	s.record(KindRemoteChannelResumed, "", attempts)
	return true
}

func (s *Session) giveUp(reason string, attempts int, err error) {
	s.record(KindRemoteChannelResumeFailed, reason, attempts)
	s.finish(err)
}

func (s *Session) record(kind, reason string, attempts int) {
	if s.res == nil || s.res.rec == nil {
		return
	}
	p := map[string]any{"session": s.res.session, "role": s.role.String(), "attempts": attempts}
	if reason != "" {
		p["reason"] = reason
	}
	payload, _ := json.Marshal(p)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
	defer cancel()
	_ = s.res.rec.Record(ctx, Record{Kind: kind, Actor: "remotechan", Body: reason, Payload: payload}, s.clock.Now())
}

func (s *Session) trackJob(j *Job, add bool) {
	s.jmu.Lock()
	defer s.jmu.Unlock()
	if add {
		s.open[j] = struct{}{}
		return
	}
	delete(s.open, j)
}

func (s *Session) cutJobs() {
	s.jmu.Lock()
	for j := range s.open {
		j.cancel(ErrRelayRestarted)
	}
	s.jmu.Unlock()
	for _, l := range s.jobs.failAll() {
		select {
		case s.lost <- l:
		case <-s.ctx.Done():
			return
		}
	}
}

func clockTimeout(parent context.Context, clock Clock, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	tick, stop := clock.Tick(d)
	go func() {
		defer stop()
		select {
		case <-tick:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
