package remotechan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// closeGrace bounds how long a graceful close waits for the relay to end the
// stream after this end stopped sending.
const closeGrace = 2 * time.Second

// conn is one end's stream through satelle.v1.RelayService/Connect. The first
// frame names the session and role; every frame after it carries one opaque
// channel frame in data, forwarded to the other end unchanged and in order.
type conn struct {
	stream grpc.BidiStreamingClient[satellepb.RelayFrame, satellepb.RelayFrame]
	cancel context.CancelFunc

	frames chan []byte   // unbuffered: every frame read is handed over before the end is
	done   chan struct{} // closed when the stream ended; err says why
	err    error
	wmu    sync.Mutex
	once   sync.Once
}

// dialRelay opens the stream for session as role. The client's own credential
// (a user's bearer or the node token) authenticates it; a refusal arrives as
// the status of the first read. The stream outlives ctx: Close ends it.
func dialRelay(ctx context.Context, relay satellepb.RelayServiceClient, session string, role Role) (*conn, error) {
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stream, err := relay.Connect(sctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("remotechan: dial relay: %w", err)
	}
	c := &conn{stream: stream, cancel: cancel, frames: make(chan []byte), done: make(chan struct{})}
	go c.read()
	if err := c.send(ctx, &satellepb.RelayFrame{Session: session, Role: role.String()}); err != nil {
		c.CloseNow()
		return nil, fmt.Errorf("remotechan: dial relay: %w", err)
	}
	return c, nil
}

func (c *conn) read() {
	defer close(c.done)
	for {
		f, err := c.stream.Recv()
		if errors.Is(err, io.EOF) {
			c.err = errStreamEnded
			return
		}
		if err != nil {
			c.err = err
			return
		}
		if len(f.GetData()) > maxFrameBytes {
			c.err = fmt.Errorf("%w: frame of %d bytes", ErrFrame, len(f.GetData()))
			c.cancel()
			return
		}
		c.frames <- f.GetData()
	}
}

// Read returns the next frame, or why the stream ended.
func (c *conn) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-c.frames:
		return b, nil
	case <-c.done:
		return nil, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Write sends one frame. A send on a stream the relay already ended reports the
// stream's final status, not io.EOF: gRPC reports a server-ended stream on
// SendMsg as io.EOF and leaves the status for RecvMsg, which the reader takes.
// Without this a refusal (UNAUTHENTICATED) could surface as EOF and be
// classified as a restart.
func (c *conn) Write(ctx context.Context, b []byte) error {
	return c.send(ctx, &satellepb.RelayFrame{Data: b})
}

// send is the one send site, for the header frame and every data frame.
func (c *conn) send(ctx context.Context, f *satellepb.RelayFrame) error {
	c.wmu.Lock()
	select {
	case <-c.done:
		c.wmu.Unlock()
		if c.err != nil {
			return c.err
		}
		return ErrClosed
	default:
	}
	err := c.stream.Send(f)
	c.wmu.Unlock()
	if errors.Is(err, io.EOF) {
		return c.ended(ctx)
	}
	return err
}

// ended waits for the stream's final status from the reader after a send found
// the stream already ended. It is bounded by ctx and closeGrace (the wait runs
// outside wmu, so Close is never held behind it); a reader that cannot finish
// in time (blocked handing over a frame nobody reads) leaves errStreamEnded,
// the same restart class as a bare EOF.
func (c *conn) ended(ctx context.Context) error {
	t := time.NewTimer(closeGrace)
	defer t.Stop()
	select {
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return errStreamEnded
	}
}

// Close stops sending, lets the relay deliver what was sent and end the
// stream (bounded by closeGrace), then drops it.
func (c *conn) Close() {
	c.once.Do(func() {
		c.wmu.Lock()
		_ = c.stream.CloseSend()
		c.wmu.Unlock()
		t := time.NewTimer(closeGrace)
		defer t.Stop()
	drain:
		for {
			select {
			case <-c.frames:
			case <-c.done:
				break drain
			case <-t.C:
				break drain
			}
		}
		c.cancel()
	})
}

// CloseNow drops the stream at once.
func (c *conn) CloseNow() {
	c.once.Do(func() { c.cancel() })
	go func() {
		for {
			select {
			case <-c.frames:
			case <-c.done:
				return
			}
		}
	}()
}

// errStreamEnded is a stream the relay ended without a status.
var errStreamEnded = errors.New("remotechan: the relay ended the stream")
