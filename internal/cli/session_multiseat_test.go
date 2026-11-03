package cli

// sty_42231b74 — one session can hold stamped seats in several worktrees (one per
// epic child). An edit, commit or substrate-lock check is attributed by TREE: the
// seat whose worktree holds the edit target, else the seat of the session's own
// tree, else none — never by the order the store lists the seats in. These tests
// run every attribution in both store orders.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

const multiSeatSession = "sess-multi"

// multiSeat is a repo (the session's anchor tree, also its cwd tree) plus a second
// linked-worktree-shaped tree, each seating one story stamped with ONE session id.
type multiSeat struct {
	repo, wtB string
	a, b      workitem.Item
}

// writeGitMarker makes dir a git-tree root for gitRootOf. A linked worktree's
// .git is a regular file, so a file is the faithful marker for both trees.
func writeGitMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// multiSeatRepo seats A in the anchor tree and B in wtB under multiSeatSession.
// aFirst picks which seat the store lists first (leases list by acquired_at). The
// session's own tree is the anchor. gateExtra is appended to the config.
func multiSeatRepo(t *testing.T, aStatus, aCategory, bStatus string, aFirst bool, gateExtra string) multiSeat {
	t.Helper()
	repo := editStateRepo(t, aStatus, aStatus, false)
	// B's tree must lie outside every temp root: a write to an out-of-repo temp
	// path is exempt before any seat is judged, which would make an edit into B
	// pass whichever seat it was attributed to.
	wtB, err := os.MkdirTemp("/var/tmp", "satelle-multiseat-")
	if err != nil {
		t.Skipf("cannot place a linked tree outside the temp roots: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wtB) })
	writeGitMarker(t, repo)
	writeGitMarker(t, wtB)
	stubWorktree(t, repo)
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.toml"), []byte(lockExemptToml+gateExtra), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	items, err := db.Stories.List(ctx, workitem.ListFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("stories: %v n=%d", err, len(items))
	}
	a := items[0]
	if a, err = db.Stories.Update(ctx, a.ID, workitem.UpdateInput{Category: &aCategory}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	b, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "sibling", Body: "goal", AcceptanceCriteria: "1. ok",
		Status: bStatus, Category: "feature",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Leases.ForceRelease(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	type seat struct {
		it     workitem.Item
		status string
		tree   string
	}
	seats := []seat{{a, aStatus, repo}, {b, bStatus, wtB}}
	if !aFirst {
		seats[0], seats[1] = seats[1], seats[0]
	}
	for _, s := range seats {
		if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
			ItemID: s.it.ID, Kind: "story", Owner: "alice", State: s.status, StorySeat: true,
			SeatKey: "epic-parent", Worktree: s.tree, SessionID: multiSeatSession,
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Leases.Confirm(ctx, s.it.ID, s.status); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // time-subject: distinct acquired_at, the store order is the acquisition order
	}
	live, err := db.Leases.List(ctx)
	if err != nil || len(live) != 2 {
		t.Fatalf("fixture must hold two live seats: %v n=%d", err, len(live))
	}
	if first := live[0].ItemID; (first == a.ID) != aFirst {
		t.Fatalf("store lists %s first, aFirst=%v", first, aFirst)
	}
	return multiSeat{repo: repo, wtB: wtB, a: a, b: b}
}

// multiSeatTwoElsewhere seats two stories in two linked trees neither of which is
// the session's own tree (the anchor), same session id.
func multiSeatTwoElsewhere(t *testing.T) (repo, wtX, wtY string, x, y workitem.Item) {
	t.Helper()
	return multiSeatTwoElsewhereOrdered(t, false)
}

// multiSeatTwoElsewhereOrdered is multiSeatTwoElsewhere with the store order
// chosen: xFirst lists X (at plan, in wtX) before Y (at in_progress, in wtY).
// Leases list by seat_key then acquired_at, so the seat acquired last lists last.
func multiSeatTwoElsewhereOrdered(t *testing.T, xFirst bool) (repo, wtX, wtY string, x, y workitem.Item) {
	t.Helper()
	f := multiSeatRepo(t, "plan", "feature", "in_progress", true, "")
	// Re-seat A out of the anchor tree: the anchor then holds no seat of the session.
	wtX = t.TempDir()
	writeGitMarker(t, wtX)
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if err := db.Leases.ForceRelease(ctx, f.a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: f.a.ID, Kind: "story", Owner: "alice", State: "plan", StorySeat: true,
		SeatKey: "epic-parent", Worktree: wtX, SessionID: multiSeatSession,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Leases.Confirm(ctx, f.a.ID, "plan"); err != nil {
		t.Fatal(err)
	}
	if xFirst {
		// Re-seat Y after X so the store lists X first.
		time.Sleep(5 * time.Millisecond) // time-subject: distinct acquired_at, Y is re-seated strictly after X
		if err := db.Leases.ForceRelease(ctx, f.b.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
			ItemID: f.b.ID, Kind: "story", Owner: "alice", State: "in_progress", StorySeat: true,
			SeatKey: "epic-parent", Worktree: f.wtB, SessionID: multiSeatSession,
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Leases.Confirm(ctx, f.b.ID, "in_progress"); err != nil {
			t.Fatal(err)
		}
	}
	live, err := db.Leases.List(ctx)
	if err != nil || len(live) != 2 {
		t.Fatalf("fixture must hold two live seats: %v n=%d", err, len(live))
	}
	if first := live[0].ItemID; (first == f.a.ID) != xFirst {
		t.Fatalf("store lists %s first, xFirst=%v", first, xFirst)
	}
	return f.repo, wtX, f.wtB, f.a, f.b
}

func multiSeatEvent(tool string, input map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": multiSeatSession, "transcript_path": "/x/transcript.jsonl",
		"tool_name": tool, "tool_input": input,
	})
	return string(b)
}

func multiSeatEdit(path string) string {
	return multiSeatEvent("Edit", map[string]any{"file_path": path, "old_string": "a", "new_string": "b"})
}

func multiSeatBash(command string) string {
	return multiSeatEvent("Bash", map[string]any{"command": command})
}

func bothOrders(t *testing.T, run func(t *testing.T, aFirst bool)) {
	t.Helper()
	for _, aFirst := range []bool{true, false} {
		name := "anchor seat listed second"
		if aFirst {
			name = "anchor seat listed first"
		}
		t.Run(name, func(t *testing.T) { run(t, aFirst) })
	}
}

// AC1: an edit inside the anchor tree is judged against the anchor's seat. When
// that is the substrate story, the substrate lock passes — even though the
// session's other seat (a product story at plan) would refuse it.
func TestMultiSeatSubstrateLockJudgedAgainstAnchorSeat(t *testing.T) {
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "in_progress", "substrate", "plan", aFirst, "")
		target := filepath.Join(f.repo, ".satelle", "satelle.toml")
		if out, err := runRootIn(t, multiSeatEdit(target), "hook", "gate"); err != nil {
			t.Fatalf("the substrate story holds the anchor tree's seat, so the lock must pass: %v\n%s", err, out)
		}
	})
}

