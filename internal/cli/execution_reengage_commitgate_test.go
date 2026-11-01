package cli

// sty_7f93ae45 — a task run re-engages its own execution with the command the
// edit gate's dropped-seat advice names, and the commit/push gate then attributes
// a push from that tree to the execution, never to a story the same run seated in
// the same tree earlier.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
)

const execRouteDone = `["*"]
obligations = ["raised", "planned", "coded", "closed"]
park = { state = "blocked" }

[execution]
obligations = ["raised", "run", "run-closed"]
`

const execRouteStep = `[raised]
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

[run]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[run-closed]
status = "done"
terminal = true
requires = ["run"]
`

const execRunSession = "release-run"

func execRunBash(command string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": execRunSession, "transcript_path": "/x/transcript.jsonl",
		"tool_name": "Bash", "tool_input": map[string]any{"command": command},
	})
	return string(b)
}

// TestReengagedExecutionPushAttributedToItOverASameTreeStory (AC1/AC2): the run
// seats a story in the main tree (at plan), then its execution — whose seat was
// dropped — is re-engaged with `story set <exe> --status in_progress` from the
// same tree and session. The push is admitted and attributed to the execution:
// push is allowed only at in_progress, so attributing it to the story (at plan)
// would refuse it.
func TestReengagedExecutionPushAttributedToItOverASameTreeStory(t *testing.T) {
	repo := tempRepo(t)
	t.Chdir(repo)
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"), execRouteDone, execRouteStep)
	writeFile(t, repo, ".satelle/satelle.toml",
		"[review]\ngate_create = false\n\n[gate.command_allow]\npush = [\"in_progress\"]\n")
	gitInitCommit(t, repo)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	t.Setenv(config.SessionEnv, execRunSession)
	stubWorktree(t, repo)

	// The run's execution, moved into its performing state, then its seat dropped.
	out, err := runRoot(t, "task", "create", "--title", "release")
	if err != nil {
		t.Fatalf("task create: %v\n%s", err, out)
	}
	task := idOf(t, out)
	out, err = runRoot(t, "execution", "create", "--title", "release run", "--parent", task)
	if err != nil {
		t.Fatalf("execution create: %v\n%s", err, out)
	}
	exe := idOf(t, out)
	if out, err := runRoot(t, "execution", "set", exe, "--status", "in_progress"); err != nil {
		t.Fatalf("execution engage: %v\n%s", err, out)
	}
	forceRelease(t, exe)

	// The run then moves a story into a performing state from the same tree.
	out, err = runRoot(t, "story", "create", "--title", "released story", "--body", "goal",
		"--acceptance", "1. ok", "--category", "feature")
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	story := idOf(t, out)
	if out, err := runRoot(t, "story", "set", story, "--status", "plan"); err != nil {
		t.Fatalf("story engage: %v\n%s", err, out)
	}
	reseatUnderEpicKey(t, story, "plan", repo)

	// With only the story seated, the push is the story's, and it is at plan.
	if out, err := runRootIn(t, execRunBash("git push origin main"), "hook", "commitgate"); err == nil {
		t.Fatalf("control: with only the plan story seated the push must be refused:\n%s", out)
	}

	time.Sleep(5 * time.Millisecond) // time-subject: the re-engage is acquired after the story's seat
	if out, err := runRoot(t, "story", "set", exe, "--status", "in_progress"); err != nil {
		t.Fatalf("re-engage the execution: %v\n%s", err, out)
	}
	seats, err := runRoot(t, "story", "seat")
	if err != nil || !strings.Contains(seats, exe) {
		t.Fatalf("satelle story seat must list %s after the re-engage: %v\n%s", exe, err, seats)
	}

	info, engaged, live, err := resolveSeatsFor(false, execRunSession, repo)
	if len(live) != 2 || live[0].ItemID != story {
		t.Fatalf("fixture: the store must list the story's seat first, got %+v", live)
	}
	if err != nil || !engaged || info.ItemID != exe {
		t.Fatalf("push from the main tree attributed to %q (engaged=%v err=%v), want %s, not %s", info.ItemID, engaged, err, exe, story)
	}
	if out, err := runRootIn(t, execRunBash("git push origin main"), "hook", "commitgate"); err != nil {
		t.Fatalf("the re-engaged execution is at in_progress, so the push must be admitted: %v\n%s", err, out)
	}
}

// reseatUnderEpicKey re-acquires id's seat, same session and tree, under an
// epic-shaped seat key. The store lists leases by seat key, and "epic-…" sorts
// ahead of the execution's own "exe_…" key, so a resolver that took the first
// same-tree seat would pick the story.
func reseatUnderEpicKey(t *testing.T, id, state, tree string) {
	t.Helper()
	forceRelease(t, id)
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: id, Kind: "story", Owner: lease.ResolveOwner(), State: state, StorySeat: true,
		SeatKey: "epic-parent", Worktree: tree, SessionID: execRunSession,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Leases.Confirm(ctx, id, state); err != nil {
		t.Fatal(err)
	}
}

// AC3: the dropped-seat advice names `satelle story set <id> --status <s>` for an
// execution too — the command TestReengagedExecutionPushAttributedToItOverASameTreeStory
// proves seats it.
func TestDroppedSeatAdviceNamesStorySetForAnExecution(t *testing.T) {
	reason := droppedSeatEditReason("exe_695726be", "in_progress")
	if !strings.Contains(reason, "satelle story set exe_695726be --status in_progress") {
		t.Fatalf("advice must name the re-engage command for the execution: %s", reason)
	}
}

// AC2: one session holding two seats in one tree attributes to the one acquired
// last; an exact tie on AcquiredAt is no pick, whatever the store order.
func TestPickSessionSeatForSameTreeLatestAcquired(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	story := seatInfo{ItemID: "sty_x", SessionID: "sess", Worktree: "/w/main", AcquiredAt: t0}
	exe := seatInfo{ItemID: "exe_y", SessionID: "sess", Worktree: "/w/main", AcquiredAt: t0.Add(time.Second)}
	stubWorktree(t, "/w/main")
	for _, live := range [][]seatInfo{{story, exe}, {exe, story}} {
		for _, target := range []string{"/w/main", ""} {
			if got, mine := pickSessionSeatFor(live, nil, "sess", target); got.ItemID != "exe_y" || !mine {
				t.Errorf("target %q: got %q mine=%v, want the later-acquired exe_y", target, got.ItemID, mine)
			}
		}
	}
	tie := exe
	tie.AcquiredAt = t0
	for _, live := range [][]seatInfo{{story, tie}, {tie, story}} {
		if got, _ := pickSessionSeatFor(live, nil, "sess", "/w/main"); got.ItemID != "" {
			t.Errorf("an exact AcquiredAt tie must pick none, got %q", got.ItemID)
		}
	}
}
