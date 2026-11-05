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
	if err := stream.Send(&satellepb.RelayFrame{Session: session, Role: role.String()}); err != nil {
		cancel()
		return nil, fmt.Errorf("remotechan: dial relay: %w", err)
	}
	c := &conn{stream: stream, cancel: cancel, frames: make(chan []byte), done: make(chan struct{})}
	go c.read()
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

// Write sends one frame.
func (c *conn) Write(_ context.Context, b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
		if c.err != nil {
			return c.err
		}
		return ErrClosed
	default:
	}
	return c.stream.Send(&satellepb.RelayFrame{Data: b})
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