// AC1: the anchor seat's OWN refusal applies when it sits at plan, though the
// session's other seat is at an executor state that would allow the edit.
func TestMultiSeatEditJudgedAgainstAnchorSeatPlanRefusal(t *testing.T) {
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "plan", "feature", "in_progress", aFirst, "")
		out, err := runRootIn(t, multiSeatEdit(filepath.Join(f.repo, "internal", "foo.go")), "hook", "gate")
		if err == nil {
			t.Fatalf("the anchor seat is at plan, so the edit must be refused:\n%s", out)
		}
		reason := denyReasonOf(t, out)
		if !strings.Contains(reason, f.a.ID) && !strings.Contains(reason, `"plan"`) {
			t.Errorf("the refusal must be the plan seat's: %s", reason)
		}
		if strings.Contains(reason, f.b.ID) {
			t.Errorf("the refusal must not be attributed to the sibling seat %s: %s", f.b.ID, reason)
		}
	})
}

// AC1: a commit from the anchor tree is judged against the anchor's seat.
func TestMultiSeatCommitgateJudgedAgainstAnchorSeat(t *testing.T) {
	const gate = "\n[gate.command_allow]\ncommit = [\"in_progress\"]\n"
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "in_progress", "feature", "plan", aFirst, gate)
		if out, err := runRootIn(t, multiSeatBash("git commit -m x"), "hook", "commitgate"); err != nil {
			t.Fatalf("the anchor seat is at in_progress, so the commit is allowed: %v\n%s", err, out)
		}
		_ = f
	})
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "plan", "feature", "in_progress", aFirst, gate)
		out, err := runRootIn(t, multiSeatBash("git commit -m x"), "hook", "commitgate")
		if err == nil {
			t.Fatalf("the anchor seat is at plan, so the commit must be refused:\n%s", out)
		}
		if reason := denyReasonOf(t, out); !strings.Contains(reason, `"plan"`) {
			t.Errorf("the refusal must name the anchor seat's status: %s", reason)
		}
		_ = f
	})
}

