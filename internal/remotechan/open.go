package remotechan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

const EnrollTTL = 10 * time.Minute

var ErrEnrollRefused = errors.New("remotechan: enrollment refused")

const (
	KindRemoteEnrollRefused       = "remote_enroll_refused"
	KindRemoteChannelResumed      = "remote_channel_resumed"
	KindRemoteChannelResumeFailed = "remote_channel_resume_failed"
)

type Record struct {
	Kind    string
	Actor   string
	Body    string
	Payload json.RawMessage
}

type Recorder interface {
	Record(ctx context.Context, r Record, now time.Time) error
}

type options struct {
	clock  Clock
	rec    Recorder
	window time.Duration
}

type Option func(*options)

func WithClock(c Clock) Option { return func(o *options) { o.clock = c } }

func WithRecorder(r Recorder) Option { return func(o *options) { o.rec = r } }

func WithResumeWindow(d time.Duration) Option { return func(o *options) { o.window = d } }

func newOptions(opts []Option) *options {
	o := &options{}
	for _, f := range opts {
		f(o)
	}
	if o.window <= 0 {
		o.window = ResumeWindow
	}
	if o.clock == nil {
		o.clock = RealClock()
	}
	return o
}

var ErrKeyUnpublished = errors.New("remotechan: the node published no channel key")

const keyPollInterval = time.Second

func Attach(ctx context.Context, relay satellepb.RelayServiceClient, session string, keyFn func(context.Context) ([]byte, error), opts ...Option) (*Session, error) {
	o := newOptions(opts)
	c, err := dialRelay(ctx, relay, session, RoleLocal)
	if err != nil {
		return nil, err
	}
	pinned, err := awaitKey(ctx, o, keyFn)
	if err != nil {
		if errors.Is(err, ErrKeyUnpublished) {
			err = o.refused(ctx, session, "key_unpublished", err)
		}
		c.CloseNow()
		return nil, err
	}
	res := &resumer{
		session: session, window: o.window, rec: o.rec,
		redial: func(ctx context.Context) (*conn, *sealer, *opener, error) {
			c, err := dialRelay(ctx, relay, session, RoleLocal)
			if err != nil {
				return nil, nil, nil, err
			}
			send, recv, err := handshakeLocal(ctx, c, session, pinned, o)
			if err != nil {
				c.CloseNow()
				return nil, nil, nil, err
			}
			return c, send, recv, nil
		},
	}
	hctx, cancel := context.WithTimeout(ctx, EnrollTTL)
	defer cancel()
	send, recv, err := handshakeLocal(hctx, c, session, pinned, o)
	if err != nil {
		c.Close()
		return nil, err
	}
	return newSession(ctx, RoleLocal, c, send, recv, o.clock, res), nil
}

