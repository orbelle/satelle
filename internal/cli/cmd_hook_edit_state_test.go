package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/testutil"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func TestEditPermitted(t *testing.T) {
	base := seatInfo{
		ItemID: "sty_x", State: "in_progress", StoryStatus: "in_progress",
		StateAgent: "executor", Engaged: true, EditCapable: true,
		EditStates: []string{"in_progress", "release"},
	}
	cases := []struct {
		name   string
		info   seatInfo
		marker dispatchMarker
		want   bool
	}{
		{"driving executor state", base, dispatchMarker{}, true},
		{"driving transition in flight", withSeat(base, func(s *seatInfo) { s.InFlight = true; s.State = "release" }), dispatchMarker{}, false},
		{"planning state", withSeat(base, func(s *seatInfo) {
			s.EditCapable = false
			s.State = "plan"
			s.StoryStatus = "plan"
			s.StateAgent = "planner"
		}), dispatchMarker{}, false},
		{"stale", withSeat(base, func(s *seatInfo) { s.Stale = true }), dispatchMarker{}, false},
		{"no seat", seatInfo{}, dispatchMarker{}, false},
		{"dispatched planner exact", withSeat(base, func(s *seatInfo) {
			s.InFlight = true
			s.EditCapable = false
			s.State = "plan"
			s.StoryStatus = "backlog"
			s.StateAgent = "planner"
		}), dispatchMarker{Agent: "planner", Step: "plan", Item: "sty_x"}, true},
		{"dispatched target while committed state remains engaging", withSeat(base, func(s *seatInfo) {
			s.InFlight = true
			s.EditCapable = false
			s.State = "plan"
			s.TargetState = "integration"
			s.StoryStatus = "plan"
			s.StateAgent = "coder"
			s.DispatchAgents = map[string][]string{"integration": {"coder"}}
		}), dispatchMarker{Agent: "coder", Step: "integration", Item: "sty_x"}, true},
		{"dispatched wrong agent", withSeat(base, func(s *seatInfo) {
			s.InFlight = true
			s.State = "plan"
			s.StateAgent = "planner"
		}), dispatchMarker{Agent: "coder", Step: "plan", Item: "sty_x"}, false},
		{"dispatched wrong item", withSeat(base, func(s *seatInfo) {
			s.InFlight = true
			s.State = "plan"
			s.StateAgent = "planner"
		}), dispatchMarker{Agent: "planner", Step: "plan", Item: "sty_other"}, false},
		// Entry dispatch is retired (sty_05a5e203): a reviewer park node allocates
		// no performer, so a marker claiming to be a dispatched agent on it is not
		// a dispatch this seat authorises.
		{"park entry dispatches nothing", withSeat(base, func(s *seatInfo) {
			s.InFlight = true
			s.State = "parked"
			s.StateAgent = "reviewer"
		}), dispatchMarker{Agent: "triage", Step: "parked", Item: "sty_x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := editPermitted(tc.info, tc.marker); got != tc.want {
				t.Fatalf("editPermitted(%+v, %+v) = %v, want %v", tc.info, tc.marker, got, tc.want)
			}
		})
	}
}

func withSeat(in seatInfo, mutate func(*seatInfo)) seatInfo {
	mutate(&in)
	return in
}

