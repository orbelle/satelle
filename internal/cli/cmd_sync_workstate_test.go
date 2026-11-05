package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// fakeWorkstateServer records Apply batches per project and accumulates
// items/ledger by id across Applies (sty_88e83180 AC8).
type fakeWorkstateServer struct {
	mu         sync.Mutex
	posts      map[string][]map[string]any // wsID -> posted batches
	itemsByID  map[string]map[string]any   // project -> id -> record
	ledgerByID map[string]map[string]any
	gets       int
	failOnPost int // 1-based: fail this POST with 500; 0 = never
	postN      int

	// Snapshot paging and since-cursors (sty_2cb4e1f6). Apply stamps every row it
	// stores with a rising version; a since_token "v<N>" returns rows stamped after
	// N, plus any row seeded straight into the maps (unstamped), which a server may
	// always repeat. Pages are page_size items AND page_size ledger rows, token
	// "o<offset>".
	itemVer      map[string]map[string]int
	ledgerVer    map[string]map[string]int
	seq          int
	snapReqs     []*syncpb.SnapshotRequest
	snapFailCall int // 1-based Snapshot call to fail with Unavailable; 0 = never
	// Tokens carry the epoch they were issued in ("v3.1", "o2.1"); expireTokens
	// bumps it, so every token issued before is refused with FailedPrecondition.
	epoch int
	// repeatBoundary makes each page after the first start with the last row of the
	// page before it: a server may repeat a row, it must never miss one.
	repeatBoundary bool
	// Story scoping (sty_991b6900): a request's story_ids limits the pass to those
	// items and the ledger rows of those stories. ignoreScope serves the whole
	// project anyway, as a server that predates the field would;
	// snapRejectCall (1-based) refuses that Snapshot call's token with
	// FailedPrecondition.
	ignoreScope    bool
	snapRejectCall int
	// omitOnCall drops an item id from the response of that 1-based Snapshot call,
	// as a server whose answer lacks a story it was asked for (sty_7b4742d0).
	omitOnCall map[string]int
	// chain is the Chain service served beside Sync (sty_9bdf08c4); applies
	// captures every Apply request as it arrived.
	chain   *fakeChain
	applies []*syncpb.ApplyRequest
}

// sent counts the items and ledger rows the server has been sent for project: the
// rows of every Apply it accepted, and the story events it accepted by Append
// (sty_9bdf08c4).
func (f *fakeWorkstateServer) sent(project string) (items, ledger int) {
	if f.chain != nil {
		ledger = f.chain.eventCount(project) // before f.mu: the chain takes it while mirroring
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.posts[project] {
		its, _ := p["items"].([]any)
		led, _ := p["ledger"].([]any)
		items += len(its)
		ledger += len(led)
	}
	return items, ledger
}

// expireTokens invalidates every since_token and page_token issued so far.
func (f *fakeWorkstateServer) expireTokens() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.epoch++
}

// stamp records that a row changed at the next version.
func (f *fakeWorkstateServer) stamp(ver map[string]map[string]int, project, id string) map[string]map[string]int {
	if ver == nil {
		ver = map[string]map[string]int{}
	}
	if ver[project] == nil {
		ver[project] = map[string]int{}
	}
	f.seq++
	ver[project][id] = f.seq
	return ver
}

// snapshotRequests copies the recorded Snapshot requests.
func (f *fakeWorkstateServer) snapshotRequests() []*syncpb.SnapshotRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*syncpb.SnapshotRequest(nil), f.snapReqs...)
}

func newFakeWorkstateServer(t *testing.T) (*httptest.Server, *fakeWorkstateServer) {
	t.Helper()
	return newFakeWorkstateServerWith(t, nil)
}