func awaitKey(ctx context.Context, o *options, keyFn func(context.Context) ([]byte, error)) ([]byte, error) {
	deadline := o.clock.Now().Add(EnrollTTL)
	tick, stop := o.clock.Tick(keyPollInterval)
	defer stop()
	for {
		key, err := keyFn(ctx)
		if err != nil {
			return nil, fmt.Errorf("remotechan: reading the node's published key: %w", err)
		}
		if len(key) > 0 {
			return key, nil
		}
		if !o.clock.Now().Before(deadline) {
			return nil, ErrKeyUnpublished
		}
		select {
		case <-tick:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func handshakeLocal(ctx context.Context, c *conn, session string, pinned []byte, o *options) (*sealer, *opener, error) {
	var send *sealer
	var recv *opener
	var answered []byte
	for {
		frame, err := c.Read(ctx)
		if err != nil {
			if send == nil {
				return nil, nil, fmt.Errorf("remotechan: waiting for the remote: %w", err)
			}
			return nil, nil, fmt.Errorf("remotechan: waiting for enrollment: %w", err)
		}
		if len(frame) == 1+pubLen && frame[0] == frameHelloRemote {
			if answered != nil && bytes.Equal(frame[1:], answered) {
				continue
			}
			eL, err := newEphemeral()
			if err != nil {
				return nil, nil, err
			}
			if send, recv, err = localKeys(session, eL, frame[1:], pinned); err != nil {
				return nil, nil, err
			}
			if err := c.Write(ctx, append([]byte{frameHelloLocal}, eL.PublicKey().Bytes()...)); err != nil {
				return nil, nil, err
			}
			answered = frame[1:]
			continue
		}
		if send == nil {
			return nil, nil, fmt.Errorf("%w: expected the remote's key half", ErrFrame)
		}
		plain, err := recv.open(frame)
		if errors.Is(err, errOpen) {
			_ = c.Write(ctx, []byte{frameRefused})
			return nil, nil, o.refused(ctx, session, "key_mismatch", ErrKeyMismatch)
		}
		if err != nil {
			return nil, nil, err
		}
		var m appMsg
		if json.Unmarshal(plain, &m) != nil || m.T != msgEnroll {
			return nil, nil, fmt.Errorf("%w: expected enrollment", ErrFrame)
		}
		admitted, _ := json.Marshal(appMsg{T: msgAdmit})
		if err := c.Write(ctx, send.seal(admitted)); err != nil {
			return nil, nil, err
		}
		return send, recv, nil
	}
}

func (o *options) refused(ctx context.Context, session, reason string, cause error) error {
	if o.rec == nil {
		return cause
	}
	payload, _ := json.Marshal(map[string]string{"session": session, "reason": reason})
	err := o.rec.Record(ctx, Record{
		Kind: KindRemoteEnrollRefused, Actor: "remotechan", Body: reason, Payload: payload,
	}, o.clock.Now())
	if err != nil {
		return errors.Join(cause, fmt.Errorf("remotechan: record refused enrollment: %w", err))
	}
	return cause
}

func Join(ctx context.Context, relay satellepb.RelayServiceClient, session string, id *Identity, opts ...Option) (*Session, error) {
	o := newOptions(opts)
	c, err := dialRelay(ctx, relay, session, RoleRemote)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, EnrollTTL)
	defer cancel()
	send, recv, err := joinHandshake(hctx, c, session, id, o, HelloResend)
	if err != nil {
		c.CloseNow()
		return nil, err
	}
	res := &resumer{
		session: session, window: o.window, rec: o.rec,
		redial: func(ctx context.Context) (*conn, *sealer, *opener, error) {
			c, err := dialRelay(ctx, relay, session, RoleRemote)
			if err != nil {
				return nil, nil, nil, err
			}
			send, recv, err := joinHandshake(ctx, c, session, id, o, HelloResend)
			if err != nil {
				c.CloseNow()
				return nil, nil, nil, err
			}
			return c, send, recv, nil
		},
	}
	return newSession(ctx, RoleRemote, c, send, recv, o.clock, res), nil
}

func joinHandshake(ctx context.Context, c *conn, session string, id *Identity, o *options, resend time.Duration) (*sealer, *opener, error) {
	eR, err := newEphemeral()
	if err != nil {
		return nil, nil, err
	}
	hello := append([]byte{frameHelloRemote}, eR.PublicKey().Bytes()...)
	if err := c.Write(ctx, hello); err != nil {
		return nil, nil, err
	}
	frame, err := awaitLocalHello(ctx, c, o.clock, hello, resend)
	if err != nil {
		return nil, nil, fmt.Errorf("remotechan: waiting for local satelle: %w", err)
	}
	if len(frame) != 1+pubLen || frame[0] != frameHelloLocal {
		return nil, nil, fmt.Errorf("%w: expected local's key half", ErrFrame)
	}
	send, recv, err := remoteKeys(session, id, eR, frame[1:])
	if err != nil {
		return nil, nil, err
	}
	enroll, _ := json.Marshal(appMsg{T: msgEnroll})
	if err := c.Write(ctx, send.seal(enroll)); err != nil {
		return nil, nil, err
	}
	reply, err := c.Read(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("remotechan: channel closed before admission: %w", err)
	}
	if len(reply) == 1 && reply[0] == frameRefused {
		return nil, nil, fmt.Errorf("%w: local satelle pinned another key", ErrEnrollRefused)
	}
	plain, err := recv.open(reply)
	if err != nil {
		return nil, nil, err
	}
	var m appMsg
	if json.Unmarshal(plain, &m) != nil {
		return nil, nil, fmt.Errorf("%w: malformed reply", ErrFrame)
	}
	switch m.T {
	case msgAdmit:
		return send, recv, nil
	case msgRefuse:
		return nil, nil, fmt.Errorf("%w: %s", ErrEnrollRefused, m.Reason)
	}
	return nil, nil, fmt.Errorf("%w: expected admission", ErrFrame)
}

func awaitLocalHello(ctx context.Context, c *conn, clock Clock, hello []byte, resend time.Duration) ([]byte, error) {
	if resend <= 0 {
		return c.Read(ctx)
	}
	tick, stop := clock.Tick(resend)
	defer stop()
	for {
		rctx, cancel := context.WithCancel(ctx)
		got := make(chan struct{})
		var frame []byte
		var err error
		go func() {
			frame, err = c.Read(rctx)
			close(got)
		}()
		select {
		case <-got:
			cancel()
			return frame, err
		case <-tick:
			cancel()
			<-got
			if frame != nil {
				return frame, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !errors.Is(err, context.Canceled) {
				return nil, err
			}
			if err := c.Write(ctx, hello); err != nil {
				return nil, err
			}
		}
	}
}
