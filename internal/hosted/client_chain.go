package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
)

// ChainEvent is one story event as Chain/Append carries it. WorkflowVersion is the
// event's workflow_hash and Payload the ledger entry's JSON, which the server
// stores as it came.
type ChainEvent struct {
	ID              string
	BaseID          string
	WorkflowVersion string
	Payload         json.RawMessage
}

// MaxChainReplyBytes is the largest reply the client reads from Chain/Append: an
// accepted Append echoes every stored event and a StaleBase detail echoes every
// event the caller has not seen. It is set on each call, which takes precedence
// over a dial default, and sits well above grpc-go's 4 MiB default and the 3 MiB
// the CLI sends per call.
const MaxChainReplyBytes = 16 << 20

func chainCallOpts() []grpc.CallOption {
	return []grpc.CallOption{grpc.MaxCallRecvMsgSize(MaxChainReplyBytes)}
}

// AppendFrameSize is the encoded size of an Append request that carries no events.
func AppendFrameSize(project, storyID, base string) int {
	return proto.Size(&syncpb.AppendRequest{Project: project, StoryId: storyID, BaseId: base})
}

// AppendEventSize is the encoded size one event adds to an Append request.
func AppendEventSize(storyID string, e ChainEvent) int {
	return proto.Size(&syncpb.AppendRequest{Events: []*syncpb.Event{newEvent(storyID, e)}})
}

func newEvent(storyID string, e ChainEvent) *syncpb.Event {
	return &syncpb.Event{
		Id: e.ID, StoryId: storyID, BaseId: e.BaseID,
		WorkflowVersion: e.WorkflowVersion, Payload: e.Payload,
	}
}

// StaleBaseError is the refusal of an Append whose base is no longer the story's
// latest event: nothing was stored. LatestID is the server's latest event and
// Missing counts the events after Base that this machine has not seen: the
// server's missing_count, which can exceed the events its bounded detail lists.
// The caller reads the story again and decides; nothing here merges or retries.
type StaleBaseError struct {
	StoryID  string
	Base     string
	LatestID string
	Missing  int
}

// staleMissingCount is the server's missing_count, or the length of the missing
// list for a server that does not send it (a real stale base has at least one
// missing event, so zero means absent).
func staleMissingCount(sb *syncpb.StaleBase) int {
	if n := sb.GetMissingCount(); n > 0 {
		return int(n)
	}
	return len(sb.GetMissing())
}

func (e *StaleBaseError) Error() string {
	return fmt.Sprintf("hosted: story %s moved since %s: server is at %s (%d event(s) not seen)",
		e.StoryID, e.Since(), e.LatestID, e.Missing)
}

// Since names the base the Append was made from, "its start" when it was empty.
func (e *StaleBaseError) Since() string {
	if e.Base == "" {
		return "its start"
	}
	return e.Base
}

// ChainConn is one connection to the server's Chain service, shared by the
// Appends of a push.
type ChainConn struct {
	c    *Client
	conn *grpc.ClientConn
	cli  syncpb.ChainClient
}

// OpenChain dials the server's Chain service. The caller closes it. A checkout
// with no credential for the server is not connected to it: that is
// ErrLoginRequired before anything is dialled.
func (c *Client) OpenChain(ctx context.Context) (*ChainConn, error) {
	if _, err := c.store.Load(c.server); err != nil {
		if errors.Is(err, ErrNoCredential) {
			return nil, ErrLoginRequired
		}
		return nil, err
	}
	conn, err := c.openGRPC(ctx)
	if err != nil {
		return nil, err
	}
	return &ChainConn{c: c, conn: conn, cli: syncpb.NewChainClient(conn)}, nil
}

// Close releases the connection.
func (cc *ChainConn) Close() error { return cc.conn.Close() }

