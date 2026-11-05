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

// A relay that restarts ends every stream and keeps none. The two ends of a
// session therefore redial the same session id and handshake again (a fresh key
// exchange, the same pinned node key), for at most ResumeWindow.
const (
	// ResumeWindow is how long an end keeps redialling before it gives up.
	ResumeWindow = 2 * time.Minute
	// HelloResend is how often the remote re-sends its key half while it waits for
	// local's, so the handshake completes whichever end the relay admits first.
	HelloResend = 250 * time.Millisecond

	resumeBackoffMin = 500 * time.Millisecond
	resumeBackoffMax = 15 * time.Second
)

var (
	// ErrResumeRefused ends a session whose redial the relay refused (signed out,
	// a revoked node token, a session it does not know): retrying cannot help.
	ErrResumeRefused = errors.New("remotechan: the relay refused the redial")
	// ErrResumeExpired ends a session that could not redial within ResumeWindow.
	ErrResumeExpired = errors.New("remotechan: the relay did not come back within the resume window")
)

// resumer is how a Session redials: dial and handshake again, with the identity
// the first handshake used. ctx is bounded by what is left of the window.
type resumer struct {
	session string
	window  time.Duration
	rec     Recorder
	redial  func(ctx context.Context) (*conn, *sealer, *opener, error)
}

type closeClass int

const (
	closeOther   closeClass = iota // an ordinary end: the session ends with it
	closeRestart                   // a restart explains it: redial
	closeRefusal                   // the relay said no: do not redial
)

// classifyClose says what a stream error means for the session. A restart
// (the relay unavailable or gone) is worth redialling; a refusal (signed out, a
// token or session the relay does not accept) never is; anything else ends the
// session.
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
		return closeRestart // a network error
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

// resume is called by readLoop when the connection failed with cause. It
// returns true once a new connection is swapped in, and false when the session
// has ended.
func (s *Session) resume(cause error) bool {
	if s.closing.Load() || s.ctx.Err() != nil {
		return false // Close ended it: never redial, and leave Close to record ErrClosed
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

	// The relay restarted. Hold writes, drop the dead connection, and fail what was
	// in flight: replies lost in the gap cannot be recovered across a fresh handshake.
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

// backoff waits out the n-th redial delay (500ms, 1s, 2s … capped at 15s). It
// returns false if the session ended meanwhile.
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

// swap installs a redialled connection and lets blocked writers through.
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

// giveUp ends the session with err after recording why.
func (s *Session) giveUp(reason string, attempts int, err error) {
	s.record(KindRemoteChannelResumeFailed, reason, attempts)
	s.finish(err)
}

// record writes one resume row when the options carry a Recorder. A failed write
// is dropped: evidence must not stop the channel.
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

// trackJob adds or removes a sending-end job from the set a resume cuts off.
func (s *Session) trackJob(j *Job, add bool) {
	s.jmu.Lock()
	defer s.jmu.Unlock()
	if add {
		s.open[j] = struct{}{}
		return
	}
	delete(s.open, j)
}

// cutJobs fails every job open at a drop, on both ends: a sending-end job's
// context is cancelled with ErrRelayRestarted, and a job being waited on is
// delivered once on Lost with LostRelayRestarted.
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

// clockTimeout is context.WithTimeout on the session's clock, so a test that
// advances a fake clock moves the deadline too.
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