// AC1: with the fence opted out, an edit into B's tree is judged against B.
func TestMultiSeatEditIntoOtherTreeJudgedAgainstItsSeat(t *testing.T) {
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "plan", "feature", "in_progress", aFirst, "allow_outside_tree_edits = true\n")
		if out, err := runRootIn(t, multiSeatEdit(filepath.Join(f.wtB, "foo.go")), "hook", "gate"); err != nil {
			t.Fatalf("B is at in_progress and holds the target tree, so the edit is allowed: %v\n%s", err, out)
		}
		// The anchor tree is still A's: plan refuses.
		if out, err := runRootIn(t, multiSeatEdit(filepath.Join(f.repo, "internal", "foo.go")), "hook", "gate"); err == nil {
			t.Fatalf("an edit in the anchor tree is still judged against A (plan):\n%s", out)
		}
	})
}

// A target in no git tree (scratch) falls back to the session's own tree; the
// refusal is the anchor seat's, whichever order the store lists them in.
func TestMultiSeatNonRepoTargetFallsBackToSessionTree(t *testing.T) {
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "plan", "feature", "in_progress", aFirst, "")
		info, engaged, _, err := resolveSeatsFor(false, multiSeatSession, treeOf("/nonexistent-scratch/x.md"))
		if err != nil || !engaged || info.ItemID != f.a.ID {
			t.Fatalf("scratch target must attribute to the session-tree seat %s, got %q engaged=%v err=%v", f.a.ID, info.ItemID, engaged, err)
		}
	})
}

// AC2: resolveSeat has no target and picks the session-tree seat, whichever order
// the store lists the seats in — the rule every caller (fix claim, story rework,
// status line, stopcheck, substrate status, SessionStart) inherits.
func TestResolveSeatNoTargetPicksSessionTreeSeat(t *testing.T) {
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "in_progress", "feature", "plan", aFirst, "")
		for _, tc := range []struct {
			tree string
			want string
		}{{f.repo, f.a.ID}, {f.wtB, f.b.ID}} {
			stubWorktree(t, tc.tree)
			info, engaged, err := resolveSeat(false, multiSeatSession)
			if err != nil || !engaged || info.ItemID != tc.want || !info.Mine {
				t.Fatalf("session tree %s: got %q engaged=%v mine=%v err=%v, want %s", tc.tree, info.ItemID, engaged, info.Mine, err, tc.want)
			}
		}
	})
}

