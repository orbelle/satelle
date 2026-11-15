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

type Handler func(ctx context.Context, body json.RawMessage) (json.RawMessage, error)

type Node struct {
	mu       sync.Mutex
	handlers map[string]Handler
	active   atomic.Int32
}

const (
	StepWaiting = "waiting"
	StepWorking = "working"
)

func (n *Node) Status() NodeStatus {
	if n.active.Load() > 0 {
		return NodeStatus{State: StateRunning, Step: StepWorking}
	}
	return NodeStatus{State: StateRunning, Step: StepWaiting}
}

func NewNode() *Node { return &Node{handlers: map[string]Handler{}} }

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
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.serveJob(ctx, sess, m, stop)
		}()
	}
}

func (n *Node) serveJob(ctx context.Context, sess *remotechan.Session, m remotechan.Message, stop context.CancelCauseFunc) {
	n.active.Add(1)
	defer n.active.Add(-1)
	job := sess.StartJob(ctx, m.Job)
	jctx := job.Context()
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