// newFakeWorkstateServerWith is newFakeWorkstateServer plus extra REST routes
// (the hosted hold surface) registered on the same server.
func newFakeWorkstateServerWith(t *testing.T, routes func(mux *http.ServeMux, f *fakeWorkstateServer)) (*httptest.Server, *fakeWorkstateServer) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Work-state cursor lives in the document-sync state file (sty_88e83180).
	hosted.DocumentSyncStatePathOverride = filepath.Join(t.TempDir(), "document-sync-state.json")
	t.Cleanup(func() { hosted.DocumentSyncStatePathOverride = "" })
	f := &fakeWorkstateServer{
		posts:      map[string][]map[string]any{},
		itemsByID:  map[string]map[string]any{},
		ledgerByID: map[string]map[string]any{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"id": "ws-personal", "kind": "personal", "name": "personal"},
			{"id": "ws-team", "kind": "team", "name": "Acme"},
		})
	})
	if routes != nil {
		routes(mux, f)
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	attachFakeWorkstateGRPC(t, f)
	return ts, f
}

// attachTestGRPC installs a process-wide bufconn dialer for hosted.Client
// gRPC (SetTestGRPCDial is not safe for t.Parallel).
func attachTestGRPC(t *testing.T, srv syncpb.SyncServer, extra ...func(*grpc.Server)) {
	t.Helper()
	attachTestGRPCWith(t, nil, srv, extra...)
}

// attachTestGRPCWith is attachTestGRPC with server options (an interceptor).
func attachTestGRPCWith(t *testing.T, opts []grpc.ServerOption, srv syncpb.SyncServer, extra ...func(*grpc.Server)) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(opts...)
	syncpb.RegisterSyncServer(gs, srv)
	for _, register := range extra {
		register(gs)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
		hosted.SetTestGRPCDial(nil)
	})
	hosted.SetTestGRPCDial(func(ctx context.Context, target string) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///buf",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	})
}

// attachFakeWorkstateGRPC serves Sync against the same maps push/pull tests use.
func attachFakeWorkstateGRPC(t *testing.T, f *fakeWorkstateServer, opts ...grpc.ServerOption) {
	t.Helper()
	f.chain = newFakeChain(f)
	attachTestGRPCWith(t, opts, &fakeWorkstateGRPC{f: f}, func(gs *grpc.Server) { syncpb.RegisterChainServer(gs, f.chain) })
}

type fakeWorkstateGRPC struct {
	syncpb.UnimplementedSyncServer
	f *fakeWorkstateServer
}

func (s *fakeWorkstateGRPC) Apply(ctx context.Context, req *syncpb.ApplyRequest) (*syncpb.ApplyResponse, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.postN++
	s.f.applies = append(s.f.applies, proto.Clone(req).(*syncpb.ApplyRequest))
	if s.f.failOnPost > 0 && s.f.postN == s.f.failOnPost {
		return nil, status.Error(codes.Internal, "injected failure")
	}
	key := req.GetProject()
	if s.f.itemsByID[key] == nil {
		s.f.itemsByID[key] = map[string]any{}
	}
	if s.f.ledgerByID[key] == nil {
		s.f.ledgerByID[key] = map[string]any{}
	}
	items := make([]any, 0, len(req.GetItems()))
	for _, it := range req.GetItems() {
		var m map[string]any
		if err := json.Unmarshal(it.GetRecord(), &m); err != nil {
			continue
		}
		items = append(items, m)
		id, _ := m["id"].(string)
		if id != "" {
			s.f.itemsByID[key][id] = m
			s.f.itemVer = s.f.stamp(s.f.itemVer, key, id)
		}
	}
	ledger := make([]any, 0, len(req.GetLedger()))
	for _, e := range req.GetLedger() {
		var m map[string]any
		if err := json.Unmarshal(e.GetRecord(), &m); err != nil {
			continue
		}
		ledger = append(ledger, m)
		id, _ := m["id"].(string)
		if id != "" {
			s.f.ledgerByID[key][id] = m
			s.f.ledgerVer = s.f.stamp(s.f.ledgerVer, key, id)
		}
	}
	s.f.posts[key] = append(s.f.posts[key], map[string]any{"items": items, "ledger": ledger})
	return &syncpb.ApplyResponse{Items: int32(len(req.GetItems())), Ledger: int32(len(req.GetLedger())), Outputs: int32(len(req.GetOutputs()))}, nil
}

