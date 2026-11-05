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

// TestSyncWorkstatePushPersonalAndIdempotent: opted-in stories push to personal
// workspace; a re-push succeeds (server upserts) (AC1, AC2, AC5).
func TestSyncWorkstatePushPersonalAndIdempotent(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")

	// Create a story so there is something to push.
	out, err := runRoot(t, "story", "create",
		"--title", "Workstate probe",
		"--body", "A story for the workstate push test.",
		"--acceptance", "1. pushed",
	)
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}

	out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if !strings.Contains(out, `project "probe"`) || strings.Contains(out, "personal collection") {
		t.Fatalf("push output should name the bound project as the destination: %q", out)
	}
	if f.postCount("probe") != 1 {
		t.Fatalf("personal posts = %d, want 1", f.postCount("probe"))
	}
	if f.postCount("ws-team") != 0 {
		t.Error("workstate must never post to the team workspace")
	}
	items := f.lastItems("probe")
	if len(items) == 0 {
		t.Fatal("expected at least one item in the push")
	}

	// No-op re-push: cursor advanced, nothing new — no POST (sty_88e83180 AC2).
	out2, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("re-push: %v\n%s", err, out2)
	}
	if f.postCount("probe") != 1 {
		t.Fatalf("no-op re-push posts = %d, want 1 (no second POST)", f.postCount("probe"))
	}
	if !strings.Contains(out2, "up to date") {
		t.Fatalf("no-op re-push should say up to date: %q", out2)
	}
}

