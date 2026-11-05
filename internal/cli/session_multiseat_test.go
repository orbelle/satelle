package cli

// sty_42231b74 — one session can hold stamped seats in several worktrees (one per
// epic child). An edit, commit or substrate-lock check is attributed by TREE: the
// seat whose worktree holds the edit target, else the seat of the session's own
// tree, else none — never by the order the store lists the seats in. These tests
// run every attribution in both store orders.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

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

const commitInProgressOnly = "\n[gate.command_allow]\ncommit = [\"in_progress\"]\n"

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

const pushInProgressOnly = "\n[gate.command_allow]\ncommit = [\"in_progress\"]\npush = [\"in_progress\"]\n"

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
