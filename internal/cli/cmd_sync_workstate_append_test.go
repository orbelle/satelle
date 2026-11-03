package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/syncscope"
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

// sty_9bdf08c4 AC1, AC6: each story's events leave in one Append that names an
// empty base, carries the chain in order with each event's workflow hash, and no
// story-scoped ledger entry rides the bulk Apply.
func TestPushAppendsEachStoryOnce(t *testing.T) {
	ts, f := newFakeWorkstateServerWith(t, func(mux *http.ServeMux, _ *fakeWorkstateServer) {
		mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				t.Errorf("a push placed a hold: %s %s", r.Method, r.URL.Path)
			}
			http.NotFound(w, r)
		})
	})
	seedCred(t, ts.URL)
	workstateRepo(t, holdOffToml)
	a, b := createStoryID(t, "alpha"), createStoryID(t, "beta")

	out, err := pushRaw(t, ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	for _, id := range []string{a, b} {
		reqs := f.chain.appendsFor(id)
		if len(reqs) != 1 {
			t.Fatalf("story %s: %d Appends, want 1", id, len(reqs))
		}
		local := localChain(t, id)
		req := reqs[0]
		if req.GetProject() != "probe" || req.GetBaseId() != "" {
			t.Errorf("story %s: project %q base %q, want probe and an empty base", id, req.GetProject(), req.GetBaseId())
		}
		if len(req.GetEvents()) != len(local) {
			t.Fatalf("story %s: %d events sent, %d local", id, len(req.GetEvents()), len(local))
		}
		for i, e := range req.GetEvents() {
			if e.GetId() != local[i].ID || e.GetBaseId() != local[i].BaseID || e.GetStoryId() != id {
				t.Errorf("story %s event %d = %s on %s, want %s on %s", id, i, e.GetId(), e.GetBaseId(), local[i].ID, local[i].BaseID)
			}
			if e.GetWorkflowVersion() != local[i].WorkflowHash {
				t.Errorf("story %s event %s workflow_version %q, want its workflow_hash %q", id, e.GetId(), e.GetWorkflowVersion(), local[i].WorkflowHash)
			}
		}
		if got := f.chain.stored("probe", id); !slices.Equal(got, eventIDs(req.GetEvents())) {
			t.Errorf("story %s stored %v, want the sent chain", id, got)
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applies) == 0 {
		t.Fatal("the story rows never went through Apply")
	}
	for _, req := range f.applies {
		for _, e := range req.GetLedger() {
			if e.GetStoryId() != "" {
				t.Errorf("Apply carried the story-scoped entry %s of %s", e.GetId(), e.GetStoryId())
			}
		}
	}
}

// sty_9bdf08c4 AC2: the accepted head is stored per server, project and repo, it
// outlives the end-of-push cursor save so a second push sends nothing, and another
// project starts from an empty base.
func TestPushStoresTheHeadPerProject(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "alpha")

	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	local := localChain(t, id)
	if got := storyHeads(t, ts.URL, "probe", repo)[id]; got != local[len(local)-1].ID {
		t.Fatalf("head = %q, want the last event %q", got, local[len(local)-1].ID)
	}
	cursor, err := hosted.LoadWorkstateCursor(ts.URL, "probe", repo)
	if err != nil || cursor.LedgerSeq <= 0 {
		t.Fatalf("the end-of-push cursor save was lost or the head overwrote it: %+v, %v", cursor, err)
	}

	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("second push: %v\n%s", err, out)
	}
	if n := len(f.chain.appendsFor(id)); n != 1 {
		t.Fatalf("a second push sent an Append: %d in all, want 1", n)
	}

	cfg := filepath.Join(repo, ".satelle", "satelle.toml")
	if err := os.WriteFile(cfg, []byte(strings.Replace(holdOffToml, `"probe"`, `"other"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push to another project: %v\n%s", err, out)
	}
	reqs := f.chain.appendsFor(id)
	if len(reqs) != 2 || reqs[1].GetProject() != "other" || reqs[1].GetBaseId() != "" || len(reqs[1].GetEvents()) != len(local) {
		t.Fatalf("another project must start the story from an empty base: %v", reqs)
	}
}

// sty_a21d9c07: a story this checkout has no head for and that the server moved
// on is read first and reported moved without being sent; nothing of it is
// stored, the other story still publishes and push exits non-zero.
func TestPushReportsAMovedStoryUnsentWhenNoHeadIsKnown(t *testing.T) {
	ts, f := newFakeWorkstateServerWith(t, func(mux *http.ServeMux, _ *fakeWorkstateServer) {
		mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				t.Errorf("a refusal placed a hold: %s %s", r.Method, r.URL.Path)
			}
			http.NotFound(w, r)
		})
	})
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	moved, fine := createStoryID(t, "moved"), createStoryID(t, "fine")
	f.chain.seed("probe", moved, &syncpb.Event{Id: "evt_foreign", StoryId: moved})
	before := localLedgerTotal(t)
	chainBefore := len(localChain(t, moved))

	out, err := pushRaw(t, ts.URL)
	if err == nil {
		t.Fatalf("a stale base must make push exit non-zero:\n%s", out)
	}
	for _, want := range []string{
		"the story " + moved + " moved since its start: re-read and decide", "evt_foreign", "1 event",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if n := len(f.chain.appendsFor(moved)); n != 0 {
		t.Errorf("the moved story was sent %d times, want none: its hosted chain is read first", n)
	}
	if got := f.chain.stored("probe", moved); !slices.Equal(got, []string{"evt_foreign"}) {
		t.Errorf("the server's chain was changed: %v", got)
	}
	if len(f.chain.stored("probe", fine)) == 0 {
		t.Errorf("the other story was not published:\n%s", out)
	}
	heads := storyHeads(t, ts.URL, "probe", repo)
	if heads[moved] != "" || heads[fine] == "" {
		t.Errorf("heads = %v, want none for the refused story and one for the other", heads)
	}
	if localLedgerTotal(t) != before || len(localChain(t, moved)) != chainBefore {
		t.Error("a refusal changed the local events")
	}
}

// sty_9bdf08c4 AC3, AC9, sty_0dc600e1 AC4: a published story the server moved on
// is rebased once; when it is stale again after that, it is reported and recorded,
// nothing of it is stored, the other story still publishes, push exits non-zero —
// and once the story is accepted the conflict is cleared.
func TestPushReportsAStaleBaseAndClearsItOnceAccepted(t *testing.T) {
	ts, f := newFakeWorkstateServerWith(t, func(mux *http.ServeMux, _ *fakeWorkstateServer) {
		mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				t.Errorf("a refusal placed a hold: %s %s", r.Method, r.URL.Path)
			}
			http.NotFound(w, r)
		})
	})
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	moved, fine := createStoryID(t, "moved"), createStoryID(t, "fine")
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("first push: %v\n%s", err, out)
	}
	published := f.chain.stored("probe", moved)
	finePublished := len(f.chain.stored("probe", fine))
	appendLocal(t, moved, "made here")
	appendLocal(t, fine, "fine moves on")
	now := time.Now().UTC()
	foreign := func(id, base string) *syncpb.Event {
		return hostedEvent(t, ledger.Entry{ID: id, StoryID: moved, BaseID: base, Kind: ledger.KindComment, Actor: "alice", Body: "made elsewhere", CreatedAt: now})
	}
	f.chain.seed("probe", moved, foreign("evt_foreign", published[len(published)-1]))
	sentBefore := len(f.chain.appendsFor(moved))
	// The story moves again between the rebase's read and its re-append.
	sent := 0
	f.chain.before = func(req *syncpb.AppendRequest, chain *[]*syncpb.Event) {
		if req.GetStoryId() != moved {
			return
		}
		if sent++; sent == 2 {
			*chain = append(*chain, foreign("evt_foreign2", "evt_foreign"))
		}
	}
	before := localLedgerTotal(t)
	chainBefore := len(localChain(t, moved))

	out, err := pushRaw(t, ts.URL)
	if err == nil {
		t.Fatalf("a base stale after the rebase must make push exit non-zero:\n%s", out)
	}
	for _, want := range []string{
		"the story " + moved + " moved on the server: 1 event(s) applied here",
		"the story " + moved + " moved since evt_foreign: re-read and decide", "evt_foreign2", "1 event",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if n := len(f.chain.appendsFor(moved)) - sentBefore; n != 2 {
		t.Errorf("the refused story was sent %d times, want once and once after the rebase", n)
	}
	if got, want := f.chain.stored("probe", moved), append(slices.Clone(published), "evt_foreign", "evt_foreign2"); !slices.Equal(got, want) {
		t.Errorf("the server's chain was changed by the refused story: %v, want %v", got, want)
	}
	if len(f.chain.stored("probe", fine)) != finePublished+1 {
		t.Errorf("the other story was not published:\n%s", out)
	}
	heads := storyHeads(t, ts.URL, "probe", repo)
	if heads[moved] != "evt_foreign" || heads[fine] == "" {
		t.Errorf("heads = %v, want the event the rebase took for the refused story and one for the other", heads)
	}
	if localLedgerTotal(t) != before+1 || len(localChain(t, moved)) != chainBefore+1 {
		t.Error("the rebase must add the one server event it read and lose none of the local ones")
	}
	if c, ok := storyConflicts(t, repo)[moved]; !ok || !strings.Contains(c.Reason, "re-read and decide") {
		t.Errorf("no conflict recorded for %s: %v", moved, storyConflicts(t, repo))
	}

	// The second move is undone on the server; the next push is accepted after it.
	f.chain.before = nil
	f.chain.mu.Lock()
	f.chain.chains[chainKey("probe", moved)] = f.chain.chains[chainKey("probe", moved)][:len(published)+1]
	f.chain.mu.Unlock()
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push after the story was settled: %v\n%s", err, out)
	}
	if _, ok := storyConflicts(t, repo)[moved]; ok {
		t.Errorf("the conflict outlived an accepted Append: %v", storyConflicts(t, repo))
	}
	if got, want := f.chain.stored("probe", moved), localIDs(t, moved); !slices.Equal(got, want) || len(want) != chainBefore+1 {
		t.Errorf("the server holds %v, want the local chain %v: ours, the foreign event, then ours", got, want)
	}
}

// sty_9bdf08c4 AC4: the server holding a prefix of this checkout's own chain past
// its saved head (a head lost to an interrupted push) is resumed from, said so,
// with only the later events sent and none resent.
func TestPushResumesFromItsOwnPrefix(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "alpha")
	local := localChain(t, id)
	if len(local) < 3 {
		t.Fatalf("need at least 3 local events, have %d", len(local))
	}
	for _, e := range local[:2] {
		f.chain.seed("probe", id, &syncpb.Event{Id: e.ID, StoryId: id, BaseId: e.BaseID})
	}
	if err := hosted.SaveStoryHead(ts.URL, "probe", repo, id, local[0].ID); err != nil {
		t.Fatal(err)
	}

	out, err := pushRaw(t, ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if want := "the story " + id + " is already published up to " + local[1].ID + ": resuming from it"; !strings.Contains(out, want) {
		t.Errorf("report missing %q:\n%s", want, out)
	}
	reqs := f.chain.appendsFor(id)
	if len(reqs) != 2 {
		t.Fatalf("%d Appends, want the refused one and one retry", len(reqs))
	}
	var rest []string
	for _, e := range local[2:] {
		rest = append(rest, e.ID)
	}
	if reqs[1].GetBaseId() != local[1].ID || !slices.Equal(eventIDs(reqs[1].GetEvents()), rest) {
		t.Errorf("retry = base %q events %v, want base %q events %v", reqs[1].GetBaseId(), eventIDs(reqs[1].GetEvents()), local[1].ID, rest)
	}
	if got := f.chain.stored("probe", id); len(got) != len(local) {
		t.Errorf("server chain = %v, want %d events once each", got, len(local))
	}
	if got := storyHeads(t, ts.URL, "probe", repo)[id]; got != local[len(local)-1].ID {
		t.Errorf("head = %q, want the last event", got)
	}
}

// sty_9bdf08c4 AC4: a second refusal on the resumed retry is the agent's to read.
func TestPushReportsASecondRefusalOnTheResume(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "alpha")
	local := localChain(t, id)
	for _, e := range local[:2] {
		f.chain.seed("probe", id, &syncpb.Event{Id: e.ID, StoryId: id, BaseId: e.BaseID})
	}
	if err := hosted.SaveStoryHead(ts.URL, "probe", repo, id, local[0].ID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.chain.before = func(req *syncpb.AppendRequest, chain *[]*syncpb.Event) {
		if calls++; calls == 2 { // someone else appends between the refusal and the retry
			*chain = append(*chain, &syncpb.Event{Id: "evt_foreign", StoryId: id, BaseId: local[1].ID})
		}
	}

	out, err := pushRaw(t, ts.URL)
	if err == nil {
		t.Fatalf("push must exit non-zero:\n%s", out)
	}
	for _, want := range []string{"resuming from it", "the story " + id + " moved since " + local[1].ID + ": re-read and decide", "evt_foreign"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if n := len(f.chain.appendsFor(id)); n != 2 {
		t.Errorf("%d Appends, want exactly the refusal and one retry", n)
	}
}

// sty_9bdf08c4 AC7: nothing is dialled for the Chain service unless the repo's
// resolved scope syncs, and nothing at all without a credential; the local log
// is never touched.
func TestPushAppendsOnlyWhenConnectedAndOptedIn(t *testing.T) {
	t.Run("scope local", func(t *testing.T) {
		ts, f := newFakeWorkstateServer(t)
		seedCred(t, ts.URL)
		// Bound, but the account is not in the project's team: the scope is local.
		workstateRepo(t, "[sync]\nproject = \"probe\"\n")
		useScope(t, kindForeign)
		id := createStoryID(t, "alpha")
		before := localLedgerTotal(t)

		out, err := pushRaw(t, ts.URL)
		if err != nil {
			t.Fatalf("push: %v\n%s", err, out)
		}
		if !strings.Contains(out, "scope: local") {
			t.Errorf("a push the account resolves local should report the local scope:\n%s", out)
		}
		if f.hasItem("probe", id) {
			t.Error("a local-scope repo pushed its stories")
		}
		if n := f.chain.appendCount(); n != 0 {
			t.Errorf("%d Appends with the scope local", n)
		}
		if after := localLedgerTotal(t); after != before {
			t.Errorf("local ledger rows %d -> %d", before, after)
		}
	})
	// A server always resolves (the default origin), so a checkout that is not
	// connected is one with no credential for it.
	t.Run("not connected", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		workstateRepo(t, holdOffToml)
		createStoryID(t, "alpha")
		before := localLedgerTotal(t)
		var dials atomic.Int32
		hosted.SetTestGRPCDial(func(context.Context, string) (*grpc.ClientConn, error) {
			dials.Add(1)
			return nil, status.Error(codes.Unavailable, "no server")
		})
		t.Cleanup(func() { hosted.SetTestGRPCDial(nil) })

		out, err := runRoot(t, "sync", "workstate", "push", "--server", "http://hosted.example")
		if err != nil || !strings.Contains(out, syncscope.ReasonSignedOut) {
			t.Fatalf("push = %v\n%s, want the signed-out local scope", err, out)
		}
		if n := dials.Load(); n != 0 {
			t.Errorf("%d dials with no credential", n)
		}
		if after := localLedgerTotal(t); after != before {
			t.Errorf("local ledger rows %d -> %d", before, after)
		}
	})
}

// sty_9bdf08c4 AC8: a refusal that is not a stale base is an error for its story —
// reported as such, its head untouched, no conflict recorded — and the other
// stories still publish.
func TestPushReportsOtherRefusalsAsErrors(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	denied, invalid, fine := createStoryID(t, "denied"), createStoryID(t, "invalid"), createStoryID(t, "fine")
	f.chain.refuse[denied] = status.Error(codes.PermissionDenied, "viewers may not append")
	f.chain.refuse[invalid] = status.Error(codes.InvalidArgument, "workflow_version is empty")

	out, err := pushRaw(t, ts.URL)
	if err == nil {
		t.Fatalf("push must exit non-zero:\n%s", out)
	}
	for _, want := range []string{
		"the story " + denied + " was not published", "viewers may not append",
		"the story " + invalid + " was not published", "workflow_version is empty",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "re-read and decide") {
		t.Errorf("a plain refusal was reported as a stale base:\n%s", out)
	}
	heads := storyHeads(t, ts.URL, "probe", repo)
	if heads[denied] != "" || heads[invalid] != "" || heads[fine] == "" {
		t.Errorf("heads = %v", heads)
	}
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Errorf("a plain refusal recorded a story conflict: %v", c)
	}

	// The refused stories stay candidates, so once the server accepts them they go.
	f.chain.mu.Lock()
	f.chain.refuse = map[string]error{}
	f.chain.mu.Unlock()
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push after the refusals cleared: %v\n%s", err, out)
	}
	if len(f.chain.stored("probe", denied)) == 0 || len(f.chain.stored("probe", invalid)) == 0 {
		t.Error("a refused story was never retried")
	}
}

// A head this store no longer holds (a restored or replaced database) is sent as an
// empty base, and the server's answer decides: here it holds nothing, so the whole
// chain is accepted.
func TestPushTreatsAHeadMissingFromTheLocalChainAsNone(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "alpha")
	if err := hosted.SaveStoryHead(ts.URL, "probe", repo, id, "evt_gone0001"); err != nil {
		t.Fatal(err)
	}

	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	reqs := f.chain.appendsFor(id)
	local := localChain(t, id)
	if len(reqs) != 1 || reqs[0].GetBaseId() != "" || len(reqs[0].GetEvents()) != len(local) {
		t.Fatalf("Appends = %v, want one from an empty base carrying the %d local events", reqs, len(local))
	}
	if got := storyHeads(t, ts.URL, "probe", repo)[id]; got != local[len(local)-1].ID {
		t.Errorf("head = %q, want the last event", got)
	}
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

// sty_9bdf08c4 AC11: a story over the per-call cap is sent as several Appends in
// chain order, each under the cap and based on the latest event the one before
// accepted, and the head ends at the last event.
func TestPushSplitsAStoryOverTheCapIntoChainedAppends(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "alpha")
	local := localChain(t, id)
	if len(local) < 3 {
		t.Fatalf("need at least 3 local events, have %d", len(local))
	}
	capForSplitting(t, id, local)

	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	reqs := f.chain.appendsFor(id)
	if len(reqs) < 2 {
		t.Fatalf("%d Appends, want the story split over the cap", len(reqs))
	}
	var sent []string
	for i, r := range reqs {
		if n := proto.Size(r); n > appendBatchBytes {
			t.Errorf("Append %d is %d bytes, over the %d cap", i, n, appendBatchBytes)
		}
		wantBase := ""
		if i > 0 {
			wantBase = sent[len(sent)-1]
		}
		if r.GetBaseId() != wantBase {
			t.Errorf("Append %d base = %q, want %q", i, r.GetBaseId(), wantBase)
		}
		sent = append(sent, eventIDs(r.GetEvents())...)
	}
	var want []string
	for _, e := range local {
		want = append(want, e.ID)
	}
	if !slices.Equal(sent, want) || !slices.Equal(f.chain.stored("probe", id), want) {
		t.Errorf("sent %v, stored %v, want the chain %v once each", sent, f.chain.stored("probe", id), want)
	}
	if got := storyHeads(t, ts.URL, "probe", repo)[id]; got != want[len(want)-1] {
		t.Errorf("head = %q, want the last event %q", got, want[len(want)-1])
	}
}

// sty_9bdf08c4 AC11, AC3: a stale base part-way through a split story is reported
// from that batch — the head stays at the last accepted batch and no more is sent.
func TestPushReportsAStaleBaseMidStory(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "alpha")
	local := localChain(t, id)
	capForSplitting(t, id, local)
	calls := 0
	f.chain.before = func(req *syncpb.AppendRequest, chain *[]*syncpb.Event) {
		if calls++; calls == 2 { // someone else appends after the first batch
			*chain = append(*chain, &syncpb.Event{Id: "evt_foreign", StoryId: id, BaseId: (*chain)[len(*chain)-1].GetId()})
		}
	}

	out, err := pushRaw(t, ts.URL)
	if err == nil {
		t.Fatalf("push must exit non-zero:\n%s", out)
	}
	first := f.chain.appendsFor(id)[0]
	lastSent := first.GetEvents()[len(first.GetEvents())-1].GetId()
	if want := "the story " + id + " moved since " + lastSent + ": re-read and decide"; !strings.Contains(out, want) {
		t.Errorf("report missing %q:\n%s", want, out)
	}
	if n := len(f.chain.appendsFor(id)); n != 2 {
		t.Errorf("%d Appends, want the accepted batch and the refused one", n)
	}
	if got := storyHeads(t, ts.URL, "probe", repo)[id]; got != lastSent {
		t.Errorf("head = %q, want the last accepted event %q", got, lastSent)
	}
}

// sty_9bdf08c4 AC12: a transient transport refusal — Unavailable, an HTTP 502
// (Unavailable to grpc-go) or ENHANCE_YOUR_CALM — is retried and the story goes
// through; one that outlasts the retries is reported per story, head unchanged.
func TestPushRetriesATransientRefusal(t *testing.T) {
	defer hosted.SetChainRetryDelays([]time.Duration{time.Millisecond, time.Millisecond, time.Millisecond})()
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	flaky, down := createStoryID(t, "flaky"), createStoryID(t, "down")
	f.chain.transient = map[string][]error{
		flaky: {
			status.Error(codes.Unavailable, "unexpected HTTP status code received from server: 502 (Bad Gateway)"),
			status.Error(codes.ResourceExhausted, "stream terminated by RST_STREAM with error code: ENHANCE_YOUR_CALM"),
		},
		down: {
			status.Error(codes.Unavailable, "502"), status.Error(codes.Unavailable, "502"),
			status.Error(codes.Unavailable, "502"), status.Error(codes.Unavailable, "502"),
			status.Error(codes.Unavailable, "502"),
		},
	}

	out, err := pushRaw(t, ts.URL)
	if err == nil {
		t.Fatalf("a story the retries did not reach must make push exit non-zero:\n%s", out)
	}
	if n := len(f.chain.appendsFor(flaky)); n != 3 {
		t.Errorf("flaky story: %d calls, want two failures and the success", n)
	}
	if got := f.chain.stored("probe", flaky); len(got) != len(localChain(t, flaky)) {
		t.Errorf("flaky story stored %v, want its whole chain", got)
	}
	if got := storyHeads(t, ts.URL, "probe", repo); got[flaky] == "" || got[down] != "" {
		t.Errorf("heads = %v, want the flaky story's only", got)
	}
	if n := len(f.chain.appendsFor(down)); n != 4 {
		t.Errorf("down story: %d calls, want the first and three retries", n)
	}
	if !strings.Contains(out, "the story "+down+" was not published") || strings.Contains(out, "the story "+flaky+" was not published") {
		t.Errorf("report:\n%s", out)
	}
}

// sty_9bdf08c4 AC12: a FailedPrecondition with no StaleBase detail is resolved by
// reading the story's latest event, then handled as an own prefix (AC4) ...
func TestPushResolvesADetaillessRefusalAsItsOwnPrefix(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "alpha")
	local := localChain(t, id)
	for _, e := range local[:2] {
		f.chain.seed("probe", id, &syncpb.Event{Id: e.ID, StoryId: id, BaseId: e.BaseID})
	}
	f.chain.bareStale = true

	out, err := pushRaw(t, ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if want := "the story " + id + " is already published up to " + local[1].ID + ": resuming from it"; !strings.Contains(out, want) {
		t.Errorf("report missing %q:\n%s", want, out)
	}
	if strings.Contains(out, "stale base: the story has moved on") {
		t.Errorf("reported as a generic error:\n%s", out)
	}
	if got := f.chain.stored("probe", id); len(got) != len(local) {
		t.Errorf("server chain = %v, want %d events once each", got, len(local))
	}
	if got := storyHeads(t, ts.URL, "probe", repo)[id]; got != local[len(local)-1].ID {
		t.Errorf("head = %q, want the last event", got)
	}
}

// ... or, when the latest event is not in the local chain, as a story that moved.
func TestPushResolvesADetaillessRefusalAsAMovedStory(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "alpha")
	f.chain.seed("probe", id, &syncpb.Event{Id: "evt_foreign", StoryId: id})
	f.chain.bareStale = true

	out, err := pushRaw(t, ts.URL)
	if err == nil {
		t.Fatalf("push must exit non-zero:\n%s", out)
	}
	for _, want := range []string{"the story " + id + " moved since its start: re-read and decide", "evt_foreign", "1 event"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if got := f.chain.stored("probe", id); !slices.Equal(got, []string{"evt_foreign"}) {
		t.Errorf("server chain = %v, want it untouched", got)
	}
	if got := storyHeads(t, ts.URL, "probe", repo)[id]; got != "" {
		t.Errorf("head = %q, want none", got)
	}
	if _, ok := storyConflicts(t, repo)[id]; !ok {
		t.Errorf("no conflict recorded for %s", id)
	}
}

// sty_a21d9c07 AC2: a push after its cursor was reset (the store was rebuilt, so
// no story head is known) sends no event the hosted chain already holds — none for
// a story the server holds whole, only the rest for one it holds a prefix of.
func TestPushAfterCursorResetSendsNoHeldEvent(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	whole, part := createStoryID(t, "whole"), createStoryID(t, "part")
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("first push: %v\n%s", err, out)
	}
	wholeLocal, partLocal := localChain(t, whole), localChain(t, part)
	if len(partLocal) < 2 {
		t.Fatalf("need at least 2 local events, have %d", len(partLocal))
	}
	const k = 1
	f.chain.mu.Lock()
	f.chain.chains[chainKey("probe", part)] = f.chain.chains[chainKey("probe", part)][:k]
	f.chain.appends = nil
	f.chain.mu.Unlock()
	if err := hosted.SaveWorkstateCursor(ts.URL, "probe", repo, hosted.WorkstateCursor{StoryHeads: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if heads := storyHeads(t, ts.URL, "probe", repo); len(heads) != 0 {
		t.Fatalf("the reset left heads %v", heads)
	}
	before := f.chain.eventCount("probe")

	out, err := pushRaw(t, ts.URL)
	if err != nil {
		t.Fatalf("push after the reset: %v\n%s", err, out)
	}
	sent := 0
	for _, r := range f.chain.appendsFor(whole) {
		sent += len(r.GetEvents())
	}
	if sent != 0 {
		t.Errorf("the story the server holds whole sent %d event(s), want 0", sent)
	}
	if want := "the story " + whole + " is already published up to " + wholeLocal[len(wholeLocal)-1].ID + ": nothing to send"; !strings.Contains(out, want) {
		t.Errorf("report missing %q:\n%s", want, out)
	}
	reqs := f.chain.appendsFor(part)
	if len(reqs) != 1 {
		t.Fatalf("the prefixed story: %d Appends, want one", len(reqs))
	}
	var rest []string
	for _, e := range partLocal[k:] {
		rest = append(rest, e.ID)
	}
	if reqs[0].GetBaseId() != partLocal[k-1].ID || !slices.Equal(eventIDs(reqs[0].GetEvents()), rest) {
		t.Errorf("Append = base %q events %v, want base %q events %v", reqs[0].GetBaseId(), eventIDs(reqs[0].GetEvents()), partLocal[k-1].ID, rest)
	}
	if got, want := f.chain.eventCount("probe"), before+len(rest); got != want {
		t.Errorf("server holds %d events, want %d", got, want)
	}
	heads := storyHeads(t, ts.URL, "probe", repo)
	if heads[whole] != wholeLocal[len(wholeLocal)-1].ID || heads[part] != partLocal[len(partLocal)-1].ID {
		t.Errorf("heads = %v, want each story at its last event", heads)
	}
	if strings.Contains(out, "Published the events of 2") {
		t.Errorf("the story with nothing to send was counted as published:\n%s", out)
	}
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

// sty_aa605c84 AC4: a story fast-forwarded by a pull and then given one local
// event pushes only that event: no Append carries an id the chain already held.
func TestPushAfterFastForwardSendsNoHeldId(t *testing.T) {
	ts, f, id, want, repo := publishedElsewhere(t)
	seedLocalPrefix(t, ts.URL, repo, id, want[:1])
	if out, err := pullRaw(t, ts.URL); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	sameChain(t, localChain(t, id), want)
	appendLocal(t, id, "made after the fast-forward")
	held := f.chain.watchHeld(id, nil)

	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	for i, ids := range held() {
		if len(ids) > 0 {
			t.Errorf("Append %d re-sent held ids %v", i, ids)
		}
	}
	if len(held()) == 0 {
		t.Error("the local event was never sent")
	}
	if got, local := f.chain.stored("probe", id), localIDs(t, id); !slices.Equal(got, local) || len(local) != len(want)+1 {
		t.Errorf("hosted ids = %v, want the hosted chain and the one local event %v", got, local)
	}
}

// sty_aa605c84 AC5: a push the server refuses because an event id is already in the
// story's chain resumes after the server's latest event, publishes the rest and
// exits 0: no Append after the refusal carries a held id.
func TestPushResumesPastAHeldEventRefusal(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, holdOffToml)
	id := createStoryID(t, "alpha")
	local := localChain(t, id)
	if len(local) < 3 {
		t.Fatalf("need at least 3 local events, have %d", len(local))
	}
	f.chain.heldRefusal = map[string]bool{id: true}
	// The hosted chain is empty when the push reads it, and holds the first two
	// local events by the time its Append arrives: an earlier publish that landed.
	held := f.chain.watchHeld(id, func(call int, chain *[]*syncpb.Event) {
		if call == 1 {
			for _, e := range local[:2] {
				*chain = append(*chain, &syncpb.Event{Id: e.ID, StoryId: id, BaseId: e.BaseID})
			}
		}
	})

	out, err := pushRaw(t, ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if want := "the story " + id + " is already published up to " + local[1].ID + ": resuming from it"; !strings.Contains(out, want) {
		t.Errorf("report missing %q:\n%s", want, out)
	}
	got := held()
	if len(got) < 2 || len(got[0]) == 0 {
		t.Fatalf("held ids per Append = %v, want the refused one carrying held ids and a resume", got)
	}
	for i, ids := range got[1:] {
		if len(ids) > 0 {
			t.Errorf("Append %d after the refusal re-sent held ids %v", i+1, ids)
		}
	}
	if got, want := f.chain.stored("probe", id), localIDs(t, id); !slices.Equal(got, want) {
		t.Errorf("hosted ids = %v, want the local chain %v", got, want)
	}
	if got := storyHeads(t, ts.URL, "probe", repo)[id]; got != local[len(local)-1].ID {
		t.Errorf("head = %q, want the last event", got)
	}
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Errorf("conflicts = %v, want none", c)
	}
}
