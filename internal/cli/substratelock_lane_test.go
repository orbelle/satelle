package cli

// sty_08569de9 — a substrate-lane story may change the substrate it was filed
// for while sibling epic children perform, and the refusal names the condition
// that actually applies. The lock is decided by substrateLane over the seats a
// hook call resolved (resolveSeatsFor → pickSessionSeatFor); these tests drive
// real leases in real linked worktrees through the gate functions.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
	"github.com/spf13/cobra"
)

// laneSpec is one seat of a laneEnv: a story of a category engaged from its own
// linked worktree by a session (stale: its heartbeat is past the TTL).
type laneSpec struct {
	key, category, session string
	stale                  bool
}

// laneEnv is a git repo (the main tree M) with the lock config written and one
// live seat per spec, each engaged from its own linked worktree. The hook's own
// tree is stubbed to none of them, so a session holding several seats has none
// bound to the target.
type laneEnv struct {
	repo  string
	ids   map[string]string // spec key → story id
	trees map[string]string // spec key → linked worktree
}

func newLaneEnv(t *testing.T, specs []laneSpec) *laneEnv {
	t.Helper()
	fenceTempRootsElsewhere(t)
	db, create := attributionRepo(t)
	stubWorktree(t, "/w/none")
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.toml"), []byte(lockExemptToml), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "commit", "--allow-empty", "-qm", "seed")
	e := &laneEnv{repo: repo, ids: map[string]string{}, trees: map[string]string{}}
	ctx := context.Background()
	now := time.Now().UTC()
	for _, s := range specs {
		it := create(s.key, "in_progress")
		cat := s.category
		if _, err := db.Stories.Update(ctx, it.ID, workitem.UpdateInput{Category: &cat}, now); err != nil {
			t.Fatal(err)
		}
		tree := linkedWorktree(t, repo)
		if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
			ItemID: it.ID, Kind: "story", Owner: "alice", State: "in_progress", StorySeat: true,
			SeatKey: "epic-parent", Worktree: tree, SessionID: s.session,
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Leases.Confirm(ctx, it.ID, "in_progress"); err != nil {
			t.Fatal(err)
		}
		e.ids[s.key], e.trees[s.key] = it.ID, tree
	}
	// Aged only once every seat is held: a later acquire under the shared SeatKey
	// would steal a lease that is already stale.
	for _, s := range specs {
		if s.stale {
			if err := db.Leases.SetHeartbeat(ctx, e.ids[s.key], now.Add(-lease.HeartbeatTTL-time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return e
}

// seats is what a hook call resolves for session sid editing target.
func (e *laneEnv) seats(sid, target string) *lockSeats {
	info, engaged, live, err := resolveSeatsFor(false, sid, treeOf(target))
	if err != nil {
		panic(err)
	}
	return &lockSeats{info: info, engaged: engaged, live: live, sid: sid}
}

// gate runs the lock on target through the Edit gate or a Bash redirect. The
// Edit gate is handed s (resolved for the target's tree, as `hook gate` does);
// the Bash gate resolves its own seats per candidate tree through the same
// resolveSeatsFor the production call sites use, for s's session.
func (e *laneEnv) gate(t *testing.T, via, target string, s *lockSeats) (error, string) {
	t.Helper()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	raw := []byte(claudeEditEvent(target))
	if via == "bash" {
		return bashSubstrateLockGate(cmd, raw, "echo x > "+target, loadGateProcess(), func(tree string) (*lockSeats, error) {
			info, engaged, live, err := resolveSeatsFor(false, s.sid, tree)
			return &lockSeats{info: info, engaged: engaged, live: live, sid: s.sid}, err
		}), out.String()
	}
	return substrateLockGate(cmd, raw, target, s), out.String()
}

// rowsByStory counts the ledger rows of a kind per story.
func rowsByStory(t *testing.T, kind string) (map[string]int, []ledger.Entry) {
	t.Helper()
	rows := ledgerRows(t, kind)
	n := map[string]int{}
	for _, r := range rows {
		n[r.StoryID]++
	}
	return n, rows
}

const laneRel = ".satelle/workflows/agents.toml"

// AC1 + AC2: a session holding a substrate seat and non-substrate epic siblings,
// each from its own worktree, may change the main tree's substrate when the edit
// is attributed to none of them; the non-substrate live seats are noted, once per
// hook call, and nothing is refused.
func TestSubstrateLockLaneAmbiguousSessionSiblings(t *testing.T) {
	for _, via := range []string{"edit", "bash"} {
		t.Run(via, func(t *testing.T) {
			e := newLaneEnv(t, []laneSpec{
				{key: "S", category: "substrate", session: "sess"},
				{key: "F1", category: "fix", session: "sess"},
				{key: "F2", category: "fix", session: "sess"},
				{key: "X", category: "feature", session: "other"},
			})
			target := filepath.Join(e.repo, laneRel)
			s := e.seats("sess", target)
			if pick, _ := pickSessionSeatFor(s.live, nil, "sess", treeOf(target)); pick.ItemID != "" || s.engaged || s.info.ItemID != "" {
				t.Fatalf("fixture: the edit must be unresolved for the session, pick=%q info=%q engaged=%v", pick.ItemID, s.info.ItemID, s.engaged)
			}
			if err, out := e.gate(t, via, target, s); err != nil {
				t.Fatalf("a substrate-lane story's change must pass (%s): %v\n%s", via, err, out)
			}
			if n, _ := rowsByStory(t, ledger.KindSubstrateLockDeny); len(n) != 0 {
				t.Errorf("an allowed change records no refusal, got %v", n)
			}
			n, rows := rowsByStory(t, ledger.KindSubstrateChangeNote)
			for _, k := range []string{"F1", "F2", "X"} {
				if n[e.ids[k]] != 1 {
					t.Errorf("%s must be noted exactly once, got %d (%v)", k, n[e.ids[k]], n)
				}
			}
			if n[e.ids["S"]] != 0 || len(rows) != 3 {
				t.Errorf("the substrate story is not noted and nothing else is: %v", n)
			}
			for _, r := range rows {
				var p substrateChangePayload
				if err := json.Unmarshal(r.Payload, &p); err != nil || p.SubstrateStory != e.ids["S"] || p.Path != laneRel {
					t.Errorf("note payload = %s (%v), want substrate story %s and path %s", r.Payload, err, e.ids["S"], laneRel)
				}
			}
		})
	}

	t.Run("one seat resolves", func(t *testing.T) {
		e := newLaneEnv(t, []laneSpec{
			{key: "S", category: "substrate", session: "sess"},
			{key: "F1", category: "fix", session: "other"},
		})
		target := filepath.Join(e.repo, laneRel)
		s := e.seats("sess", target)
		if !s.engaged || s.info.ItemID != e.ids["S"] {
			t.Fatalf("fixture: a session holding one seat resolves to it, got %q engaged=%v", s.info.ItemID, s.engaged)
		}
		if err, out := e.gate(t, "edit", target, s); err != nil {
			t.Fatalf("the single substrate seat must pass: %v\n%s", err, out)
		}
		if n, _ := rowsByStory(t, ledger.KindSubstrateChangeNote); n[e.ids["F1"]] != 1 || len(n) != 1 {
			t.Errorf("only the sibling is noted, once: %v", n)
		}
	})
}

// AC3: an edit attributed to a story of the session that is not substrate is
// refused even though the session also holds a substrate seat.
func TestSubstrateLockAttributedNonSubstrateRefused(t *testing.T) {
	for _, via := range []string{"edit", "bash"} {
		t.Run(via, func(t *testing.T) {
			e := newLaneEnv(t, []laneSpec{
				{key: "S", category: "substrate", session: "sess"},
				{key: "F", category: "fix", session: "sess"},
			})
			target := filepath.Join(e.trees["F"], laneRel)
			s := e.seats("sess", target)
			if !s.engaged || s.info.ItemID != e.ids["F"] {
				t.Fatalf("fixture: the edit must be attributed to F by tree, got %q engaged=%v", s.info.ItemID, s.engaged)
			}
			err, out := e.gate(t, via, target, s)
			if err == nil {
				t.Fatalf("an edit attributed to a non-substrate story must be refused:\n%s", out)
			}
			reason := denyReasonOf(t, out)
			if !strings.Contains(reason, e.ids["F"]) || strings.Contains(reason, e.ids["S"]) {
				t.Errorf("the deny names F and not the substrate seat: %s", reason)
			}
			if n, _ := rowsByStory(t, ledger.KindSubstrateLockDeny); n[e.ids["F"]] != 1 || len(n) != 1 {
				t.Errorf("one refusal, on F: %v", n)
			}
			if n, _ := rowsByStory(t, ledger.KindSubstrateChangeNote); len(n) != 0 {
				t.Errorf("a refusal notes nobody: %v", n)
			}
		})
	}
}

// AC2: change notes record an allowed change only. A Bash command with one
// candidate the lane passes (the main tree, attributed to none of the session's
// seats) and a later one it refuses (F's own worktree) does not run, so it leaves
// no substrate_change_note — only the refusal.
func TestSubstrateLockRefusedCommandLeavesNoChangeNotes(t *testing.T) {
	e := newLaneEnv(t, []laneSpec{
		{key: "S", category: "substrate", session: "sess"},
		{key: "F", category: "fix", session: "sess"},
		{key: "X", category: "feature", session: "other"},
	})
	passes := filepath.Join(e.repo, laneRel)
	refused := filepath.Join(e.trees["F"], laneRel)
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := bashSubstrateLockGate(cmd, []byte(claudeEditEvent(passes)), "echo x > "+passes+" && echo y > "+refused, loadGateProcess(), func(tree string) (*lockSeats, error) {
		info, engaged, live, err := resolveSeatsFor(false, "sess", tree)
		return &lockSeats{info: info, engaged: engaged, live: live, sid: "sess"}, err
	})
	if err == nil {
		t.Fatalf("a command that rewrites F's own worktree substrate must be refused:\n%s", out.String())
	}
	if n, _ := rowsByStory(t, ledger.KindSubstrateChangeNote); len(n) != 0 {
		t.Errorf("a refused command leaves no change note, got %v", n)
	}
	if n, _ := rowsByStory(t, ledger.KindSubstrateLockDeny); n[e.ids["F"]] != 1 || len(n) != 1 {
		t.Errorf("one refusal, on F: %v", n)
	}
}

// substrateLane is pure over the seats and the category answer: a category the
// store could not give is "not substrate", so a doubt refuses.
func TestSubstrateLaneDoubtIsNotTheLane(t *testing.T) {
	s := &lockSeats{
		info: seatInfo{ItemID: "sty_a", SessionID: "sess"}, engaged: true, sid: "sess",
		live: []seatInfo{{ItemID: "sty_a", SessionID: "sess"}, {ItemID: "sty_b", SessionID: "other"}},
	}
	if lane := substrateLane(s, func(string) bool { return false }); lane.Lane != nil || lane.Why != laneOwnNotSubstrate {
		t.Errorf("an unreadable category is not the lane: %+v", lane)
	}
	lane := substrateLane(s, func(id string) bool { return id == "sty_a" })
	if lane.Lane == nil || lane.Lane.ItemID != "sty_a" || len(lane.Noted) != 1 || lane.Noted[0].ItemID != "sty_b" {
		t.Errorf("the attributed substrate seat is the lane and its sibling is noted: %+v", lane)
	}
}