func TestEditPermissionDenyReasonsNameRecovery(t *testing.T) {
	now := time.Now().UTC()
	inFlight := seatInfo{
		ItemID: "sty_x", State: "integration", StoryStatus: "in_progress",
		StateAgent: "executor", Engaged: true, InFlight: true, Mine: true,
	}
	got := editPermissionDenyReason(inFlight, nil, "", now)
	for _, want := range []string{"IN FLIGHT", "sty_x", "integration", "satelle story attach"} {
		if !strings.Contains(got, want) {
			t.Errorf("in-flight reason missing %q: %s", want, got)
		}
	}
	plan := seatInfo{
		ItemID: "sty_x", State: "plan", StoryStatus: "plan",
		StateAgent: "planner", Engaged: true, EditStates: []string{"in_progress", "release"},
	}
	got = editPermissionDenyReason(inFlight, nil, "", now)
	if !strings.Contains(got, "the driving session cannot edit") {
		t.Errorf("driver in-flight must address the driver: %s", got)
	}
	inFlight.Mine = false
	got = editPermissionDenyReason(inFlight, nil, "", now)
	if strings.Contains(got, "the driving session cannot edit") {
		t.Errorf("unstamped/non-mine must not be addressed as the driver: %s", got)
	}
	if strings.Contains(got, "not the driver") {
		t.Errorf("unstamped/non-mine must not claim this session is not the driver: %s", got)
	}
	if !strings.Contains(got, "IN FLIGHT") || !strings.Contains(got, "sty_x") {
		t.Errorf("unstamped/non-mine must still name the in-flight story: %s", got)
	}
	got = editPermissionDenyReason(plan, nil, "", now)
	for _, want := range []string{`at "plan"`, `"planner"`, "in_progress, release", "Do not work ahead", "Read-only"} {
		if !strings.Contains(got, want) {
			t.Errorf("state reason missing %q: %s", want, got)
		}
	}
}

func editStateRepo(t *testing.T, status, leaseState string, inFlight bool) string {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "planned", "coded", "integrated", "released", "closed"]
park = { state = "blocked" }
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "planner"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]

[integrated]
status = "integration"
agent = "executor"
requires = ["coded"]

[released]
status = "release"
agent = "executor"
requires = ["integrated"]

