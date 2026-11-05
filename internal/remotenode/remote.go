package remotenode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/remotechan"
)

// ErrJobLost: a job's heartbeats stopped, or the channel ended, before the node
// answered it.
var ErrJobLost = errors.New("remotenode: job lost")

// Run is the controller's handle on an attached node: the report it gave and the
// channel to it. End it with End.
type Run struct {
	Report Report

	sess *remotechan.Session

	mu      sync.Mutex
	pending map[string]chan result
	ended   bool
}

type result struct {
	body json.RawMessage
	err  error
}

// AttachRun is the controller's end of an enrolled channel: it waits up to
// readyTimeout for the node's ready report and starts routing the node's replies
// and heartbeats to Request. sess is the channel remotechan.Attach returned for
// a node satelle-server started (`satelle node join`). An error of the node's own, or a first message that is not its ready
// report, fails the attach and closes sess.
func AttachRun(ctx context.Context, sess *remotechan.Session, readyTimeout time.Duration) (*Run, error) {
	r := &Run{sess: sess, pending: map[string]chan result{}}
	if err := r.begin(ctx, readyTimeout); err != nil {
		_ = sess.Close()
		return nil, err
	}
	return r, nil
}

// begin reads the ready report and starts the reply routing.
func (r *Run) begin(ctx context.Context, readyTimeout time.Duration) error {
	rctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	m, err := r.sess.Recv(rctx)
	if err != nil {
		return fmt.Errorf("the node did not report ready: %w", err)
	}
	env, err := decode(m.Body)
	if err != nil || env.Type != typeReady {
		return errors.New("the node's first message was not its ready report")
	}
	if err := json.Unmarshal(env.Body, &r.Report); err != nil {
		return fmt.Errorf("the node's report is malformed: %w", err)
	}
	if r.Report.Error != "" {
		return fmt.Errorf("the node is not ready: %s", r.Report.Error)
	}

	go r.pump()
	go r.watchLost()
	return nil
}

// Request sends the node one job of jobType carrying body and waits for its
// reply. The job heartbeats on the channel while the node works; if its
// heartbeats stop or the channel ends first, Request returns an error wrapping
// ErrJobLost. A job type is any name the node registered a handler for.
func (r *Run) Request(ctx context.Context, jobType string, body any) (json.RawMessage, error) {
	payload, err := encode(jobType, body)
	if err != nil {
		return nil, err
	}
	id, err := newJobID()
	if err != nil {
		return nil, err
	}
	ch := make(chan result, 1)
	r.mu.Lock()
	if r.ended {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: the run has ended", ErrJobLost)
	}
	r.pending[id] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
	}()

	r.sess.Expect(id)
	if err := r.sess.Send(ctx, id, payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJobLost, err)
	}
	select {
	case res := <-ch:
		return res.body, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pump routes each reply to the request waiting on its job, and fails every
// waiting request when the channel ends.
func (r *Run) pump() {
	for {
		m, err := r.sess.Recv(context.Background())
		if err != nil {
			r.failAll(fmt.Errorf("%w: the channel ended: %v", ErrJobLost, err))
			return
		}
		if m.Job == "" {
			continue
		}
		var res result
		env, derr := decode(m.Body)
		var rep reply
		switch {
		case derr != nil:
			res.err = derr
		case env.Type != typeReply:
			res.err = fmt.Errorf("remotenode: unexpected %q message in job %s", env.Type, m.Job)
		case json.Unmarshal(env.Body, &rep) != nil:
			res.err = errors.New("remotenode: malformed reply")
		case rep.Error != "":
			res.err = errors.New(rep.Error)
		default:
			res.body = rep.Body
		}
		r.deliver(m.Job, res)
	}
}

// watchLost turns the channel's lost-job reports into failed requests.
func (r *Run) watchLost() {
	for {
		select {
		case l := <-r.sess.Lost():
			err := fmt.Errorf("%w: no heartbeat since %s", ErrJobLost, l.LastBeat.UTC().Format(time.RFC3339))
			if l.Reason == remotechan.LostRelayRestarted {
				err = fmt.Errorf("%w: relay restarted mid-job", ErrJobLost)
			}
			r.deliver(l.Job, result{err: err})
		case <-r.sess.Done():
			return
		}
	}
}

func (r *Run) deliver(job string, res result) {
	r.mu.Lock()
	ch := r.pending[job]
	r.mu.Unlock()
	if ch == nil {
		return // a finished job's late heartbeat or done
	}
	select {
	case ch <- res:
	default:
	}
}

func (r *Run) failAll(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.pending {
		select {
		case ch <- result{err: err}:
		default:
		}
	}
}

// End closes the channel to the node. It is safe to call more than once; only the
// first does anything.
func (r *Run) End() error {
	r.mu.Lock()
	if r.ended {
		r.mu.Unlock()
		return nil
	}
	r.ended = true
	r.mu.Unlock()
	return r.sess.Close()
}

func newJobID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "job-" + hex.EncodeToString(b), nil
}