func (s *fakeWorkstateGRPC) Snapshot(ctx context.Context, req *syncpb.SnapshotRequest) (*syncpb.SnapshotResponse, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.gets++
	s.f.snapReqs = append(s.f.snapReqs, proto.Clone(req).(*syncpb.SnapshotRequest))
	if s.f.snapFailCall > 0 && s.f.gets == s.f.snapFailCall {
		return nil, status.Error(codes.Unavailable, "injected snapshot failure")
	}
	if s.f.snapRejectCall > 0 && s.f.gets == s.f.snapRejectCall {
		return nil, status.Error(codes.FailedPrecondition, "token expired")
	}
	var scope map[string]bool
	if ids := req.GetStoryIds(); len(ids) > 0 && !s.f.ignoreScope {
		scope = map[string]bool{}
		for _, id := range ids {
			scope[id] = true
		}
	}
	var since, off int
	for _, tok := range []struct {
		val, format string
		into        *int
	}{
		{req.GetSinceToken(), "v%d.%d", &since},
		{req.GetPageToken(), "o%d.%d", &off},
	} {
		if tok.val == "" {
			continue
		}
		var epoch int
		if n, _ := fmt.Sscanf(tok.val, tok.format, tok.into, &epoch); n != 2 || epoch != s.f.epoch {
			return nil, status.Error(codes.FailedPrecondition, "token expired")
		}
	}
	key := req.GetProject()
	changed := func(ver map[string]map[string]int, id string) bool {
		v, stamped := ver[key][id]
		return since == 0 || !stamped || v > since
	}
	var items []*syncpb.WorkItem
	kindFilter := req.GetKind()
	for _, id := range slices.Sorted(maps.Keys(s.f.itemsByID[key])) {
		m, ok := s.f.itemsByID[key][id].(map[string]any)
		if !ok || !changed(s.f.itemVer, id) || (scope != nil && !scope[id]) || s.f.omitOnCall[id] == s.f.gets {
			continue
		}
		kind, _ := m["kind"].(string)
		if kindFilter != "" && kind != kindFilter {
			continue
		}
		st, _ := m["status"].(string)
		title, _ := m["title"].(string)
		rec, _ := json.Marshal(m)
		items = append(items, &syncpb.WorkItem{
			Id: id, Kind: kind, Type: kind, Status: st, Title: title,
			Origin: "cli-sync", Record: rec,
		})
	}
	var rows []*syncpb.LedgerEntry
	for _, id := range slices.Sorted(maps.Keys(s.f.ledgerByID[key])) {
		m, ok := s.f.ledgerByID[key][id].(map[string]any)
		sid, _ := m["story_id"].(string)
		if !ok || !changed(s.f.ledgerVer, id) || (scope != nil && !scope[sid]) {
			continue
		}
		kind, _ := m["kind"].(string)
		rec, _ := json.Marshal(m)
		rows = append(rows, &syncpb.LedgerEntry{
			Id: id, StoryId: sid, Kind: kind, Type: "ledger",
			Origin: "cli-sync", Record: rec,
		})
	}
	token := fmt.Sprintf("v%d.%d", s.f.seq, s.f.epoch)
	size := int(req.GetPageSize())
	if size <= 0 {
		return &syncpb.SnapshotResponse{Items: items, Ledger: rows, SyncToken: token}, nil
	}
	window := func(n int) (int, int) {
		lo := min(off, n)
		if s.f.repeatBoundary && lo > 0 {
			lo--
		}
		return lo, min(off+size, n)
	}
	out := &syncpb.SnapshotResponse{}
	lo, hi := window(len(items))
	out.Items = items[lo:hi]
	lo, hi = window(len(rows))
	out.Ledger = rows[lo:hi]
	if off+size < max(len(items), len(rows)) {
		out.NextPageToken = fmt.Sprintf("o%d.%d", off+size, s.f.epoch)
	} else {
		out.SyncToken = token
	}
	return out, nil
}