[closed]
status = "done"
terminal = true
requires = ["released"]
`)
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "foo.go"), []byte("package internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	story, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "edit state", Body: "goal",
		AcceptanceCriteria: "1. ok", Status: status, Category: "feature",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if leaseState == "" {
		leaseState = status
	}
	if _, _, _, err := db.Leases.Acquire(ctx, story.ID, "story", lease.ResolveOwner(), leaseState, true); err != nil {
		t.Fatal(err)
	}
	if !inFlight {
		if err := db.Leases.Confirm(ctx, story.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// TestHookGateDispatchedPerformerSeesUnstampedInFlight: an isolated performer
// (SATELLE_DISPATCH_*, own hook session_id) must still resolve an unstamped
// in-flight seat so editPermitted's dispatch-marker branch can fire.
func TestHookGateDispatchedPerformerSeesUnstampedInFlight(t *testing.T) {
	editStateRepo(t, "in_progress", "integration", true)
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	origTree := sessionWorktree
	t.Cleanup(func() { sessionWorktree = origTree })
	sessionWorktree = func() string { return repo }
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	leases, err := db.Leases.List(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if len(leases) != 1 {
		_ = db.Close()
		t.Fatalf("want one lease, got %d", len(leases))
	}
	storyID := leases[0].ItemID
	_ = db.Leases.ForceRelease(ctx, storyID)
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: storyID, Kind: "story", Owner: lease.ResolveOwner(),
		State: "integration", StorySeat: true, Worktree: repo,
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	t.Setenv(config.DispatchAgentEnv, "executor")
	t.Setenv(config.DispatchStepEnv, "integration")
	t.Setenv(config.DispatchItemEnv, storyID)

	out, err := runRootIn(t, `{"session_id":"sess-performer","tool_input":{"file_path":"internal/foo.go"}}`, "hook", "gate")
	if err != nil {
		t.Fatalf("dispatched performer must see the unstamped in-flight seat: %v\n%s", err, out)
	}
}

// TestHookGatePrefersEnvStampOverPayload: a driving session that stamped the
// lease from SATELLE_SESSION must still resolve that seat when the harness
// payload carries a different session_id (the identity namespaces must meet).
func TestHookGatePrefersEnvStampOverPayload(t *testing.T) {
	editStateRepo(t, "in_progress", "in_progress", false)
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	origTree := sessionWorktree
	t.Cleanup(func() { sessionWorktree = origTree })
	sessionWorktree = func() string { return repo }

	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	leases, err := db.Leases.List(ctx)
	if err != nil || len(leases) != 1 {
		_ = db.Close()
		t.Fatalf("leases: %v n=%d", err, len(leases))
	}
	storyID := leases[0].ItemID
	_ = db.Leases.ForceRelease(ctx, storyID)
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: storyID, Kind: "story", Owner: lease.ResolveOwner(),
		State: "in_progress", StorySeat: true, Worktree: repo, SessionID: "sess-A",
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Leases.Confirm(ctx, storyID, "in_progress"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	t.Setenv(config.SessionEnv, "sess-A")
	out, err := runRootIn(t, `{"session_id":"harness-uuid","tool_input":{"file_path":"internal/foo.go"}}`, "hook", "gate")
	if err != nil {
		t.Fatalf("driver must keep its stamped seat when hook payload differs: %v\n%s", err, out)
	}
}

func TestGateFailsClosedForUnknownWorkflowStatus(t *testing.T) {
	prevHarness := hookHarnessFlag
	hookHarnessFlag = ""
	t.Cleanup(func() { hookHarnessFlag = prevHarness })
	editStateRepo(t, "unknown_state", "unknown_state", false)
	out, err := runRootIn(t, `{"tool_input":{"file_path":"internal/foo.go"}}`, "hook", "gate")
	if err == nil {
		t.Fatalf("gate allowed mutation when workflow status could not be classified:\n%s", out)
	}
	for _, want := range []string{"deny", "not declared", "cannot classify edit permission"} {
		if !strings.Contains(out+err.Error(), want) {
			t.Errorf("fail-closed output missing %q:\nout=%s\nerr=%v", want, out, err)
		}
	}
}

func TestHookEditPermittedRelay(t *testing.T) {
	base := seatInfo{
		ItemID: "sty_x", State: "in_progress", StoryStatus: "in_progress",
		StateAgent: "coder", Engaged: true, EditCapable: false,
		DispatchAgents: map[string][]string{"in_progress": {"coder"}},
		EditStates:     []string{"integration", "release"},
	}
	cases := []struct {
		name string
		info seatInfo
		dm   dispatchMarker
		rm   relayMarker
		want bool
	}{
		{"relay coder allocated", base, dispatchMarker{}, relayMarker{Binding: "coder", Item: "sty_x"}, true},
		{"relay binding not allocated", base, dispatchMarker{}, relayMarker{Binding: "reviewer-consult", Item: "sty_x"}, false},
		{"relay wrong item", base, dispatchMarker{}, relayMarker{Binding: "coder", Item: "sty_other"}, false},
		{"relay stale", withSeat(base, func(s *seatInfo) { s.Stale = true }), dispatchMarker{}, relayMarker{Binding: "coder", Item: "sty_x"}, false},
		{"relay in flight", withSeat(base, func(s *seatInfo) { s.InFlight = true }), dispatchMarker{}, relayMarker{Binding: "coder", Item: "sty_x"}, false},
		{"no marker driving at in_progress", base, dispatchMarker{}, relayMarker{}, false},
		{"no marker at executor status", withSeat(base, func(s *seatInfo) {
			s.StoryStatus = "integration"
			s.State = "integration"
			s.StateAgent = "executor"
			s.EditCapable = true
			s.DispatchAgents = map[string][]string{"integration": {"executor"}}
		}), dispatchMarker{}, relayMarker{}, true},
		{"in-flight dispatch marker unchanged", withSeat(base, func(s *seatInfo) {
			s.InFlight = true
			s.State = "integration"
			s.TargetState = "integration"
			s.StoryStatus = "in_progress"
			s.StateAgent = "coder"
			s.DispatchAgents = map[string][]string{"integration": {"coder"}}
		}), dispatchMarker{Agent: "coder", Step: "integration", Item: "sty_x"}, relayMarker{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hookEditPermitted(tc.info, tc.dm, tc.rm); got != tc.want {
				t.Fatalf("hookEditPermitted = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRelayDenyReason(t *testing.T) {
	now := time.Now().UTC()
	leaseA := "a4de0ec7-5809-4d35-8356-9f7c01769098"
	sessionB := "31261381-b59d-49af-8306-a8a2c925a9fe"
	liveForItem := []seatInfo{{
		ItemID: "sty_x", SessionID: leaseA, Engaged: true,
		State: "in_progress", StoryStatus: "in_progress", StateAgent: "coder",
	}}
	resolved := seatInfo{
		ItemID: "sty_x", State: "in_progress", StoryStatus: "in_progress",
		StateAgent: "coder", Engaged: true, EditCapable: false,
		DispatchAgents: map[string][]string{"in_progress": {"coder"}},
	}
	cases := []struct {
		name   string
		info   seatInfo
		live   []seatInfo
		rm     relayMarker
		sid    string
		want   []string
		forbid []string
	}{
		{
			name: "seat not bound but live exists",
			info: seatInfo{}, live: liveForItem,
			rm: relayMarker{Binding: "coder", Item: "sty_x"}, sid: sessionB,
			want:   []string{"sty_x", leaseA, sessionB, "rework relay"},
			forbid: []string{"without a performing story"},
		},
		{
			name: "no live seat for item",
			info: seatInfo{}, live: nil,
			rm: relayMarker{Binding: "coder", Item: "sty_x"}, sid: sessionB,
			want:   []string{"sty_x", "live, non-stale seat", "committed status"},
			forbid: []string{"without a performing story"},
		},
		{
			name: "stale seat",
			info: withSeat(resolved, func(s *seatInfo) { s.Stale = true }),
			rm:   relayMarker{Binding: "coder", Item: "sty_x"}, sid: leaseA,
			want: []string{"live, non-stale seat", "stale"},
		},
		{
			name: "in flight",
			info: withSeat(resolved, func(s *seatInfo) { s.InFlight = true; s.State = "integration" }),
			rm:   relayMarker{Binding: "coder", Item: "sty_x"}, sid: leaseA,
			want: []string{"in flight", "integration"},
		},
		{
			name: "binding not allocated",
			info: resolved,
			rm:   relayMarker{Binding: "reviewer-consult", Item: "sty_x"}, sid: leaseA,
			want:   []string{"rework relay", "reviewer-consult", "[coder]"},
			forbid: []string{"allocated to agent=executor"},
		},
		{
			name: "item mismatch",
			info: resolved,
			rm:   relayMarker{Binding: "coder", Item: "sty_other"}, sid: leaseA,
			want: []string{"sty_other", "sty_x"},
		},
		{
			name: "allocated binding resolved never names executor rule",
			info: resolved,
			rm:   relayMarker{Binding: "coder", Item: "sty_x"}, sid: leaseA,
			forbid: []string{"allocated to agent=executor"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := relayDenyReason(tc.info, tc.live, tc.rm, tc.sid, now)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in: %s", w, got)
				}
			}
			for _, f := range tc.forbid {
				if strings.Contains(got, f) {
					t.Errorf("forbidden %q in: %s", f, got)
				}
			}
		})
	}
}

func TestUnmarkedDenyReasonUnchanged(t *testing.T) {
	// empty seatInfo reaches firstDroppedPerformingSeat → app.Open → GlobalDir.
	testutil.IsolateHome(t)
	t.Chdir(t.TempDir())
	now := time.Now().UTC()
	cases := []seatInfo{
		{}, // not engaged
		{ItemID: "sty_x", State: "plan", StoryStatus: "plan", StateAgent: "planner", Engaged: true},
		{ItemID: "sty_x", State: "integration", StoryStatus: "in_progress", StateAgent: "executor", Engaged: true, InFlight: true, Mine: true},
	}
	for i, info := range cases {
		want := editPermissionDenyReason(info, nil, "sess", now)
		got := hookDenyReason(info, nil, dispatchMarker{}, relayMarker{}, "sess", now)
		if got != want {
			t.Errorf("case %d: unmarked hookDenyReason diverged from editPermissionDenyReason\n got: %s\nwant: %s", i, got, want)
		}
	}
}
