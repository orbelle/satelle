package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
)

const nodeTestServer = "http://hosted.example"

// nodeCall is one RPC a node client made: its method and the metadata it sent.
type nodeCall struct{ method, auth, location string }

// nodeCalls records every RPC the server received, in order.
type nodeCalls struct {
	mu    sync.Mutex
	calls []nodeCall
}

func (r *nodeCalls) intercept(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	first := func(key string) string {
		if v := md.Get(key); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	r.mu.Lock()
	r.calls = append(r.calls, nodeCall{method: path.Base(info.FullMethod), auth: first("authorization"), location: first(LocationHeader)})
	r.mu.Unlock()
	return handler(ctx, req)
}

func (r *nodeCalls) all() []nodeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]nodeCall(nil), r.calls...)
}

// newNodeTestClient is a client acting under the node token "node-tok" against
// a bufconn server for Sync and Chain. Its HTTP client fails the test on any
// request, so a refresh attempt is caught.
func newNodeTestClient(t *testing.T, s *stubSync, ch *stubChain) (*Client, *nodeCalls) {
	t.Helper()
	rec := &nodeCalls{}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.UnaryInterceptor(rec.intercept))
	syncpb.RegisterSyncServer(gs, s)
	syncpb.RegisterChainServer(gs, ch)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})
	c := NewClient(nodeTestServer, NodeTokenStore{Server: nodeTestServer, Token: "node-tok"}, fatalHTTP(t))
	c.dialGRPC = func(ctx context.Context, target string) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///buf",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
	return c, rec
}