func (f *fakeWorkstateServer) postCount(wsID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts[wsID])
}

func (f *fakeWorkstateServer) lastItems(wsID string) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	posts := f.posts[wsID]
	if len(posts) == 0 {
		return nil
	}
	items, _ := posts[len(posts)-1]["items"].([]any)
	return items
}

func (f *fakeWorkstateServer) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

// workstateRepo scaffolds a store-backed repo with the given [sync] work-state
// scopes and optional hosted workspace binding.
func workstateRepo(t *testing.T, satelleToml string) string {
	t.Helper()
	repo := tempRepo(t)
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	if err := os.WriteFile(cfgPath, []byte(satelleToml), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

// TestSyncWorkstatePushSkipsLocal: a bound repo whose account is not in the
// project's team resolves local — the skip message and no network call (AC1, AC5).
func TestSyncWorkstatePushSkipsLocal(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\nproject = \"probe\"\n")
	useScope(t, kindForeign)

	out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if !strings.Contains(out, "scope: local") || !strings.Contains(out, "is not a member") {
		t.Fatalf("expected local-skip message, got: %q", out)
	}
	if f.postCount("probe") != 0 {
		t.Error("local-scope push contacted the server")
	}
}

// A push records the scope it resolved: a push the account resolves local records
// local and leaves the seeded success as it was, and one it resolves personal
// records personal (sty_3df5f95a).
func TestSyncWorkstatePushRecordsScope(t *testing.T) {
	ts, _ := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, "[sync]\nproject = \"probe\"\n")
	acct := useScope(t, kindForeign)
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if err := syncstate.RecordPush(config.GlobalDir(), repo, true, "", "personal", t0); err != nil {
		t.Fatal(err)
	}

	if out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("local push: %v\n%s", err, out)
	}
	st, _, err := syncstate.Load(config.GlobalDir(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if st.Scope != "local" || !st.PushLastSuccess.Equal(t0) {
		t.Fatalf("local push: Scope %q, push_last_success %v; want local, %v", st.Scope, st.PushLastSuccess, t0)
	}

	// The account joins the project's team: no file changes.
	acct.mu.Lock()
	acct.fallback = kindPersonal
	acct.mu.Unlock()
	if out, err := runRoot(t, "story", "create", "--title", "Scope probe", "--body", "A story to push.", "--acceptance", "1. pushed"); err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	if out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("personal push: %v\n%s", err, out)
	}
	if st, _, _ = syncstate.Load(config.GlobalDir(), repo); st.Scope != "personal" || !st.PushLastSuccess.After(t0) {
		t.Fatalf("bound push: Scope %q, push_last_success %v; want personal, after %v", st.Scope, st.PushLastSuccess, t0)
	}
}

// TestSyncWorkstatePushRequiresBoundProject (AC5): opted-in workstate without a
// bound project fails client-side with zero network calls.
func TestSyncWorkstatePushRequiresBoundProject(t *testing.T) {
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n") // no project

	out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil && strings.Contains(err.Error(), "no sync project") {
		t.Fatalf("unset project should default to repo name, got %v\n%s", err, out)
	}
}

// TestSyncWorkstatePullSkipsLocal: a local scope skips with zero GET calls.
func TestSyncWorkstatePullSkipsLocal(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\nproject = \"probe\"\n")
	useScope(t, kindForeign)

	out, err := runRoot(t, "sync", "workstate", "pull", "--server", ts.URL)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if !strings.Contains(out, "scope: local") || !strings.Contains(out, "is not a member") {
		t.Fatalf("expected local-skip message, got: %q", out)
	}
	if f.getCount() != 0 {
		t.Errorf("local pull contacted server %d time(s)", f.getCount())
	}
}

func TestSyncWorkstateSnapshotSkipsLocal(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\nproject = \"probe\"\n")
	useScope(t, kindForeign)

	out, err := runRoot(t, "sync", "workstate", "snapshot", "--server", ts.URL)
	if err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	if !strings.Contains(out, "scope: local") || !strings.Contains(out, "is not a member") {
		t.Fatalf("expected local-skip message, got: %q", out)
	}
	if f.getCount() != 0 {
		t.Errorf("local snapshot contacted server %d time(s)", f.getCount())
	}
}