// AppendStory stores events after base on the story's chain and returns the new
// latest event id. base is the latest event this machine saw for the story,
// empty when it saw none. A stale base returns *StaleBaseError, and so does a
// refusal of an event id the story's chain already holds (heldEventRefusal) when
// the story has moved past base: the caller resumes after the server's latest
// event instead of sending the held events again. Any other refusal is mapped
// like every other hosted gRPC error.
func (cc *ChainConn) AppendStory(ctx context.Context, project, storyID, base string, events []ChainEvent) (string, error) {
	req := &syncpb.AppendRequest{Project: project, StoryId: storyID, BaseId: base}
	for _, e := range events {
		req.Events = append(req.Events, newEvent(storyID, e))
	}
	var out *syncpb.AppendResponse
	err := cc.c.withGRPCAuth(ctx, func(ctx context.Context) error {
		return retryTransient(ctx, func() (rpcErr error) {
			out, rpcErr = cc.cli.Append(ctx, req, chainCallOpts()...)
			return rpcErr
		})
	})
	if err != nil {
		if sb := staleBaseFromStatus(err); sb != nil {
			return "", &StaleBaseError{StoryID: storyID, Base: base, LatestID: sb.GetLatestId(), Missing: staleMissingCount(sb)}
		}
		if bareStaleBase(err) || heldEventRefusal(err) {
			// The refusal's detail echoes every event the caller has not seen and
			// can be too large for the HTTP/2 trailers, so it arrives without it. The
			// story's latest event is read instead and decides the same way. A
			// refused held event id means the chain already holds part of what was
			// sent; the read names where it ends, and the caller resumes from it.
			// A read that fails too, or a story still at base, leaves the refusal as
			// it came.
			if stale, rerr := cc.staleBaseByRead(ctx, project, storyID, base); rerr == nil && stale != nil {
				return "", stale
			}
		}
		return "", cc.c.mapSyncErr("Append", err)
	}
	return out.GetLatestId(), nil
}

// chainRetryDelays are the waits before each retry of a call that failed
// transiently: three retries, then the failure is reported. A var so tests run
// without waiting.
var chainRetryDelays = []time.Duration{500 * time.Millisecond, 2 * time.Second, 5 * time.Second}

// SetChainRetryDelays replaces the waits between retries and returns a func that
// restores them (tests only).
func SetChainRetryDelays(d []time.Duration) (restore func()) {
	prev := chainRetryDelays
	chainRetryDelays = d
	return func() { chainRetryDelays = prev }
}

// transientChainErr reports a failure of the transport rather than a decision of
// the server: the server or a proxy in front of it is unavailable (including an
// HTTP 502, which grpc-go reports as Unavailable), or reset the stream with
// ENHANCE_YOUR_CALM, which grpc-go reports as ResourceExhausted.
func transientChainErr(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unavailable:
		return true
	case codes.ResourceExhausted:
		return strings.Contains(st.Message(), "ENHANCE_YOUR_CALM")
	}
	return false
}

// retryTransient runs call, repeating it after each chainRetryDelays wait while it
// fails transiently. An Append that did apply before its reply was lost is safe to
// repeat: the repeat is refused as a stale base, or as an event id the chain
// already holds, whose latest event is one this checkout holds, which the caller
// resumes from.
func retryTransient(ctx context.Context, call func() error) error {
	err := call()
	for _, d := range chainRetryDelays {
		if err == nil || !transientChainErr(err) {
			return err
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return err
		case <-t.C:
		}
		err = call()
	}
	return err
}

// bareStaleBase reports a FailedPrecondition that carries no StaleBase detail.
func bareStaleBase(err error) bool {
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.FailedPrecondition && staleBaseFromStatus(err) == nil
}

// heldEventMessage is the text of the server's refusal of an event whose id the
// story's chain already holds ("invalid chain event: event id already in the
// story's chain").
const heldEventMessage = "already in the story's chain"

// heldEventRefusal reports the server refusing an Append because one of its event
// ids is already on the story's chain. It is matched by its message, under either
// code the server may give it; a reworded refusal falls back to the plain error.
func heldEventRefusal(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.InvalidArgument, codes.AlreadyExists:
		return strings.Contains(st.Message(), heldEventMessage)
	}
	return false
}

// ErrChainUnsupported is a server that has no Chain service (or no Read on it).
var ErrChainUnsupported = errors.New("hosted: the server has no story chain service")

// read is one Chain/Read of the events after after (the whole chain when empty).
func (cc *ChainConn) read(ctx context.Context, project, storyID, after string) (*syncpb.ReadResponse, error) {
	var out *syncpb.ReadResponse
	err := cc.c.withGRPCAuth(ctx, func(ctx context.Context) error {
		return retryTransient(ctx, func() (rpcErr error) {
			out, rpcErr = cc.cli.Read(ctx, &syncpb.ReadRequest{Project: project, StoryId: storyID, AfterId: after}, chainCallOpts()...)
			return rpcErr
		})
	})
	return out, err
}