// TestSyncWorkstatePushNoREST: the satelled child verb talks gRPC Apply only.
// A hosted origin that 500s every HTTP request still succeeds.
func TestSyncWorkstatePushNoREST(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hosted.DocumentSyncStatePathOverride = filepath.Join(t.TempDir(), "document-sync-state.json")
	t.Cleanup(func() { hosted.DocumentSyncStatePathOverride = "" })
	f := &fakeWorkstateServer{
		posts:      map[string][]map[string]any{},
		itemsByID:  map[string]map[string]any{},
		ledgerByID: map[string]map[string]any{},
	}
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/locations" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ok"}`))
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/workstate/items/") {
			http.NotFound(w, r)
			return
		}
		hits++
		t.Errorf("unexpected HTTP %s %s", r.Method, r.URL.Path)
		http.Error(w, "no REST", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	attachFakeWorkstateGRPC(t, f)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")

	out, err := runRoot(t, "story", "create",
		"--title", "Workstate no-REST probe",
		"--body", "A story for the workstate push no-REST test.",
		"--acceptance", "1. pushed",
	)
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if f.postCount("probe") != 1 {
		t.Fatalf("gRPC applies = %d, want 1", f.postCount("probe"))
	}
	if hits != 0 {
		t.Fatalf("HTTP hits = %d, want 0", hits)
	}
}

// TestSyncWorkstatePushIgnoresTeamBinding: even with active team workspace,
// work-state goes to personal only (AC1, AC3, AC5).
func TestSyncWorkstatePushIgnoresTeamBinding(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\nworkspace = \"Acme\"\n")

	out, err := runRoot(t, "story", "create",
		"--title", "Team binding probe",
		"--body", "Shared scope still goes personal.",
		"--acceptance", "1. personal dest",
	)
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if f.postCount("probe") != 1 {
		t.Fatalf("personal posts = %d, want 1 (shared scope still personal dest)", f.postCount("probe"))
	}
	if f.postCount("ws-team") != 0 {
		t.Error("shared-scope workstate must NOT route to team workspace")
	}
	if !strings.Contains(out, `project "probe"`) || strings.Contains(out, "personal collection") {
		t.Fatalf("output should confirm the bound project as dest: %q", out)
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

// TestSyncWorkstatePullDryRun: dry-run lists areas without GETs.
func TestSyncWorkstatePullDryRun(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")

	out, err := runRoot(t, "sync", "workstate", "pull", "--server", ts.URL, "--dry-run")
	if err != nil {
		t.Fatalf("pull dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Would pull") || !strings.Contains(out, "stories") {
		t.Fatalf("dry-run output: %q", out)
	}
	if f.getCount() != 0 {
		t.Errorf("dry-run contacted server %d time(s)", f.getCount())
	}
}

// TestSyncWorkstatePullRoundTrip: push → wipe local stories → pull restores id/AC.
func TestSyncWorkstatePullRoundTrip(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")

	out, err := runRoot(t, "story", "create",
		"--title", "Roundtrip story",
		"--body", "Body for rehydrate.",
		"--acceptance", "1. AC survives pull",
		"--category", "feature",
	)
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	// Parse id from create JSON if present; otherwise list.
	listOut, err := runRoot(t, "story", "list", "--limit", "5")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, listOut)
	}
	var stories []map[string]any
	if jerr := json.Unmarshal([]byte(listOut), &stories); jerr != nil {
		t.Fatalf("parse list: %v\n%s", jerr, listOut)
	}
	var styID string
	for _, s := range stories {
		if title, _ := s["title"].(string); title == "Roundtrip story" {
			styID, _ = s["id"].(string)
			break
		}
	}
	if styID == "" {
		t.Fatalf("story not found in list: %s", listOut)
	}

	out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if f.postCount("probe") != 1 {
		t.Fatalf("posts = %d", f.postCount("probe"))
	}

	// Wipe local work items by opening store via a second push path isn't available;
	// delete via story is not archive-all. Use SQL-free approach: remove the
	// home-keyed runtime DB (sty_4660bbe1) and re-open empty.
	dbPath := runtimeDBPath(t)
	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	// Confirm empty.
	listOut, err = runRoot(t, "story", "list", "--limit", "5")
	if err != nil {
		t.Fatalf("list after wipe: %v\n%s", err, listOut)
	}
	if strings.Contains(listOut, styID) {
		t.Fatalf("story still present after db wipe: %s", listOut)
	}

	// --dry-run=false: registered cobra flag vars persist across tests in-process.
	out, err = runRoot(t, "sync", "workstate", "pull", "--server", ts.URL, "--dry-run=false")
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Pulled work-state") {
		t.Fatalf("pull output: %q", out)
	}
	if !strings.Contains(out, `project "probe"`) || strings.Contains(out, "personal collection") {
		t.Fatalf("pull output should name the bound project as the source: %q", out)
	}

	got, err := runRoot(t, "story", "get", styID)
	if err != nil {
		t.Fatalf("get after pull: %v\n%s", err, got)
	}
	if !strings.Contains(got, "Roundtrip story") {
		t.Errorf("title missing: %s", got)
	}
	if !strings.Contains(got, "AC survives pull") {
		t.Errorf("acceptance_criteria missing after pull: %s", got)
	}
}

func TestSyncWorkstateSnapshotRoundTrip(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")

	out, err := runRoot(t, "story", "create",
		"--title", "Snapshot story",
		"--body", "Body for snapshot.",
		"--acceptance", "1. AC survives snapshot",
		"--category", "feature",
	)
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	listOut, err := runRoot(t, "story", "list", "--limit", "5")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, listOut)
	}
	var stories []map[string]any
	if jerr := json.Unmarshal([]byte(listOut), &stories); jerr != nil {
		t.Fatalf("parse list: %v\n%s", jerr, listOut)
	}
	var styID string
	for _, s := range stories {
		if title, _ := s["title"].(string); title == "Snapshot story" {
			styID, _ = s["id"].(string)
			break
		}
	}
	if styID == "" {
		t.Fatalf("story not found: %s", listOut)
	}
	out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if f.postCount("probe") < 1 {
		t.Fatalf("posts = %d", f.postCount("probe"))
	}
	dbPath := runtimeDBPath(t)
	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	out, err = runRoot(t, "sync", "workstate", "snapshot", "--server", ts.URL)
	if err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Snapshot work-state") {
		t.Fatalf("snapshot output: %q", out)
	}
	got, err := runRoot(t, "story", "get", styID)
	if err != nil {
		t.Fatalf("get after snapshot: %v\n%s", err, got)
	}
	if !strings.Contains(got, "Snapshot story") {
		t.Errorf("title missing: %s", got)
	}
}

// The conflict tests (same-id local change, ledger mismatch, --force override)
// live in cmd_sync_workstate_merge_test.go.

// TestSyncWorkstatePullIgnoresTeamBinding: GETs personal only.
func TestSyncWorkstatePullIgnoresTeamBinding(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\nworkspace = \"Acme\"\n")

	// Seed hosted via push first.
	out, err := runRoot(t, "story", "create",
		"--title", "Team ignore pull",
		"--body", "b",
		"--acceptance", "1. a",
	)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if _, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("push: %v", err)
	}
	// Empty local DB then pull (home-keyed runtime plane).
	dbPath := runtimeDBPath(t)
	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	out, err = runRoot(t, "sync", "workstate", "pull", "--server", ts.URL, "--dry-run=false")
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if f.getCount() == 0 {
		t.Error("expected GET to personal")
	}
}

// TestSyncWorkstatePushCursorSurvivesMidRunFailure (sty_88e83180 AC3): force two
// item chunks; the second POST fails after the first succeeded; cursor stays
// pre-run so the next push re-sends the unconfirmed chunk.
// TestSyncWorkstatePushCursorSurvivesMidRunFailure (sty_88e83180 AC3 +
// sty_c526753a AC5): Apply failure is a non-nil error and the cursor does not
// advance. satelle sync stays the manual escape hatch.
func TestSyncWorkstatePushCursorSurvivesMidRunFailure(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo := workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")

	prevItem, prevLed := workstateItemChunk, workstateLedgerChunk
	workstateItemChunk, workstateLedgerChunk = 1, 1
	t.Cleanup(func() {
		workstateItemChunk, workstateLedgerChunk = prevItem, prevLed
	})

	// Two stories, no prior cursor → two POSTs (chunk size 1). Fail the second.
	for _, title := range []string{"Chunk A", "Chunk B"} {
		out, err := runRoot(t, "story", "create",
			"--title", title, "--body", "body", "--acceptance", "1. ok", "--category", "chore")
		if err != nil {
			t.Fatalf("create %s: %v\n%s", title, err, out)
		}
	}
	f.mu.Lock()
	f.failOnPost = 2 // first chunk lands, second fails
	f.mu.Unlock()

	out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err == nil {
		t.Fatalf("expected mid-run failure, got: %s", out)
	}
	// Cursor must not advance: first chunk confirmed, second failed.
	cur, err := hosted.LoadWorkstateCursor(ts.URL, "probe", repo)
	if err != nil {
		t.Fatal(err)
	}
	if !cur.ItemsUpdatedAt.IsZero() {
		t.Fatalf("cursor must stay zero after partial failure, got %v", cur.ItemsUpdatedAt)
	}

	// Recovery: clear failure, full push (still no cursor) re-sends both.
	f.mu.Lock()
	f.failOnPost = 0
	f.mu.Unlock()
	out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("recovery push: %v\n%s", err, out)
	}
	cur, err = hosted.LoadWorkstateCursor(ts.URL, "probe", repo)
	if err != nil {
		t.Fatal(err)
	}
	if cur.ItemsUpdatedAt.IsZero() {
		t.Fatal("cursor should advance after full success")
	}
	// Accumulated map has both stories.
	f.mu.Lock()
	n := len(f.itemsByID["probe"])
	f.mu.Unlock()
	if n < 2 {
		t.Fatalf("accumulated items = %d, want ≥2", n)
	}
}

// TestSyncWorkstateIncrementalRehydrate (sty_88e83180 AC8): full push, mutate,
// incremental push, wipe local DB, pull — every id restores with current content.
func TestSyncWorkstateIncrementalRehydrate(t *testing.T) {
	ts, _ := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")

	out, err := runRoot(t, "story", "create",
		"--title", "Keep me", "--body", "body", "--acceptance", "1. ok", "--category", "feature")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	listOut, _ := runRoot(t, "story", "list", "--limit", "5")
	var stories []map[string]any
	_ = json.Unmarshal([]byte(listOut), &stories)
	var keepID string
	for _, s := range stories {
		if s["title"] == "Keep me" {
			keepID, _ = s["id"].(string)
		}
	}
	if keepID == "" {
		t.Fatal("keep id missing")
	}
	if out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("full push: %v\n%s", err, out)
	}

	// Mutate + create.
	if out, err = runRoot(t, "story", "set", keepID, "--body", "updated body"); err != nil {
		t.Fatalf("mutate: %v\n%s", err, out)
	}
	if out, err = runRoot(t, "story", "create",
		"--title", "New after", "--body", "new", "--acceptance", "1. ok", "--category", "feature"); err != nil {
		t.Fatalf("create2: %v\n%s", err, out)
	}
	if out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("incremental push: %v\n%s", err, out)
	}

	// Wipe local.
	dbPath := runtimeDBPath(t)
	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	if out, err = runRoot(t, "sync", "workstate", "pull", "--server", ts.URL, "--dry-run=false"); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	got, err := runRoot(t, "story", "get", keepID)
	if err != nil {
		t.Fatalf("get keep: %v\n%s", err, got)
	}
	if !strings.Contains(got, "updated body") {
		t.Errorf("mutated body missing after rehydrate: %s", got)
	}
	listOut, _ = runRoot(t, "story", "list", "--limit", "10")
	if !strings.Contains(listOut, "New after") {
		t.Errorf("new story missing after rehydrate: %s", listOut)
	}
}

// TestSyncWorkstatePushIncrementalOnlyChanged (sty_88e83180 AC1 +
// sty_c526753a AC4): after a full push, mutating one of two stories makes the
// next POST carry only that id. Down-window rows (created with no push, as if
// satelled was down) ride a later push via the stored cursor.
func TestSyncWorkstatePushIncrementalOnlyChanged(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")

	var ids []string
	for _, title := range []string{"Alpha", "Beta"} {
		out, err := runRoot(t, "story", "create",
			"--title", title, "--body", "b", "--acceptance", "1. ok", "--category", "chore")
		if err != nil {
			t.Fatalf("create: %v\n%s", err, out)
		}
	}
	listOut, _ := runRoot(t, "story", "list", "--limit", "10")
	var stories []map[string]any
	_ = json.Unmarshal([]byte(listOut), &stories)
	for _, s := range stories {
		if id, _ := s["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) < 2 {
		t.Fatalf("need 2 stories, got %v", ids)
	}
	if out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("full: %v\n%s", err, out)
	}
	n1 := f.postCount("probe")

	// Touch only the first story.
	if out, err := runRoot(t, "story", "set", ids[0], "--body", "changed"); err != nil {
		t.Fatalf("mutate: %v\n%s", err, out)
	}
	if out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("incr: %v\n%s", err, out)
	}
	if f.postCount("probe") != n1+1 {
		t.Fatalf("posts = %d, want %d", f.postCount("probe"), n1+1)
	}
	items := f.lastItems("probe")
	if len(items) != 1 {
		t.Fatalf("incremental batch size = %d, want 1: %+v", len(items), items)
	}
	m, _ := items[0].(map[string]any)
	if id, _ := m["id"].(string); id != ids[0] {
		t.Fatalf("incremental id = %q, want %q", id, ids[0])
	}
}

// The sty_948ad5df rule that a planted change patch never leaves the machine was
// reversed by sty_4d42e13e; its replacement, TestAttachmentExposure, lives in
// workstate_attachments_test.go.

// TestWorkstatePushExcludesBinaryAttachment (sty_40e5a305 AC9): a binary
// attachment under the runtime stories dir never rides the workstate mirror.
func TestWorkstatePushExcludesBinaryAttachment(t *testing.T) {
	const plant = "BIN40E5A305-WORKSTATE-EXCLUSION-MARKER"
	// Minimal PNG-like bytes with planted ASCII so a leak is greppable.
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
		0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54,
		0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
		0x0d, 0x0a, 0x2d, 0xb4,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44,
		0xae, 0x42, 0x60, 0x82,
	}
	png = append(png, []byte(plant)...)

	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	_ = workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")

	out, err := runRoot(t, "story", "create",
		"--title", "Binary exclusion",
		"--body", "Binary attachments stay off the workstate wire.",
		"--acceptance", "1. binary not in push",
	)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created map[string]any
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&created); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	id, _ := created["id"].(string)
	binPath := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(binPath, png, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runRoot(t, "story", "attach", id,
		"--name", "shot.png", "--type", "screenshot",
		"--binary-file", binPath, "--content-type", "image/png",
	); err != nil {
		t.Fatalf("binary attach: %v\n%s", err, out)
	}

	if out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	blob, _ := json.Marshal(f.posts["probe"])
	if strings.Contains(string(blob), plant) {
		t.Error("binary plant must not appear in workstate POST bodies")
	}
	if strings.Contains(string(blob), "shot.png") && strings.Contains(string(blob), "data_base64") {
		t.Error("binary attachment payload must not ride workstate")
	}
}

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
