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

const closeGrace = 2 * time.Second

type conn struct {
	stream grpc.BidiStreamingClient[satellepb.RelayFrame, satellepb.RelayFrame]
	cancel context.CancelFunc

	frames chan []byte
	done   chan struct{}
	err    error
	wmu    sync.Mutex
	once   sync.Once
}

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

func (c *conn) Write(ctx context.Context, b []byte) error {
	return c.send(ctx, &satellepb.RelayFrame{Data: b})
}

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

var errStreamEnded = errors.New("remotechan: the relay ended the stream")
