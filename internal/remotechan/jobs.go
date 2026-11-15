package remotechan

import (
	"context"
	"errors"
	"sync"
	"time"
)

const HeartbeatInterval = 5 * time.Second

const LostAfter = 3 * HeartbeatInterval

const LostRelayRestarted = "relay_restarted"

var ErrRelayRestarted = errors.New("remotechan: the relay restarted mid-job")

type JobLost struct {
	Job      string
	LastBeat time.Time
	Reason   string
}

type jobTable struct {
	mu     sync.Mutex
	last   map[string]time.Time
	failed map[string]struct{}
}

func newJobTable() *jobTable {
	return &jobTable{last: map[string]time.Time{}, failed: map[string]struct{}{}}
}

func (t *jobTable) beat(job string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.failed[job]; ok {
		return
	}
	t.last[job] = now
}

func (t *jobTable) done(job string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.last, job)
}

func (t *jobTable) lost(now time.Time) []JobLost {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []JobLost
	for job, at := range t.last {
		if now.Sub(at) > LostAfter {
			out = append(out, JobLost{Job: job, LastBeat: at})
			delete(t.last, job)
		}
	}
	return out
}

func (t *jobTable) failAll() []JobLost {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []JobLost
	for job, at := range t.last {
		out = append(out, JobLost{Job: job, LastBeat: at, Reason: LostRelayRestarted})
		t.failed[job] = struct{}{}
		delete(t.last, job)
	}
	return out
}

type Job struct {
	name   string
	s      *Session
	ctx    context.Context
	cancel context.CancelCauseFunc
	stop   chan struct{}
	exit   chan struct{}
	once   sync.Once
}

func (s *Session) StartJob(ctx context.Context, job string) *Job {
	jctx, cancel := context.WithCancelCause(ctx)
	j := &Job{name: job, s: s, ctx: jctx, cancel: cancel, stop: make(chan struct{}), exit: make(chan struct{})}
	s.trackJob(j, true)
	go j.beat()
	return j
}

func (j *Job) Context() context.Context { return j.ctx }

func (j *Job) beat() {
	defer close(j.exit)
	tick, stopTick := j.s.clock.Tick(HeartbeatInterval)
	defer stopTick()
	for {
		if err := j.s.sendApp(j.ctx, appMsg{T: msgHeartbeat, Job: j.name}); err != nil {
			return
		}
		select {
		case <-j.stop:
			return
		case <-j.ctx.Done():
			return
		case <-j.s.done:
			return
		case <-tick:
		}
	}
}

func (j *Job) halt() { j.once.Do(func() { close(j.stop) }) }

func (j *Job) Done() error {
	j.halt()
	<-j.exit
	j.s.trackJob(j, false)
	cut := errors.Is(context.Cause(j.ctx), ErrRelayRestarted)
	j.cancel(context.Canceled)
	if cut {
		return nil
	}
	return j.s.sendApp(j.s.ctx, appMsg{T: msgDone, Job: j.name})
}
