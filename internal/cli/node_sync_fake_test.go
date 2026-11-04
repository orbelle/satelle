package cli

import (
	"context"
	"encoding/json"
	"path"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// rpcCall is one Sync or Chain RPC as the fake server received it.
type rpcCall struct {
	Method   string // Apply, Read, Append, ...
	Project  string
	Story    string
	Auth     string
	Location string
	// At is the recorder's mark when the call arrived (0 with no mark).
	At int
}

// rpcRecorder records every unary RPC, in order, and can refuse any of them.
type rpcRecorder struct {
	mu    sync.Mutex
	calls []rpcCall
	// refuse, when set, answers a call with its error instead of serving it.
	refuse func(rpcCall) error
	// mark, when set, stamps each call's At: how far another server's log had
	// got when it arrived.
	mark func() int
}

func (r *rpcRecorder) intercept(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	first := func(key string) string {
		if v := md.Get(key); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	c := rpcCall{Method: path.Base(info.FullMethod), Auth: first("authorization"), Location: first(hosted.LocationHeader)}
	if p, ok := req.(interface{ GetProject() string }); ok {
		c.Project = p.GetProject()
	}
	if s, ok := req.(interface{ GetStoryId() string }); ok {
		c.Story = s.GetStoryId()
	}
	r.mu.Lock()
	if r.mark != nil {
		c.At = r.mark()
	}
	r.calls = append(r.calls, c)
	refuse := r.refuse
	r.mu.Unlock()
	if refuse != nil {
		if err := refuse(c); err != nil {
			return nil, err
		}
	}
	return handler(ctx, req)
}

func (r *rpcRecorder) all() []rpcCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rpcCall(nil), r.calls...)
}

// nodeSync is the Sync and Chain services a node's work state reaches: the fake
// push and pull tests use, behind a recorder.
type nodeSync struct {
	f   *fakeWorkstateServer
	rec *rpcRecorder
}

// newNodeSync serves a fresh fake as the process's Sync and Chain.
func newNodeSync(t *testing.T) *nodeSync {
	t.Helper()
	f := &fakeWorkstateServer{
		posts:      map[string][]map[string]any{},
		itemsByID:  map[string]map[string]any{},
		ledgerByID: map[string]map[string]any{},
	}
	f.chain = newFakeChain(f)
	return serveNodeSync(t, f)
}

// serveNodeSync serves f, its chains as they stand, as the process's Sync and
// Chain behind a fresh recorder: a node and a developer's checkout then reach
// the one server.
func serveNodeSync(t *testing.T, f *fakeWorkstateServer) *nodeSync {
	t.Helper()
	s := &nodeSync{f: f, rec: &rpcRecorder{}}
	attachTestGRPCWith(t, []grpc.ServerOption{grpc.UnaryInterceptor(s.rec.intercept)}, &fakeWorkstateGRPC{f: f},
		func(gs *grpc.Server) { syncpb.RegisterChainServer(gs, f.chain) })
	return s
}

// seed chains rows on story's hosted chain in project from its current head, as
// a push would have stored them.
func (s *nodeSync) seed(project, story string, rows ...ledger.Entry) error {
	base := ""
	if held := s.f.chain.stored(project, story); len(held) > 0 {
		base = held[len(held)-1]
	}
	for _, e := range rows {
		if e.ID == "" {
			e.ID = ledger.NewID()
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
		e.StoryID, e.BaseID = story, base
		payload, err := marshalWorkstateLedger(e)
		if err != nil {
			return err
		}
		s.f.chain.seed(project, story, &syncpb.Event{Id: e.ID, StoryId: story, BaseId: base, WorkflowVersion: e.WorkflowHash, Payload: payload})
		base = e.ID
	}
	return nil
}

// applies is every Apply request, in order.
func (s *nodeSync) applies() []*syncpb.ApplyRequest {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return append([]*syncpb.ApplyRequest(nil), s.f.applies...)
}

// appends is every Append request, in order.
func (s *nodeSync) appends() []*syncpb.AppendRequest {
	s.f.chain.mu.Lock()
	defer s.f.chain.mu.Unlock()
	return append([]*syncpb.AppendRequest(nil), s.f.chain.appends...)
}

// pushedRow is what a test reads of a pushed ledger row.
type pushedRow struct {
	ID      string          `json:"id"`
	StoryID string          `json:"story_id"`
	Kind    string          `json:"kind"`
	BaseID  string          `json:"base_id"`
	Actor   string          `json:"actor"`
	Payload json.RawMessage `json:"payload"`
}

// appended is the rows the push appended to story's chain, in order.
func (s *nodeSync) appended(t *testing.T, story string) []pushedRow {
	t.Helper()
	var out []pushedRow
	for _, req := range s.f.chain.appendsFor(story) {
		for _, ev := range req.GetEvents() {
			var r pushedRow
			if err := json.Unmarshal(ev.GetPayload(), &r); err != nil {
				t.Fatal(err)
			}
			r.ID, r.StoryID, r.BaseID = ev.GetId(), ev.GetStoryId(), ev.GetBaseId()
			out = append(out, r)
		}
	}
	return out
}