// ReadStory returns the story's hosted events after the event after, in chain
// order (the whole chain when after is empty), and the story's latest event id,
// empty for a story the server holds no events of. A server without the Chain
// service is ErrChainUnsupported; an after the server does not know is the
// server's refusal, for the caller to read again from the start.
func (cc *ChainConn) ReadStory(ctx context.Context, project, storyID, after string) ([]ChainEvent, string, error) {
	out, err := cc.read(ctx, project, storyID, after)
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return nil, "", ErrChainUnsupported
		}
		return nil, "", cc.c.mapSyncErr("Read", err)
	}
	events := make([]ChainEvent, 0, len(out.GetEvents()))
	for _, e := range out.GetEvents() {
		events = append(events, ChainEvent{ID: e.GetId(), BaseID: e.GetBaseId(), WorkflowVersion: e.GetWorkflowVersion(), Payload: e.GetPayload()})
	}
	latest := out.GetLatestId()
	if latest == "" && len(events) > 0 {
		latest = events[len(events)-1].ID
	}
	return events, latest, nil
}

// SubscribeStories follows the project's story chains (sty_0dc600e1): every event
// appended to any of its stories after the subscription starts arrives, in
// per-story chain order, and resume maps a story to the event this checkout last
// saw, whose later events are replayed first. on is called for each event; its
// error ends the subscription and is returned as it came. A transient failure is
// retried from the last event on handled, so nothing is replayed twice. It returns
// nil when the server ends the stream and ctx's error when ctx ends. A server
// without Subscribe is ErrChainUnsupported.
func (cc *ChainConn) SubscribeStories(ctx context.Context, project string, resume map[string]string, on func(storyID string, ev ChainEvent) error) error {
	seen := make(map[string]string, len(resume))
	for id, ev := range resume {
		seen[id] = ev
	}
	var onErr error
	err := cc.c.withGRPCAuth(ctx, func(ctx context.Context) error {
		return retryTransient(ctx, func() error {
			stream, err := cc.cli.Subscribe(ctx, &syncpb.SubscribeRequest{Project: project, Resume: seen}, chainCallOpts()...)
			if err != nil {
				return err
			}
			for {
				resp, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
				e := resp.GetEvent()
				if e == nil {
					continue
				}
				ev := ChainEvent{ID: e.GetId(), BaseID: e.GetBaseId(), WorkflowVersion: e.GetWorkflowVersion(), Payload: e.GetPayload()}
				if onErr = on(e.GetStoryId(), ev); onErr != nil {
					return onErr
				}
				seen[e.GetStoryId()] = e.GetId()
			}
		})
	})
	switch {
	case err == nil:
		return nil
	case onErr != nil:
		return onErr
	case ctx.Err() != nil:
		return ctx.Err()
	case status.Code(err) == codes.Unimplemented:
		return ErrChainUnsupported
	case errors.Is(err, ErrLoginRequired):
		return err
	}
	return cc.c.mapSyncErr("Subscribe", err)
}

// staleBaseByRead learns where the story stands by reading the events after base.
// It returns nil when the story is still at base, so the refusal was not a stale
// base. A base the server does not know is read again from the start.
func (cc *ChainConn) staleBaseByRead(ctx context.Context, project, storyID, base string) (*StaleBaseError, error) {
	read := func(after string) (*syncpb.ReadResponse, error) { return cc.read(ctx, project, storyID, after) }
	out, err := read(base)
	missing := 0
	if err == nil {
		missing = len(out.GetEvents())
	} else if base != "" {
		first := err
		if out, err = read(""); err != nil {
			return nil, errors.Join(first, err)
		}
		missing = len(out.GetEvents())
		for i, e := range out.GetEvents() {
			if e.GetId() == base {
				missing = len(out.GetEvents()) - i - 1
				break
			}
		}
	} else {
		return nil, err
	}
	latest := out.GetLatestId()
	if latest == "" {
		if evs := out.GetEvents(); len(evs) > 0 {
			latest = evs[len(evs)-1].GetId()
		}
	}
	if latest == "" || latest == base {
		return nil, nil
	}
	return &StaleBaseError{StoryID: storyID, Base: base, LatestID: latest, Missing: missing}, nil
}

// staleBaseFromStatus returns the StaleBase detail of a FailedPrecondition
// status, or nil for any other error.
func staleBaseFromStatus(err error) *syncpb.StaleBase {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return nil
	}
	for _, d := range st.Details() {
		if sb, ok := d.(*syncpb.StaleBase); ok {
			return sb
		}
	}
	return nil
}