// pickSessionSeatFor is the one chooser: target tree, then session tree, then none.
func TestPickSessionSeatForChoosesByTree(t *testing.T) {
	x := seatInfo{ItemID: "sty_x", SessionID: "sess", Worktree: "/w/x"}
	y := seatInfo{ItemID: "sty_y", SessionID: "sess", Worktree: "/w/y"}
	other := seatInfo{ItemID: "sty_o", SessionID: "other", Worktree: "/w/o"}
	for _, live := range [][]seatInfo{{x, y, other}, {other, y, x}, {y, x}} {
		stubWorktree(t, "/w/y")
		if got, mine := pickSessionSeatFor(live, nil, "sess", "/w/x"); got.ItemID != "sty_x" || !mine {
			t.Errorf("target tree /w/x: got %q mine=%v", got.ItemID, mine)
		}
		if got, mine := pickSessionSeatFor(live, nil, "sess", ""); got.ItemID != "sty_y" || !mine {
			t.Errorf("no target, session tree /w/y: got %q mine=%v", got.ItemID, mine)
		}
		// A target in no seat's tree falls to the session's tree.
		if got, _ := pickSessionSeatFor(live, nil, "sess", "/w/elsewhere"); got.ItemID != "sty_y" {
			t.Errorf("unmatched target tree: got %q, want the session-tree seat", got.ItemID)
		}
		if got, mine := pickSessionSeat(live, nil, "sess"); got.ItemID != "sty_y" || !mine {
			t.Errorf("pickSessionSeat must share the rule: got %q mine=%v", got.ItemID, mine)
		}
		// Neither tree matches: no pick.
		stubWorktree(t, "/w/none")
		if got, _ := pickSessionSeatFor(live, nil, "sess", "/w/elsewhere"); got.ItemID != "" {
			t.Errorf("no seat in either tree must pick none, got %q", got.ItemID)
		}
	}
	// Exactly one stamped seat resolves as today, whatever the trees say.
	stubWorktree(t, "/w/none")
	if got, mine := pickSessionSeatFor([]seatInfo{other, x}, nil, "sess", "/w/y"); got.ItemID != "sty_x" || !mine {
		t.Errorf("a single stamped seat must resolve as today, got %q mine=%v", got.ItemID, mine)
	}
}

func TestTreeOf(t *testing.T) {
	f := multiSeatRepo(t, "plan", "feature", "plan", true, "")
	for _, tc := range []struct{ name, target, want string }{
		{"file in the anchor tree", filepath.Join(f.repo, "internal", "foo.go"), f.repo},
		{"new file in a new dir of the anchor tree", filepath.Join(f.repo, "no", "such", "new.go"), f.repo},
		{"file in a linked tree", filepath.Join(f.wtB, "x.go"), f.wtB},
		{"repo-relative path is the anchor's", "internal/foo.go", f.repo},
		{"scratch outside every tree", "/nonexistent-scratch/x.md", ""},
		{"no target (a Bash event)", "", ""},
	} {
		if got := treeOf(tc.target); got != tc.want {
			t.Errorf("%s: treeOf(%q) = %q, want %q", tc.name, tc.target, got, tc.want)
		}
	}
}

// AC3: the session holds two seats and neither is in the target's tree or the
// session's tree — nothing is picked, and the gate and commitgate name every seat
// with its status and worktree.
func TestMultiSeatNoMatchingSeatNamesEverySeat(t *testing.T) {
	repo, wtX, wtY, x, y := multiSeatTwoElsewhere(t)
	check := func(t *testing.T, label, out string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: a session whose seats are all elsewhere must be refused:\n%s", label, out)
		}
		reason := denyReasonOf(t, out)
		for _, want := range []string{x.ID, y.ID, `"plan"`, `"in_progress"`, wtX, wtY} {
			if !strings.Contains(reason, want) {
				t.Errorf("%s: deny missing %q: %s", label, want, reason)
			}
		}
		if strings.Contains(reason, noEngagedStoryEditReason) {
			t.Errorf("%s: the generic engage-a-story text must not stand in for the seat list: %s", label, reason)
		}
	}
	out, err := runRootIn(t, multiSeatEdit(filepath.Join(repo, "internal", "foo.go")), "hook", "gate")
	check(t, "gate", out, err)
	out, err = runRootIn(t, multiSeatBash("echo x > "+filepath.Join(repo, "internal", "foo.go")), "hook", "commitgate")
	check(t, "commitgate mutation", out, err)
	out, err = runRootIn(t, multiSeatBash("git commit -m x"), "hook", "commitgate")
	check(t, "commitgate commit", out, err)
}