// sty_ce11bbdb AC1, AC3: under the node token every Sync and Chain call carries
// the token as its bearer and no location, Apply carries the run outputs, and no
// row the shared encoding sends carries an origin of the client's own.
func TestNodeTokenIsTheBearerWithNoLocationOrOrigin(t *testing.T) {
	var applied *syncpb.ApplyRequest
	s := &stubSync{apply: func(req *syncpb.ApplyRequest) (*syncpb.ApplyResponse, error) {
		applied = req
		return &syncpb.ApplyResponse{Items: int32(len(req.GetItems())), Ledger: int32(len(req.GetLedger())), Outputs: int32(len(req.GetOutputs()))}, nil
	}}
	var appended *syncpb.AppendRequest
	ch := &stubChain{
		read: func(*syncpb.ReadRequest) (*syncpb.ReadResponse, error) { return &syncpb.ReadResponse{}, nil },
		append: func(req *syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
			appended = req
			return &syncpb.AppendResponse{LatestId: "evt_1"}, nil
		},
	}
	c, rec := newNodeTestClient(t, s, ch)
	ctx := context.Background()

	res, err := c.Apply(ctx, "team", WorkstateIngest{
		Items:   []json.RawMessage{json.RawMessage(`{"id":"exe_1","kind":"execution"}`)},
		Ledger:  []json.RawMessage{json.RawMessage(`{"id":"led_1","kind":"comment"}`)},
		Outputs: []WorkstateOutput{{Execution: "exe_1", Name: "output-exe_1.md", Body: "swept"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outputs != 1 {
		t.Errorf("outputs written = %d, want 1", res.Outputs)
	}
	if out := applied.GetOutputs(); len(out) != 1 || out[0].GetExecution() != "exe_1" || out[0].GetName() != "output-exe_1.md" || string(out[0].GetBody()) != "swept" {
		t.Errorf("Apply outputs = %v, want the run output", out)
	}
	for _, it := range applied.GetItems() {
		if it.GetOrigin() != "" {
			t.Errorf("item %s origin = %q, want none", it.GetId(), it.GetOrigin())
		}
	}
	for _, e := range applied.GetLedger() {
		if e.GetOrigin() != "" {
			t.Errorf("ledger %s origin = %q, want none", e.GetId(), e.GetOrigin())
		}
	}

	cc, err := c.OpenChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cc.Close() }()
	if _, _, err := cc.ReadStory(ctx, "team", "sty_1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := cc.AppendStory(ctx, "team", "sty_1", "", []ChainEvent{{ID: "evt_1", Payload: []byte(`{"id":"evt_1","story_id":"sty_1"}`)}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range appended.GetEvents() {
		var row map[string]any
		if err := json.Unmarshal(e.GetPayload(), &row); err != nil {
			t.Fatal(err)
		}
		if _, has := row["origin"]; has {
			t.Errorf("appended event %s carries an origin: %s", e.GetId(), e.GetPayload())
		}
	}

	calls := rec.all()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want Apply, Read and Append", calls)
	}
	for _, call := range calls {
		if call.auth != "Bearer node-tok" || call.location != "" {
			t.Errorf("%s sent authorization %q and location %q, want the node token and no location", call.method, call.auth, call.location)
		}
	}
}

// sty_ce11bbdb AC3: the server's UNAUTHENTICATED (node ended or killed) and
// NOT_FOUND (another project) on Chain/Read, Chain/Append and Sync/Apply come back
// as the named errors after exactly one call, with no refresh and no retry.
func TestNodeTokenRefusalsAreNamedAndNotRetried(t *testing.T) {
	ops := map[string]func(*Client) error{
		"Read": func(c *Client) error {
			cc, err := c.OpenChain(context.Background())
			if err != nil {
				return err
			}
			defer func() { _ = cc.Close() }()
			_, _, err = cc.ReadStory(context.Background(), "other", "sty_1", "")
			return err
		},
		"Append": func(c *Client) error {
			cc, err := c.OpenChain(context.Background())
			if err != nil {
				return err
			}
			defer func() { _ = cc.Close() }()
			_, err = cc.AppendStory(context.Background(), "other", "sty_1", "", oneEvent)
			return err
		},
		"Apply": func(c *Client) error {
			_, err := c.Apply(context.Background(), "other", WorkstateIngest{})
			return err
		},
	}
	answers := []struct {
		code codes.Code
		want error
	}{
		{codes.Unauthenticated, ErrNodeTokenRefused},
		{codes.NotFound, ErrNodeProjectNotFound},
	}
	for op, call := range ops {
		for _, a := range answers {
			t.Run(op+"/"+a.code.String(), func(t *testing.T) {
				refusal := status.Error(a.code, "refused")
				s := &stubSync{apply: func(*syncpb.ApplyRequest) (*syncpb.ApplyResponse, error) { return nil, refusal }}
				ch := &stubChain{
					read:   func(*syncpb.ReadRequest) (*syncpb.ReadResponse, error) { return nil, refusal },
					append: func(*syncpb.AppendRequest) (*syncpb.AppendResponse, error) { return nil, refusal },
				}
				c, rec := newNodeTestClient(t, s, ch)
				err := call(c)
				if !errors.Is(err, a.want) {
					t.Fatalf("err = %v, want %v", err, a.want)
				}
				if errors.Is(err, ErrLoginRequired) {
					t.Errorf("err = %v reads as a login to do; a node cannot log in", err)
				}
				if calls := rec.all(); len(calls) != 1 || calls[0].method != op {
					t.Errorf("calls = %+v, want exactly one %s", calls, op)
				}
				if s.refreshes != 0 {
					t.Errorf("Sync.Refresh called %d time(s), want none", s.refreshes)
				}
			})
		}
	}
}

// The node store serves the node token for its own server only, and persists
// nothing.
func TestNodeTokenStoreServesOnlyItsServer(t *testing.T) {
	s := NodeTokenStore{Server: nodeTestServer, Token: "node-tok"}
	cred, err := s.Load(nodeTestServer + "/")
	if err != nil || cred.AccessToken != "node-tok" || !cred.IsNode() || cred.IsSession() {
		t.Fatalf("Load = %+v, %v, want the node credential", cred, err)
	}
	if _, err := s.Load("https://elsewhere.example"); !errors.Is(err, ErrNoCredential) {
		t.Errorf("Load of another server = %v, want ErrNoCredential", err)
	}
	if err := s.Save(Credential{ServerURL: nodeTestServer, AccessToken: "other"}); err != nil {
		t.Fatal(err)
	}
	if cred, _ := s.Load(nodeTestServer); cred.AccessToken != "node-tok" {
		t.Errorf("after Save the token is %q, want it unchanged", cred.AccessToken)
	}
}
