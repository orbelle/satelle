package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

// fakeChain is a satelle.chain.v1.Chain server with the real compare-and-append
// rule: events are stored only when the request's base is the story's latest, and
// each event builds on the one before. It mirrors accepted events into the Sync
// fake's ledger so pulls and ledger counts see them.
type fakeChain struct {
	syncpb.UnimplementedChainServer
	f *fakeWorkstateServer

	mu      sync.Mutex
	chains  map[string][]*syncpb.Event // project|story -> stored events
	appends []*syncpb.AppendRequest    // every request, as it arrived
	// refuse returns this error for a story instead of storing its events.
	refuse map[string]error
	// before runs under the lock ahead of each Append, so a test can move a story
	// on the server between two of a push's calls.
	before func(req *syncpb.AppendRequest, chain *[]*syncpb.Event)
	// transient lists errors a story's next Appends return, one per call, before it
	// behaves normally.
	transient map[string][]error
	// bareStale refuses a stale base without the StaleBase detail, as the server
	// does when the detail is too large for the HTTP/2 trailers.
	bareStale bool
	// heldRefusal refuses, for a story, an Append carrying an event id the chain
	// already holds, ahead of the base check, as the server does.
	heldRefusal map[string]bool
	// unreadable makes Read answer Unimplemented, as a server that predates the
	// Read RPC does: a pull then has no hosted chain to take.
	unreadable bool
	// reads counts Read calls per story.
	reads map[string]int
	// feed is what Subscribe streams before it ends the stream; subscribes are the
	// requests it received. noSubscribe answers Unimplemented, as a server that
	// predates the subscription does.
	feed        []*syncpb.Event
	subscribes  []*syncpb.SubscribeRequest
	noSubscribe bool
}

// Subscribe streams the feed, in order, and ends the stream.
func (c *fakeChain) Subscribe(req *syncpb.SubscribeRequest, stream grpc.ServerStreamingServer[syncpb.SubscribeResponse]) error {
	c.mu.Lock()
	c.subscribes = append(c.subscribes, proto.Clone(req).(*syncpb.SubscribeRequest))
	feed, unsupported := slices.Clone(c.feed), c.noSubscribe
	c.mu.Unlock()
	if unsupported {
		return status.Error(codes.Unimplemented, "method Subscribe not implemented")
	}
	for _, e := range feed {
		if err := stream.Send(&syncpb.SubscribeResponse{Event: e}); err != nil {
			return err
		}
	}
	return nil
}

// Read serves the events after after_id, and the story's latest id.
func (c *fakeChain) Read(_ context.Context, req *syncpb.ReadRequest) (*syncpb.ReadResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads[req.GetStoryId()]++
	if c.unreadable {
		return nil, status.Error(codes.Unimplemented, "method Read not implemented")
	}
	chain := c.chains[chainKey(req.GetProject(), req.GetStoryId())]
	events := chain
	if after := req.GetAfterId(); after != "" {
		i := slices.IndexFunc(chain, func(e *syncpb.Event) bool { return e.GetId() == after })
		if i < 0 {
			return nil, status.Error(codes.NotFound, "no such event")
		}
		events = chain[i+1:]
	}
	out := &syncpb.ReadResponse{Events: events}
	if len(chain) > 0 {
		out.LatestId = chain[len(chain)-1].GetId()
	}
	return out, nil
}

func newFakeChain(f *fakeWorkstateServer) *fakeChain {
	return &fakeChain{f: f, chains: map[string][]*syncpb.Event{}, refuse: map[string]error{}, reads: map[string]int{}}
}

func chainKey(project, story string) string { return project + "|" + story }

