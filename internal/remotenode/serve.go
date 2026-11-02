package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/bobmcallan/satelle/internal/remotechan"
)

// Handler serves one job type on the node: it receives the request body and
// returns the reply body.
type Handler func(ctx context.Context, body json.RawMessage) (json.RawMessage, error)

// Node is the node's end of a run. Register job types with Handle, then Serve.
type Node struct {
	mu       sync.Mutex
	handlers map[string]Handler
	active   atomic.Int32 // jobs whose handler is running
}

// What a node reports as its heartbeat step while it waits for a job and while a
// job's handler runs.
const (
	StepWaiting = "waiting"
	StepWorking = "working"
)

// Status is the node's heartbeat now: running, with a step that says whether a job
// is in flight. It is what StartHeartbeat reports.
func (n *Node) Status() NodeStatus {
	if n.active.Load() > 0 {
		return NodeStatus{State: StateRunning, Step: StepWorking}
	}
	return NodeStatus{State: StateRunning, Step: StepWaiting}
}

// NewNode returns a Node with no job types.
func NewNode() *Node { return &Node{handlers: map[string]Handler{}} }

// Handle registers the handler for a job type. A later story adds its own type
// here; an unregistered type is answered as unsupported, never ignored.
func (n *Node) Handle(jobType string, h Handler) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.handlers[jobType] = h
}

func (n *Node) handler(jobType string) (Handler, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	h, ok := n.handlers[jobType]
	return h, ok
}

// Serve runs the node's side of an admitted session until it ends: it sends the
// ready report, then answers every job request. Each job heartbeats while its
// handler runs and ends with its reply. It returns nil when local satelle closed
// the channel. A report with Error set says why the node could not become ready;
// it is sent, and Serve then returns that error without serving. A job that fails
// because satelle.dev refused the node token is answered, and then ends the
// serving with that *TokenRefusedError: a node whose token is dead does no more
// work.
func (n *Node) Serve(ctx context.Context, sess *remotechan.Session, report Report) error {
	body, err := encode(typeReady, report)
	if err != nil {
		return err
	}
	if err := sess.Send(ctx, "", body); err != nil {
		return err
	}
	if report.Error != "" {
		return errors.New(report.Error)
	}

	ctx, stop := context.WithCancelCause(ctx)
	var wg sync.WaitGroup
	defer func() { stop(nil); wg.Wait() }()
	for {
		m, err := sess.Recv(ctx)
		if err != nil {
			var refused *TokenRefusedError
			if errors.As(context.Cause(ctx), &refused) {
				return refused
			}
			if errors.Is(err, remotechan.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if m.Job == "" {
			continue // nothing outside a job is expected after ready
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.serveJob(ctx, sess, m, stop)
		}()
	}
}

// serveJob answers one request: heartbeat while the handler runs, send its
// reply, announce the job done, and stop the node if the handler found its token
// refused.
func (n *Node) serveJob(ctx context.Context, sess *remotechan.Session, m remotechan.Message, stop context.CancelCauseFunc) {
	n.active.Add(1)
	defer n.active.Add(-1)
	job := sess.StartJob(ctx, m.Job)
	jctx := job.Context() // also cancelled, ErrRelayRestarted, when the relay drops under the job
	var r reply
	var refused *TokenRefusedError
	env, err := decode(m.Body)
	switch {
	case err != nil:
		r.Error = err.Error()
	default:
		h, ok := n.handler(env.Type)
		if !ok {
			r.Error = fmt.Sprintf("unsupported job type %q", env.Type)
			break
		}
		if body, herr := h(jctx, env.Body); herr != nil {
			r.Error = herr.Error()
			_ = errors.As(herr, &refused)
		} else {
			r.Body = body
		}
	}
	if errors.Is(context.Cause(jctx), remotechan.ErrRelayRestarted) {
		// The waiting end already failed this job, naming the restart: no reply, no done.
		_ = job.Done()
		return
	}
	if out, merr := encode(typeReply, r); merr == nil {
		_ = sess.Send(ctx, m.Job, out)
	}
	_ = job.Done()
	if refused != nil {
		stop(refused)
	}
}