const commitInProgressOnly = "\n[gate.command_allow]\ncommit = [\"in_progress\"]\n"

// sty_3a9b06fe AC1: a commit that a leading cd moved into a worktree is judged
// against that worktree's seat, whichever order the store lists the seats in.
func TestMultiSeatCommitFromWorktreeAttributedToItsSeat(t *testing.T) {
	// The session's own tree holds none of its seats: such a commit used to be
	// refused as unattributable.
	bothOrders(t, func(t *testing.T, xFirst bool) {
		_, _, wtY, _, _ := multiSeatTwoElsewhereOrdered(t, xFirst)
		if out, err := runRootIn(t, multiSeatBash("cd "+wtY+" && git commit -m x"), "hook", "commitgate"); err != nil {
			t.Fatalf("the commit runs in Y's worktree, so it is attributed to Y and allowed: %v\n%s", err, out)
		}
	})
	// The anchor's seat is at plan, B's at in_progress: the commit is B's to make.
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "plan", "feature", "in_progress", aFirst, commitInProgressOnly)
		if out, err := runRootIn(t, multiSeatBash("cd "+f.wtB+" && git commit -m x"), "hook", "commitgate"); err != nil {
			t.Fatalf("B is at in_progress and holds the worktree the commit runs in: %v\n%s", err, out)
		}
	})
}

// sty_3a9b06fe AC2: the moved commit is decided by the seat's own state under the
// same rules as any commit — no new check.
func TestMultiSeatMovedCommitDecidedByItsSeatState(t *testing.T) {
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "in_progress", "feature", "plan", aFirst, commitInProgressOnly)
		out, err := runRootIn(t, multiSeatBash("cd "+f.wtB+" && git commit -m x"), "hook", "commitgate")
		if err == nil {
			t.Fatalf("B is at plan, so the commit in B's worktree must be refused though A would allow it:\n%s", out)
		}
		reason := denyReasonOf(t, out)
		if !strings.Contains(reason, "plan") || !strings.Contains(reason, f.b.ID) {
			t.Errorf("the refusal must name B's status and seat: %s", reason)
		}
		if strings.Contains(reason, f.a.ID) {
			t.Errorf("the refusal must not be attributed to A (%s): %s", f.a.ID, reason)
		}
	})
	// Without [gate.command_allow] the engaged seat is all a commit needs.
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "in_progress", "feature", "plan", aFirst, "")
		if out, err := runRootIn(t, multiSeatBash("cd "+f.wtB+" && git commit -m x"), "hook", "commitgate"); err != nil {
			t.Fatalf("no command_allow: an engaged seat may commit whatever its status: %v\n%s", err, out)
		}
	})
}

