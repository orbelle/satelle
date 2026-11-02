package hosted

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
)

func noRetryWait(t *testing.T) {
	t.Helper()
	t.Cleanup(SetChainRetryDelays([]time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}))
}

// sty_9bdf08c4 AC12: Unavailable (which is what an HTTP 502 becomes) and
// ENHANCE_YOUR_CALM are retried a bounded number of times, then succeed or report.
func TestAppendStoryRetriesTransientRefusals(t *testing.T) {
	noRetryWait(t)
	var calls atomic.Int32
	cc := openStubChain(t, &stubChain{append: func(*syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
		switch calls.Add(1) {
		case 1:
			return nil, status.Error(codes.Unavailable, "unexpected HTTP status code received from server: 502 (Bad Gateway)")
		case 2:
			return nil, status.Error(codes.ResourceExhausted, "stream terminated by RST_STREAM with error code: ENHANCE_YOUR_CALM")
		}
		return &syncpb.AppendResponse{LatestId: "evt_2"}, nil
	}})
	latest, err := cc.AppendStory(context.Background(), "probe", "sty_a", "evt_1", oneEvent)
	if err != nil || latest != "evt_2" || calls.Load() != 3 {
		t.Fatalf("latest = %q, err = %v after %d calls, want success on the third", latest, err, calls.Load())
	}
}

func TestAppendStoryGivesUpAfterTheRetries(t *testing.T) {
	noRetryWait(t)
	var calls atomic.Int32
	cc := openStubChain(t, &stubChain{append: func(*syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
		calls.Add(1)
		return nil, status.Error(codes.Unavailable, "502")
	}})
	_, err := cc.AppendStory(context.Background(), "probe", "sty_a", "evt_1", oneEvent)
	if err == nil || calls.Load() != 4 {
		t.Fatalf("err = %v after %d calls, want a failure after the first call and three retries", err, calls.Load())
	}
}

// A refusal that is the server's decision is not retried, and neither is a
// message-size ResourceExhausted.
func TestAppendStoryDoesNotRetryDecisions(t *testing.T) {
	noRetryWait(t)
	for _, st := range []error{
		status.Error(codes.PermissionDenied, "viewer"),
		status.Error(codes.InvalidArgument, "empty"),
		status.Error(codes.ResourceExhausted, "received message larger than max"),
	} {
		var calls atomic.Int32
		cc := openStubChain(t, &stubChain{append: func(*syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
			calls.Add(1)
			return nil, st
		}})
		if _, err := cc.AppendStory(context.Background(), "probe", "sty_a", "evt_1", oneEvent); err == nil || calls.Load() != 1 {
			t.Errorf("%v: err = %v after %d calls, want one call", st, err, calls.Load())
		}
	}
}

// sty_9bdf08c4 AC12: a FailedPrecondition with no StaleBase detail is resolved by
// reading the story, never reported as a generic error.
func TestAppendStoryResolvesADetaillessStaleBaseByReading(t *testing.T) {
	var afters []string
	cc := openStubChain(t, &stubChain{
		append: func(*syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "stale base: the story has moved on")
		},
		read: func(req *syncpb.ReadRequest) (*syncpb.ReadResponse, error) {
			afters = append(afters, req.GetAfterId())
			return &syncpb.ReadResponse{LatestId: "evt_9", Events: []*syncpb.Event{{Id: "evt_8"}, {Id: "evt_9"}}}, nil
		},
	})
	_, err := cc.AppendStory(context.Background(), "probe", "sty_a", "evt_1", oneEvent)
	var stale *StaleBaseError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want *StaleBaseError", err)
	}
	if stale.LatestID != "evt_9" || stale.Missing != 2 || stale.Base != "evt_1" || len(afters) != 1 || afters[0] != "evt_1" {
		t.Fatalf("stale = %+v after reads %v", stale, afters)
	}
}

// A base the server does not know is read again from the start, and the events
// after the base are what is missing.
func TestAppendStoryReadsFromTheStartWhenTheBaseIsUnknown(t *testing.T) {
	cc := openStubChain(t, &stubChain{
		append: func(*syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "stale base")
		},
		read: func(req *syncpb.ReadRequest) (*syncpb.ReadResponse, error) {
			if req.GetAfterId() != "" {
				return nil, status.Error(codes.NotFound, "no such event")
			}
			return &syncpb.ReadResponse{LatestId: "evt_3", Events: []*syncpb.Event{{Id: "evt_1"}, {Id: "evt_2"}, {Id: "evt_3"}}}, nil
		},
	})
	_, err := cc.AppendStory(context.Background(), "probe", "sty_a", "evt_1", oneEvent)
	var stale *StaleBaseError
	if !errors.As(err, &stale) || stale.LatestID != "evt_3" || stale.Missing != 2 {
		t.Fatalf("err = %v, want a stale base at evt_3 with 2 missing", err)
	}
}

// A bare FailedPrecondition while the story is still at the base was not a stale
// base; it is reported as the refusal it is.
func TestAppendStoryReportsABareRefusalTheStoryDidNotMove(t *testing.T) {
	cc := openStubChain(t, &stubChain{
		append: func(*syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "story is archived")
		},
		read: func(*syncpb.ReadRequest) (*syncpb.ReadResponse, error) {
			return &syncpb.ReadResponse{LatestId: "evt_1"}, nil
		},
	})
	_, err := cc.AppendStory(context.Background(), "probe", "sty_a", "evt_1", oneEvent)
	var stale *StaleBaseError
	if err == nil || errors.As(err, &stale) {
		t.Fatalf("err = %v, want a plain refusal", err)
	}
}

// sty_aa605c84 AC5: a refusal of an event id the story's chain already holds is
// resolved by reading the story, so the caller resumes after the server's latest
// event instead of sending the held ids again; while the story is still at the
// base it stays the refusal it is.
func TestAppendStoryResolvesAHeldEventRefusalByReading(t *testing.T) {
	for _, code := range []codes.Code{codes.InvalidArgument, codes.AlreadyExists} {
		var afters []string
		cc := openStubChain(t, &stubChain{
			append: func(*syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
				return nil, status.Error(code, "invalid chain event: event id already in the story's chain")
			},
			read: func(req *syncpb.ReadRequest) (*syncpb.ReadResponse, error) {
				afters = append(afters, req.GetAfterId())
				return &syncpb.ReadResponse{LatestId: "evt_2", Events: []*syncpb.Event{{Id: "evt_1"}, {Id: "evt_2"}}}, nil
			},
		})
		_, err := cc.AppendStory(context.Background(), "probe", "sty_a", "", oneEvent)
		var stale *StaleBaseError
		if !errors.As(err, &stale) {
			t.Fatalf("%v: err = %v, want *StaleBaseError", code, err)
		}
		if stale.LatestID != "evt_2" || stale.Missing != 2 || stale.Base != "" || len(afters) != 1 || afters[0] != "" {
			t.Fatalf("%v: stale = %+v after reads %v", code, stale, afters)
		}
	}

	cc := openStubChain(t, &stubChain{
		append: func(*syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
			return nil, status.Error(codes.InvalidArgument, "invalid chain event: event id already in the story's chain")
		},
		read: func(*syncpb.ReadRequest) (*syncpb.ReadResponse, error) {
			return &syncpb.ReadResponse{LatestId: "evt_1"}, nil
		},
	})
	_, err := cc.AppendStory(context.Background(), "probe", "sty_a", "evt_1", oneEvent)
	var stale *StaleBaseError
	if err == nil || errors.As(err, &stale) {
		t.Fatalf("still at base: err = %v, want the plain refusal", err)
	}
}
