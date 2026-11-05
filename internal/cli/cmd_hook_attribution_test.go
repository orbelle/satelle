package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// attributionWFs is a route where the epic-level `ready` step is a performing
// step that does NOT wait on children, and `in_progress` is a dispatched coder
// step — the shape sty_fbbb4aee was observed on.
func attributionWFs() []docindex.Doc {
	return routeWFs(
		`["*"]
obligations = ["raised", "ready", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[ready]
status = "ready"
agent = "ready-reviewer"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "coder"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
}

// attributionRepo writes attributionWFs into a fresh repo, chdirs into it and
// returns the open store plus a helper that files a story at a status.
func attributionRepo(t *testing.T) (*store.DB, func(title, status string) workitem.Item) {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range attributionWFs() {
		if err := os.WriteFile(filepath.Join(wfDir, d.Name+".toml"), []byte(d.Body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, now); err != nil {
		t.Fatal(err)
	}
	create := func(title, status string) workitem.Item {
		it, err := db.Stories.Create(ctx, workitem.CreateInput{
			Kind: workitem.KindStory, Title: title, Body: "goal", AcceptanceCriteria: "1. ok",
			Status: status, Category: "feature",
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	return db, create
}

func stubWorktree(t *testing.T, tree string) {
	t.Helper()
	orig := sessionWorktree
	t.Cleanup(func() { sessionWorktree = orig })
	sessionWorktree = func() string { return tree }
}

// TestEditGateEpicReadySeatlessChildSeated (sty_fbbb4aee AC1/AC3): an epic left
// at `ready` with no seat and a child at `in_progress` holding the live seat for
// this worktree. However the session presents — unstamped, the lease's own id,
// or a different id — the deny must never name the seatless epic or tell the
// caller to re-acquire, and must not depend on which story sorts first.
func TestEditGateEpicReadySeatlessChildSeated(t *testing.T) {
	for _, epicFirst := range []bool{true, false} {
		name := "child first"
		if epicFirst {
			name = "epic first"
		}
		t.Run(name, func(t *testing.T) {
			db, create := attributionRepo(t)
			stubWorktree(t, "/w/child")
			var epic, child workitem.Item
			if epicFirst {
				epic = create("epic", "ready")
				child = create("child", "in_progress")
			} else {
				child = create("child", "in_progress")
				epic = create("epic", "ready")
			}
			if _, _, _, err := db.Leases.AcquireWith(context.Background(), lease.AcquireOpts{
				ItemID: child.ID, Kind: "story", Owner: "alice", State: "in_progress",
				StorySeat: true, Worktree: "/w/child", SessionID: "sess-child",
			}); err != nil {
				t.Fatal(err)
			}
			// Acquire opens the seat in flight; commit it as a settled seat.
			if err := db.Leases.Confirm(context.Background(), child.ID, "in_progress"); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()

			for _, sid := range []string{"", "sess-child", "sess-other"} {
				info, engaged, live, err := resolveSeats(false, sid)
				if err != nil {
					t.Fatal(err)
				}
				if len(live) != 1 || live[0].ItemID != child.ID {
					t.Fatalf("sid %q: live seats = %+v, want only the child", sid, live)
				}
				got := hookDenyReason(info, live, dispatchMarker{}, relayMarker{}, sid, now)
				if strings.Contains(got, epic.ID) {
					t.Errorf("sid %q (engaged=%v): deny named the seatless epic: %s", sid, engaged, got)
				}
				if strings.Contains(got, "seat was dropped") || strings.Contains(got, "re-acquire with") {
					t.Errorf("sid %q: deny sent the caller to re-acquire a seat it holds: %s", sid, got)
				}
				if !strings.Contains(got, child.ID) || !strings.Contains(got, `"in_progress"`) || !strings.Contains(got, "seat: live") {
					t.Errorf("sid %q: deny must name the child, its state and its live seat: %s", sid, got)
				}
			}
		})
	}
}

// TestEditGateDenyUnstampedMultiplePerforming (sty_fbbb4aee AC2): several
// performing stories, none seated, no session — the gate says so and lists them
// rather than picking the first and sending the caller to re-acquire it.
func TestEditGateDenyUnstampedMultiplePerforming(t *testing.T) {
	_, create := attributionRepo(t)
	stubWorktree(t, "/w/none")
	a := create("epic", "ready")
	b := create("child", "in_progress")
	now := time.Now().UTC()

	info, _, live, err := resolveSeats(false, "")
	if err != nil {
		t.Fatal(err)
	}
	got := hookDenyReason(info, live, dispatchMarker{}, relayMarker{}, "", now)
	for _, want := range []string{"more than one performing story, no session stamped", a.ID, b.ID, `"ready"`, `"in_progress"`, "seat: none"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	if strings.Contains(got, "re-acquire") || strings.Contains(got, "seat was dropped") {
		t.Errorf("an ambiguous session must not be told to re-acquire: %s", got)
	}

	// A single seatless performing story keeps the precise dropped-seat text.
	one := attributedReasonOrFail(t, nil, []seatInfo{{ItemID: "sty_only", StoryStatus: "plan"}}, "", "/w/x")
	if !strings.Contains(one, "seat was dropped") || !strings.Contains(one, "seat: none") {
		t.Errorf("sole dropped story must keep the dropped-seat text: %s", one)
	}
}

// seatStory gives it a settled live seat in worktree, held by session.
func seatStory(t *testing.T, db *store.DB, it workitem.Item, status, worktree, session string) {
	t.Helper()
	ctx := context.Background()
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: it.ID, Kind: "story", Owner: "alice", State: status,
		StorySeat: true, Worktree: worktree, SessionID: session,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Leases.Confirm(ctx, it.ID, status); err != nil {
		t.Fatal(err)
	}
}

// TestEditGateUnstampedLiveSeatPlusSeatlessStory (sty_fbbb4aee AC2): one live
// lease in another worktree, a second story performing with no lease, no session
// stamped, and this session bound to neither. That is two performing stories, so
// the gate reports the ambiguity and lists both — it does not resolve to the one
// seat (and so never allows an edit under it or names only its allocation).
func TestEditGateUnstampedLiveSeatPlusSeatlessStory(t *testing.T) {
	db, create := attributionRepo(t)
	stubWorktree(t, "/w/other")
	seated := create("seated", "in_progress")
	seatless := create("seatless", "ready")
	seatStory(t, db, seated, "in_progress", "/w/a", "sess-a")
	now := time.Now().UTC()

	info, engaged, live, err := resolveSeats(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if engaged {
		t.Fatalf("an unstamped session outside every seat's worktree with two performing stories must not resolve to %s", info.ItemID)
	}
	if hookEditPermitted(info, dispatchMarker{}, relayMarker{}) {
		t.Fatalf("the edit must not be permitted: %+v", info)
	}
	got := hookDenyReason(info, live, dispatchMarker{}, relayMarker{}, "", now)
	for _, want := range []string{"more than one performing story, no session stamped", seated.ID, seatless.ID, "seat: none", "seat: live"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	if strings.Contains(got, "re-acquire") || strings.Contains(got, "seat was dropped") {
		t.Errorf("an ambiguous session must not be told to re-acquire: %s", got)
	}

	// Permission is untouched where something binds the session: the worktree
	// match and a matching session id both still resolve to the seat.
	stubWorktree(t, "/w/a")
	if _, engaged, _, err := resolveSeats(false, ""); err != nil || !engaged {
		t.Errorf("worktree match must still resolve: engaged=%v err=%v", engaged, err)
	}
	stubWorktree(t, "/w/other")
	if pick, engaged, _, err := resolveSeats(false, "sess-a"); err != nil || !engaged || pick.ItemID != seated.ID {
		t.Errorf("a matching session id must still resolve: pick=%q engaged=%v err=%v", pick.ItemID, engaged, err)
	}
}

func attributedReasonOrFail(t *testing.T, live, dropped []seatInfo, sid, tree string) string {
	t.Helper()
	got, ok := attributedDenyReason(live, dropped, sid, tree)
	if !ok {
		t.Fatalf("attributedDenyReason(%+v, %+v) named nothing", live, dropped)
	}
	return got
}

// TestAttributedDenyReason pins the pure chooser's order: the live seat for this
// worktree, else the only performing story, else the ambiguity report.
func TestAttributedDenyReason(t *testing.T) {
	seated := seatInfo{ItemID: "sty_kid", StoryStatus: "in_progress", Engaged: true, Worktree: "/w/kid", SessionID: "sess-kid"}
	otherTree := seatInfo{ItemID: "sty_far", StoryStatus: "plan", Engaged: true, Worktree: "/w/far", SessionID: "sess-far"}
	seatless := seatInfo{ItemID: "sty_epic", StoryStatus: "ready"}

	cases := []struct {
		name          string
		live, dropped []seatInfo
		sid, tree     string
		want          []string
		notWant       []string
	}{
		{
			name: "seated worktree beats a seatless story sorted first",
			live: []seatInfo{seated}, dropped: []seatInfo{seatless}, sid: "", tree: "/w/kid",
			want:    []string{"sty_kid", `"in_progress"`, "seat: live (session sess-kid, worktree /w/kid)", "SATELLE_SESSION=sess-kid"},
			notWant: []string{"sty_epic", "seat was dropped"},
		},
		{
			name: "session mismatch still resolves the worktree seat",
			live: []seatInfo{otherTree, seated}, dropped: []seatInfo{seatless}, sid: "sess-x", tree: "/w/kid",
			want:    []string{"sty_kid", "this session is sess-x"},
			notWant: []string{"sty_epic", "sty_far", "seat was dropped"},
		},
		{
			name: "unstamped, several performing, no worktree seat",
			live: []seatInfo{otherTree}, dropped: []seatInfo{seatless}, sid: "", tree: "/w/none",
			want:    []string{"more than one performing story, no session stamped", "sty_far", "sty_epic", "seat: none"},
			notWant: []string{"seat was dropped", "re-acquire"},
		},
		{
			name: "unstamped, several LIVE seats, none for this worktree",
			live: []seatInfo{otherTree, seated}, sid: "", tree: "/w/none",
			want:    []string{"more than one performing story, no session stamped", "sty_far", "sty_kid"},
			notWant: []string{"seat was dropped", "re-acquire"},
		},
		{
			name: "stamped, several performing, no worktree seat",
			live: []seatInfo{otherTree}, dropped: []seatInfo{seatless}, sid: "sess-x", tree: "",
			want:    []string{"more than one performing story, none holds a seat for session sess-x"},
			notWant: []string{"seat was dropped"},
		},
		{
			name:    "sole seatless story is the dropped seat",
			dropped: []seatInfo{seatless}, sid: "", tree: "/w/none",
			want: []string{"sty_epic", `"ready"`, "seat: none", "seat was dropped"},
		},
		{
			name: "sole live story held elsewhere is named with its seat",
			live: []seatInfo{otherTree}, sid: "sess-x", tree: "/w/none",
			want:    []string{"sty_far", "seat: live (session sess-far, worktree /w/far)", "SATELLE_SESSION=sess-far"},
			notWant: []string{"seat was dropped"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := attributedReasonOrFail(t, tc.live, tc.dropped, tc.sid, tc.tree)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in: %s", w, got)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("must not contain %q: %s", w, got)
				}
			}
		})
	}

	if got, ok := attributedDenyReason(nil, nil, "", "/w/x"); ok || got != "" {
		t.Errorf("nothing performing must name nothing, got %q ok=%v", got, ok)
	}
}

// TestSeatMismatchEditReasonAdvice: "run from that seat's worktree" is advice
// only when the session is somewhere else; in the seat's own worktree the reason
// says the ids differ and names both.
func TestSeatMismatchEditReasonAdvice(t *testing.T) {
	seat := seatInfo{ItemID: "sty_kid", StoryStatus: "in_progress", Engaged: true, Worktree: "/w/kid", SessionID: "sess-kid"}

	same := seatMismatchEditReason(seat, "sess-x", "/w/kid")
	if strings.Contains(same, "run from that seat's worktree") {
		t.Errorf("same-worktree reason tells the caller to go where it already is: %s", same)
	}
	for _, want := range []string{"already in that worktree", "sess-x", "sess-kid"} {
		if !strings.Contains(same, want) {
			t.Errorf("same-worktree reason missing %q: %s", want, same)
		}
	}
	if strings.Contains(same, "SATELLE_SESSION") {
		t.Errorf("a caller with a resolved id must not be told to export a variable the hook cannot see: %s", same)
	}

	elsewhere := seatMismatchEditReason(seat, "sess-x", "/w/other")
	if !strings.Contains(elsewhere, "run from that seat's worktree") || strings.Contains(elsewhere, "already in that worktree") {
		t.Errorf("different-worktree reason must advise the seat's worktree: %s", elsewhere)
	}
}

// sty_36d6f155 AC3: a seat stamped with an id no live session publishes, in the
// caller's own worktree and engaged by the caller, is denied with the command that
// re-stamps it — not an environment variable — and never also says it need not be
// re-acquired. A live holder's stamp keeps the "held" sentence.
func TestSeatMismatchEditReasonGhostStamp(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	owner := lease.ResolveOwner()
	ghost := seatInfo{ItemID: "sty_kid", StoryStatus: "release", Engaged: true, Worktree: "/w/kid", SessionID: "ghost-id", Owner: owner}

	got := seatMismatchEditReason(ghost, "driver-id", "/w/kid")
	for _, want := range []string{"satelle story set sty_kid --status release", "no live session holds it"} {
		if !strings.Contains(got, want) {
			t.Errorf("ghost-stamp reason missing %q: %s", want, got)
		}
	}
	for _, bad := range []string{"SATELLE_SESSION", "does not need re-acquiring"} {
		if strings.Contains(got, bad) {
			t.Errorf("ghost-stamp reason must not contain %q: %s", bad, got)
		}
	}

	notDriver := ghost
	notDriver.Owner = "someone-else"
	if got := seatMismatchEditReason(notDriver, "driver-id", "/w/kid"); strings.Contains(got, "satelle story set") || !strings.Contains(got, "did not engage") {
		t.Errorf("a ghost seat engaged by another owner must not offer the re-stamp: %s", got)
	}

	config.PublishSession("live-holder")
	live := ghost
	live.SessionID = "live-holder"
	got = seatMismatchEditReason(live, "driver-id", "/w/kid")
	if strings.Contains(got, "satelle story set") || strings.Contains(got, "SATELLE_SESSION") || !strings.Contains(got, "does not need re-acquiring") || !strings.Contains(got, "another live session") {
		t.Errorf("a live foreign stamp must name the holder and keep the held sentence: %s", got)
	}

	// With no id resolved for the caller there is nothing to re-stamp to.
	if got := seatMismatchEditReason(ghost, "", "/w/kid"); !strings.Contains(got, "SATELLE_SESSION=ghost-id") {
		t.Errorf("an unresolved caller keeps the stamp hint: %s", got)
	}
}

// TestSeatTokenBranches (AC3): every seat state reads as a distinct token.
func TestSeatTokenBranches(t *testing.T) {
	if got := seatToken(seatInfo{Stale: true, Engaged: true}); got != "stale" {
		t.Errorf("stale = %q", got)
	}
	if got := seatToken(seatInfo{}); got != "none" {
		t.Errorf("none = %q", got)
	}
	if got := seatToken(seatInfo{Engaged: true}); got != "live (session unstamped, worktree unrecorded)" {
		t.Errorf("live blank = %q", got)
	}
}