// sty_3a9b06fe AC3: a commit nothing moved is attributed to the seat in the hook's
// own tree (sessionWorktree), not to the anchor's.
func TestMultiSeatPlainCommitKeepsHookTreeSeat(t *testing.T) {
	bothOrders(t, func(t *testing.T, aFirst bool) {
		// The hook runs in B's tree while the anchor (config root) holds A.
		f := multiSeatRepo(t, "in_progress", "feature", "plan", aFirst, commitInProgressOnly)
		stubWorktree(t, f.wtB)
		out, err := runRootIn(t, multiSeatBash("git commit -m x"), "hook", "commitgate")
		if err == nil {
			t.Fatalf("the hook runs in B's tree and B is at plan, so the commit is refused:\n%s", out)
		}
		if reason := denyReasonOf(t, out); !strings.Contains(reason, f.b.ID) {
			t.Errorf("the refusal must be B's: %s", reason)
		}
	})
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "plan", "feature", "in_progress", aFirst, commitInProgressOnly)
		stubWorktree(t, f.wtB)
		if out, err := runRootIn(t, multiSeatBash("git commit -m x"), "hook", "commitgate"); err != nil {
			t.Fatalf("the hook runs in B's tree and B is at in_progress: %v\n%s", err, out)
		}
	})
}

// sty_3a9b06fe AC3: a command that runs in no seat's tree is refused as before,
// moved or not.
func TestMultiSeatCommitInNoSeatTreeStillRefused(t *testing.T) {
	_, _, _, _, _ = multiSeatTwoElsewhere(t)
	for _, command := range []string{"git commit -m x", "cd " + t.TempDir() + " && git commit -m x"} {
		out, err := runRootIn(t, multiSeatBash(command), "hook", "commitgate")
		if err == nil {
			t.Fatalf("%q runs in no seat's tree and must be refused:\n%s", command, out)
		}
		if reason := denyReasonOf(t, out); !strings.Contains(reason, "none is in the tree this event runs in") {
			t.Errorf("%q: want the existing unattributed refusal, got: %s", command, reason)
		}
	}
}

// sty_3a9b06fe AC4: the foreign-tree fence is untouched. git -C into another
// tree is refused by default; with the fence opted out it is attributed to that
// tree's seat by the same rule as a cd.
func TestMultiSeatGitDashCFenceAndAttribution(t *testing.T) {
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "plan", "feature", "in_progress", aFirst, commitInProgressOnly)
		out, err := runRootIn(t, multiSeatBash("git -C "+f.wtB+" commit -m x"), "hook", "commitgate")
		if err == nil {
			t.Fatalf("git -C into another tree is still refused by containment:\n%s", out)
		}
		if reason := denyReasonOf(t, out); !strings.Contains(reason, "another repo's tree") {
			t.Errorf("want the containment refusal, got: %s", reason)
		}
	})
	const optOut = "allow_outside_tree_edits = true\n" + commitInProgressOnly
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "plan", "feature", "in_progress", aFirst, optOut)
		if out, err := runRootIn(t, multiSeatBash("git -C "+f.wtB+" commit -m x"), "hook", "commitgate"); err != nil {
			t.Fatalf("fence opted out and B (in_progress) holds the tree the commit runs in: %v\n%s", err, out)
		}
	})
	bothOrders(t, func(t *testing.T, aFirst bool) {
		f := multiSeatRepo(t, "in_progress", "feature", "plan", aFirst, optOut)
		out, err := runRootIn(t, multiSeatBash("git -C "+f.wtB+" commit -m x"), "hook", "commitgate")
		if err == nil {
			t.Fatalf("fence opted out, but B is at plan, so the commit in B's tree is refused:\n%s", out)
		}
		if reason := denyReasonOf(t, out); !strings.Contains(reason, f.b.ID) {
			t.Errorf("the refusal must be B's: %s", reason)
		}
	})
}

// sty_68a4d071 — real linked worktrees. The fixtures above fake a tree with a
// .git marker; these run `git worktree add`, so a branch really is checked out in a
// worktree and the hook, running in the main tree under the default fence, has to
// attribute by the tree an event targets.

func resolvedPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// linkedSeats is the main tree (repo, which holds no seat and is where the hook
// runs) and two linked worktrees, wtX on feat/x and wtY on feat/y, each seating one
// story under multiSeatSession.
type linkedSeats struct {
	repo, wtX, wtY string
	x, y           workitem.Item
}