func (c *fakeChain) Append(_ context.Context, req *syncpb.AppendRequest) (*syncpb.AppendResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appends = append(c.appends, proto.Clone(req).(*syncpb.AppendRequest))
	key := chainKey(req.GetProject(), req.GetStoryId())
	chain := c.chains[key]
	if c.before != nil {
		c.before(req, &chain)
		c.chains[key] = chain
	}
	if err := c.refuse[req.GetStoryId()]; err != nil {
		return nil, err
	}
	if q := c.transient[req.GetStoryId()]; len(q) > 0 {
		c.transient[req.GetStoryId()] = q[1:]
		return nil, q[0]
	}
	if c.heldRefusal[req.GetStoryId()] {
		for _, e := range req.GetEvents() {
			if slices.ContainsFunc(chain, func(h *syncpb.Event) bool { return h.GetId() == e.GetId() }) {
				return nil, status.Error(codes.InvalidArgument, "invalid chain event: event id already in the story's chain")
			}
		}
	}
	latest := ""
	if len(chain) > 0 {
		latest = chain[len(chain)-1].GetId()
	}
	if req.GetBaseId() != latest && c.bareStale {
		return nil, status.Error(codes.FailedPrecondition, "stale base: the story has moved on")
	}
	if req.GetBaseId() != latest {
		var missing []*syncpb.Event
		if i := slices.IndexFunc(chain, func(e *syncpb.Event) bool { return e.GetId() == req.GetBaseId() }); req.GetBaseId() != "" && i >= 0 {
			missing = chain[i+1:]
		} else {
			missing = chain
		}
		st, err := status.New(codes.FailedPrecondition, "stale base").WithDetails(&syncpb.StaleBase{LatestId: latest, Missing: missing})
		if err != nil {
			return nil, err
		}
		return nil, st.Err()
	}
	prev := latest
	for _, e := range req.GetEvents() {
		if e.GetBaseId() != prev || e.GetId() == "" || e.GetStoryId() != req.GetStoryId() {
			return nil, status.Error(codes.InvalidArgument, "event does not build on the one before")
		}
		prev = e.GetId()
	}
	for _, e := range req.GetEvents() {
		stored := proto.Clone(e).(*syncpb.Event)
		stored.Seq = int64(len(chain) + 1)
		chain = append(chain, stored)
		c.mirror(req.GetProject(), stored)
	}
	c.chains[key] = chain
	return &syncpb.AppendResponse{LatestId: prev}, nil
}

// mirror files an accepted event under the Sync fake's ledger.
func (c *fakeChain) mirror(project string, e *syncpb.Event) {
	var m map[string]any
	if json.Unmarshal(e.GetPayload(), &m) != nil || c.f == nil {
		return
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.f.ledgerByID[project] == nil {
		c.f.ledgerByID[project] = map[string]any{}
	}
	c.f.ledgerByID[project][e.GetId()] = m
	c.f.ledgerVer = c.f.stamp(c.f.ledgerVer, project, e.GetId())
}

// seed stores events as already on the server for a story.
func (c *fakeChain) seed(project, story string, events ...*syncpb.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chains[chainKey(project, story)] = append(c.chains[chainKey(project, story)], events...)
}

func (c *fakeChain) appendsFor(story string) []*syncpb.AppendRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*syncpb.AppendRequest
	for _, r := range c.appends {
		if r.GetStoryId() == story {
			out = append(out, r)
		}
	}
	return out
}

func (c *fakeChain) readCount(story string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads[story]
}

func (c *fakeChain) appendCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.appends)
}

// eventCount is how many events the server has accepted for project.
func (c *fakeChain) eventCount(project string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for key, chain := range c.chains {
		if strings.HasPrefix(key, project+"|") {
			n += len(chain)
		}
	}
	return n
}

// carries reports whether any Append request held text in an event payload.
func (c *fakeChain) carries(text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.appends {
		for _, e := range r.GetEvents() {
			if strings.Contains(string(e.GetPayload()), text) {
				return true
			}
		}
	}
	return false
}

func (c *fakeChain) stored(project, story string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, e := range c.chains[chainKey(project, story)] {
		ids = append(ids, e.GetId())
	}
	return ids
}

func eventIDs(events []*syncpb.Event) []string {
	var ids []string
	for _, e := range events {
		ids = append(ids, e.GetId())
	}
	return ids
}

