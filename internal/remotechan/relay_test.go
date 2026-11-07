package remotechan

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// eofRelay is a relay whose stream the server ends before this end reads it:
// send number eofAt (0 is the header frame) returns io.EOF, and only after that
// does Recv return the stream's final status, end. This forces the order gRPC
// allows in production: SendMsg reports io.EOF while the status is still
// waiting for RecvMsg.
type eofRelay struct {
	eofAt int
	end   error
}

func (r *eofRelay) Connect(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[satellepb.RelayFrame, satellepb.RelayFrame], error) {
	return &eofStream{ctx: ctx, eofAt: r.eofAt, end: r.end, sent: make(chan struct{})}, nil
}

type eofStream struct {
	grpc.ClientStream // nil: only the methods below are used

	ctx   context.Context
	eofAt int
	end   error

	mu   sync.Mutex
	n    int
	sent chan struct{} // closed when the send that returns io.EOF happened
}

func (s *eofStream) Send(*satellepb.RelayFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.n
	s.n++
	switch {
	case n < s.eofAt:
		return nil
	case n == s.eofAt:
		close(s.sent)
	}
	return io.EOF
}

func (s *eofStream) Recv() (*satellepb.RelayFrame, error) {
	select {
	case <-s.sent:
		return nil, s.end
	case <-s.ctx.Done():
		return nil, status.FromContextError(s.ctx.Err()).Err()
	}
}

func (s *eofStream) CloseSend() error         { return nil }
func (s *eofStream) Context() context.Context { return s.ctx }

// A refusal that gRPC reports to a send as io.EOF reaches Join's caller as the
// stream's status, whether the header or the first data frame hit it.
func TestSendEOFReportsTheRelayStatus(t *testing.T) {
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		eofAt int
	}{{"header", 0}, {"hello", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			relay := &eofRelay{eofAt: tc.eofAt, end: status.Error(codes.Unauthenticated, "invalid_token")}
			_, err := Join(testCtx(t), relay, "nod_1", id)
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("join = %v, want UNAUTHENTICATED", err)
			}
			if classifyClose(err) != closeRefusal {
				t.Fatalf("join = %v is not classified as a refusal", err)
			}
			if errors.Is(err, io.EOF) {
				t.Fatalf("join = %v still reports io.EOF", err)
			}
		})
	}
}

// A stream the relay ended with OK still reads as a restart after a send saw
// io.EOF.
func TestSendEOFAfterOKEndIsARestart(t *testing.T) {
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Join(testCtx(t), &eofRelay{eofAt: 1, end: io.EOF}, "nod_1", id)
	if !errors.Is(err, errStreamEnded) {
		t.Fatalf("join = %v, want errStreamEnded", err)
	}
	if classifyClose(err) != closeRestart {
		t.Fatalf("join = %v is not classified as a restart", err)
	}
}