// multiSeatLinked seats X (xStatus, wtX) and Y (yStatus, wtY). xFirst picks which
// seat the store lists first. yInFlight leaves Y's transition unconfirmed.
func multiSeatLinked(t *testing.T, xStatus, yStatus string, xFirst, yInFlight bool, gateExtra string) linkedSeats {
	t.Helper()
	f := multiSeatRepo(t, xStatus, "feature", yStatus, true, gateExtra)
	// multiSeatRepo's fake markers give way to a real repository and real worktrees.
	_ = os.Remove(filepath.Join(f.repo, ".git"))
	if err := os.RemoveAll(f.wtB); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.repo, "init", "-q", "-b", "main")
	gitIn(t, f.repo, "commit", "-q", "--allow-empty", "-m", "init")
	wtX, err := os.MkdirTemp("/var/tmp", "satelle-linked-x-")
	if err != nil {
		t.Skipf("cannot place a linked tree outside the temp roots: %v", err)
	}
	if err := os.Remove(wtX); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wtX) })
	gitIn(t, f.repo, "worktree", "add", "-q", "-b", "feat/x", wtX)
	gitIn(t, f.repo, "worktree", "add", "-q", "-b", "feat/y", f.wtB)
	wtX, wtY := resolvedPath(t, wtX), resolvedPath(t, f.wtB)

	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	type seat struct {
		it     workitem.Item
		status string
		tree   string
		flight bool
	}
	seats := []seat{{f.a, xStatus, wtX, false}, {f.b, yStatus, wtY, yInFlight}}
	if !xFirst {
		seats[0], seats[1] = seats[1], seats[0]
	}
	for _, s := range seats {
		if err := db.Leases.ForceRelease(ctx, s.it.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range seats {
		if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
			ItemID: s.it.ID, Kind: "story", Owner: "alice", State: s.status, StorySeat: true,
			SeatKey: "epic-parent", Worktree: s.tree, SessionID: multiSeatSession,
		}); err != nil {
			t.Fatal(err)
		}
		if !s.flight {
			if err := db.Leases.Confirm(ctx, s.it.ID, s.status); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(5 * time.Millisecond) // time-subject: distinct acquired_at, the store order is the acquisition order
	}
	live, err := db.Leases.List(ctx)
	if err != nil || len(live) != 2 {
		t.Fatalf("fixture must hold two live seats: %v n=%d", err, len(live))
	}
	if first := live[0].ItemID; (first == f.a.ID) != xFirst {
		t.Fatalf("store lists %s first, xFirst=%v", first, xFirst)
	}
	return linkedSeats{repo: f.repo, wtX: wtX, wtY: wtY, x: f.a, y: f.b}
}

const pushInProgressOnly = "\n[gate.command_allow]\ncommit = [\"in_progress\"]\npush = [\"in_progress\"]\n"

// AC1: a commit a cd moved into a linked worktree is judged against its seat.
func TestMultiSeatLinkedCommitJudgedAgainstWorktreeSeat(t *testing.T) {
	bothOrders(t, func(t *testing.T, xFirst bool) {
		l := multiSeatLinked(t, "plan", "in_progress", xFirst, false, pushInProgressOnly)
		if out, err := runRootIn(t, multiSeatBash("cd "+l.wtY+" && git commit -m x"), "hook", "commitgate"); err != nil {
			t.Fatalf("Y is at in_progress and the commit runs in its worktree: %v\n%s", err, out)
		}
		out, err := runRootIn(t, multiSeatBash("cd "+l.wtX+" && git commit -m x"), "hook", "commitgate")
		if err == nil {
			t.Fatalf("X is at plan and the commit runs in its worktree:\n%s", out)
		}
		if reason := denyReasonOf(t, out); !strings.Contains(reason, l.x.ID) {
			t.Errorf("the refusal must be X's: %s", reason)
		}
	})
}