// TestSyncWorkstatePullRequiresBoundProject: no project → fail before network.
func TestSyncWorkstatePullRequiresBoundProject(t *testing.T) {
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n")

	out, err := runRoot(t, "sync", "workstate", "pull", "--server", ts.URL)
	if err != nil && strings.Contains(err.Error(), "no sync project") {
		t.Fatalf("unset project should default to repo name, got %v\n%s", err, out)
	}
}

// The conflict tests (same-id local change, ledger mismatch, --force override)
// live in cmd_sync_workstate_merge_test.go.

// The sty_948ad5df rule that a planted change patch never leaves the machine was
// reversed by sty_4d42e13e; its replacement, TestAttachmentExposure, lives in
// workstate_attachments_test.go.

func TestWorkstateItemWireEpicRoundTrip(t *testing.T) {
	parent := workitem.Item{
		ID: "sty_parent", Kind: workitem.KindStory, Status: "backlog", Title: "epic",
		Category: "epic-parent", Tags: []string{"epic:story-checkout", "order:0"},
		AcceptanceCriteria: "1. parent",
	}
	child := workitem.Item{
		ID: "sty_child", Kind: workitem.KindStory, Status: "backlog", Title: "child",
		Category: "feature", ParentID: "sty_parent",
		Tags:               []string{"epic:story-checkout", "order:1"},
		AcceptanceCriteria: "1. first\n2. second",
		Body:               "goal",
	}
	for _, it := range []workitem.Item{parent, child} {
		raw, err := marshalWorkstateItem(it)
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseWorkstateItem(hosted.WorkstateItem{ID: it.ID, Kind: string(it.Kind), Record: raw})
		if err != nil {
			t.Fatal(err)
		}
		if got.ParentID != it.ParentID || got.Category != it.Category || got.Kind != it.Kind {
			t.Errorf("shape: %+v vs %+v", got, it)
		}
		if got.AcceptanceCriteria != it.AcceptanceCriteria || got.Body != it.Body {
			t.Errorf("body/ac: %+v vs %+v", got, it)
		}
		if len(got.Tags) != len(it.Tags) {
			t.Fatalf("tags %v vs %v", got.Tags, it.Tags)
		}
		for i := range it.Tags {
			if got.Tags[i] != it.Tags[i] {
				t.Errorf("tag[%d] = %q want %q", i, got.Tags[i], it.Tags[i])
			}
		}
	}
}

// The sync help names the bound project as the destination, never a "personal
// collection" (sty_9914df10 AC1).
func TestSyncHelpNamesTheBoundProjectNotAPersonalCollection(t *testing.T) {
	for _, args := range [][]string{
		{"sync", "workstate", "--help"}, {"sync", "workstate", "push", "--help"}, {"sync", "workstate", "pull", "--help"},
		{"sync", "documents", "push", "--help"}, {"sync", "documents", "pull", "--help"},
		{"sync", "config", "push", "--help"}, {"sync", "config", "deploy", "--help"},
	} {
		out, err := runRoot(t, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		// Collapse whitespace so a phrase wrapped across a help line still matches.
		flat := strings.Join(strings.Fields(out), " ")
		if strings.Contains(flat, "personal collection") {
			t.Errorf("%v help still says \"personal collection\":\n%s", args, out)
		}
		if strings.Contains(flat, "personal workspace") {
			t.Errorf("%v help still names the personal workspace as the destination:\n%s", args, out)
		}
	}
	for _, args := range [][]string{
		{"sync", "workstate", "--help"}, {"sync", "workstate", "push", "--help"}, {"sync", "workstate", "pull", "--help"},
	} {
		out, _ := runRoot(t, args...)
		if !strings.Contains(strings.Join(strings.Fields(out), " "), "bound hosted project") {
			t.Errorf("%v help should name the bound project:\n%s", args, out)
		}
	}
}