// localChain is a story's local events in chain order.
func localChain(t *testing.T, story string) []ledger.Entry {
	t.Helper()
	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	events, err := db.Ledger.StoryChain(context.Background(), story)
	if err != nil {
		t.Fatal(err)
	}
	order, err := chainOrder(events)
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func localLedgerTotal(t *testing.T) int {
	t.Helper()
	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	n, err := db.Ledger.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func storyHeads(t *testing.T, server, project, repo string) map[string]string {
	t.Helper()
	c, err := hosted.LoadWorkstateCursor(server, project, repo)
	if err != nil {
		t.Fatal(err)
	}
	return c.StoryHeads
}

func storyConflicts(t *testing.T, repo string) map[string]syncstate.StoryConflict {
	t.Helper()
	s, _, err := syncstate.Load(config.GlobalDir(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return s.StoryConflicts
}

func pushRaw(t *testing.T, server string) (string, error) {
	t.Helper()
	return runRoot(t, "sync", "workstate", "push", "--server", server)
}

// A chain the local store cannot lay out as one line is reported, never guessed at.
func TestChainOrderRefusesAForkAndAStrandedEvent(t *testing.T) {
	e := func(id, base string) ledger.Entry { return ledger.Entry{ID: id, StoryID: "sty_a", BaseID: base} }
	got, err := chainOrder([]ledger.Entry{e("c", "b"), e("a", ""), e("b", "a")})
	if err != nil || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("out-of-order chain = %v, %v", got, err)
	}
	if _, err := chainOrder([]ledger.Entry{e("a", ""), e("b", "a"), e("c", "a")}); err == nil || !strings.Contains(err.Error(), "forks at a") {
		t.Errorf("fork: err = %v", err)
	}
	if _, err := chainOrder([]ledger.Entry{e("a", ""), e("b", "gone")}); err == nil || !strings.Contains(err.Error(), "not reached") {
		t.Errorf("stranded event: err = %v", err)
	}
}

// capForSplitting lowers appendBatchBytes to a size that holds the frame and the
// largest of the story's events but rarely two, so any chain splits.
func capForSplitting(t *testing.T, story string, local []ledger.Entry) {
	t.Helper()
	events, err := chainEvents(local)
	if err != nil {
		t.Fatal(err)
	}
	largest := 0
	for _, e := range events {
		largest = max(largest, hosted.AppendEventSize(story, e))
	}
	old := appendBatchBytes
	appendBatchBytes = hosted.AppendFrameSize("probe", story, local[0].ID) + largest + 1
	t.Cleanup(func() { appendBatchBytes = old })
}

// watchHeld makes the chain record, for each Append of story, the ids it carried
// that the story's chain already held when it arrived. seed, when set, runs first
// with the call's number (from 1), so a test can move the chain before it.
func (c *fakeChain) watchHeld(story string, seed func(call int, chain *[]*syncpb.Event)) func() [][]string {
	var held [][]string
	calls := 0
	c.mu.Lock()
	c.before = func(req *syncpb.AppendRequest, chain *[]*syncpb.Event) {
		if req.GetStoryId() != story {
			return
		}
		calls++
		if seed != nil {
			seed(calls, chain)
		}
		var ids []string
		for _, e := range req.GetEvents() {
			if slices.ContainsFunc(*chain, func(h *syncpb.Event) bool { return h.GetId() == e.GetId() }) {
				ids = append(ids, e.GetId())
			}
		}
		held = append(held, ids)
	}
	c.mu.Unlock()
	return func() [][]string {
		c.mu.Lock()
		defer c.mu.Unlock()
		return slices.Clone(held)
	}
}

// ungatedLifecycle gives repo a backlog → plan → in_progress → done route with no
// gates, so a test moves a story's status without a reviewer.
func ungatedLifecycle(t *testing.T, repo string) {
	t.Helper()
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"),
		`["*"]
obligations = ["raised", "planned", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "executor"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
}