// An explicit cd still beats the branch: a moved push is decided by its directory.
func TestMultiSeatLinkedMovedPushKeepsItsDirectory(t *testing.T) {
	l := multiSeatLinked(t, "plan", "in_progress", true, false, pushInProgressOnly)
	if out, err := runRootIn(t, multiSeatBash("cd "+l.wtY+" && git push origin feat/x"), "hook", "commitgate"); err != nil {
		t.Fatalf("the push runs in Y's worktree, which decides it: %v\n%s", err, out)
	}
}

// AC3: scratch paths outside every repo tree stay ungated while the session holds
// two seats and one has a transition in flight.
func TestMultiSeatLinkedScratchAllowedWhileTransitionInFlight(t *testing.T) {
	bothOrders(t, func(t *testing.T, xFirst bool) {
		l := multiSeatLinked(t, "plan", "in_progress", xFirst, true, "")
		scratch := filepath.Join(t.TempDir(), "scratchpad", "brief.md")
		if out, err := runRootIn(t, multiSeatEdit(scratch), "hook", "gate"); err != nil {
			t.Fatalf("an Edit to scratch %s is allowed: %v\n%s", scratch, err, out)
		}
		write := multiSeatEvent("Write", map[string]any{"file_path": scratch, "content": "x"})
		if out, err := runRootIn(t, write, "hook", "gate"); err != nil {
			t.Fatalf("a Write to scratch is allowed: %v\n%s", err, out)
		}
		redirect := multiSeatBash("echo x > " + scratch)
		if out, err := runRootIn(t, redirect, "hook", "gate"); err != nil {
			t.Fatalf("a shell redirect to scratch is allowed by the gate: %v\n%s", err, out)
		}
		if out, err := runRootIn(t, redirect, "hook", "commitgate"); err != nil {
			t.Fatalf("a shell redirect to scratch is allowed by the commitgate: %v\n%s", err, out)
		}
		// The in-flight seat still fences its own tree.
		if out, err := runRootIn(t, multiSeatEdit(filepath.Join(l.wtY, "a.go")), "hook", "gate"); err == nil {
			t.Fatalf("an edit into the in-flight seat's tree is still refused:\n%s", out)
		}
	})
}

func TestGitPushBranches(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{"git push origin feat/x", []string{"refs/heads/feat/x"}},
		{"git push -u origin feat/x", []string{"refs/heads/feat/x"}},
		{"git push --force-with-lease origin feat/x", []string{"refs/heads/feat/x"}},
		{"git push origin feat/x:release", []string{"refs/heads/feat/x"}},
		{"git push origin +feat/x:feat/x", []string{"refs/heads/feat/x"}},
		{"git push origin refs/heads/feat/x", []string{"refs/heads/feat/x"}},
		{"git push -o ci.skip origin feat/x", []string{"refs/heads/feat/x"}},
		{"git push origin a b", []string{"refs/heads/a", "refs/heads/b"}},
		{"git -C /w push origin feat/x", []string{"refs/heads/feat/x"}},
		{"git push origin feat/x && git push origin feat/y", []string{"refs/heads/feat/x", "refs/heads/feat/y"}},
		{"git push origin feat/x && git push", nil},
		{"git commit -m x && git push origin feat/x", nil},
		{"git status && git push origin feat/x", nil},
		{"git push", nil},
		{"git push origin", nil},
		{"git push origin HEAD", nil},
		{"git push origin HEAD:feat/x", nil},
		{"git push --all origin", nil},
		{"git push --tags origin v1", nil},
		{"git push --mirror origin", nil},
		{"git push origin --delete feat/x", nil},
		{"git push origin :feat/x", nil},
		{"git push origin refs/tags/v1", nil},
		{"git commit -m x", nil},
		{"echo git push origin feat/x", nil},
	} {
		if got := gitPushBranches(tc.command); !slices.Equal(got, tc.want) {
			t.Errorf("gitPushBranches(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}
