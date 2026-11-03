package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// stopcheckSeatState is the seat the fixture arranges before a Stop fires.
type stopcheckSeatState string

const (
	seatNone    stopcheckSeatState = "none"    // performing story, no lease at all
	seatMine    stopcheckSeatState = "mine"    // lease stamped with THIS session's id
	seatOther   stopcheckSeatState = "other"   // lease stamped with a sibling session's id
	seatExpired stopcheckSeatState = "expired" // sibling lease whose heartbeat is past the TTL
)

const (
	thisSession    = "session-B"
	siblingSession = "session-A"
)

// stopcheckRepo scaffolds a git-tracked repo with a wildcard route, an
// in_progress story, and the requested seat, then points the hooks' session
// identity at thisSession. It mirrors liveSeatRepo but stamps the lease with a
// chosen session id, because stopcheck's question is WHICH session holds the
// seat (sty_211d8419).
func stopcheckRepo(t *testing.T, state stopcheckSeatState) (repo, storyID string) {
	t.Helper()
	repo = tempRepo(t)
	t.Chdir(repo)
	t.Setenv(config.SessionEnv, thisSession)
	// The exempt set is configuration; declare it so the exempt-only case
	// exercises the same predicate a real repo does.
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	if err := os.WriteFile(cfgPath, []byte("[review]\ngate_create = false\n\n[gate]\nedit_exempt_paths = [\".satelle/\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "foo.go"), []byte("package internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dirtyGatedPaths asks git; the tree must be a repo with a clean baseline.
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "baseline"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, time.Now().UTC()); err != nil {
		t.Fatalf("doc sync: %v", err)
	}
	sty, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "stopcheck seat scope", Body: "goal",
		AcceptanceCriteria: "1. ok", Status: "in_progress", Category: "chore",
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("create story: %v", err)
	}
	storyID = sty.ID
	if state == seatNone {
		return repo, storyID
	}
	sess := siblingSession
	if state == seatMine {
		sess = thisSession
	}
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: storyID, Kind: "story", Owner: lease.ResolveOwner(), State: "in_progress",
		StorySeat: true, SessionID: sess,
	}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := db.Leases.Confirm(ctx, storyID, "in_progress"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if state == seatExpired {
		if err := db.Leases.SetHeartbeat(ctx, storyID, time.Now().UTC().Add(-2*lease.HeartbeatTTL)); err != nil {
			t.Fatalf("age heartbeat: %v", err)
		}
	}
	return repo, storyID
}

func dirtyTree(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "internal", "foo.go"), []byte("package internal\n\nvar edited = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStopcheckFailOpenPathsUnchanged (AC4): stop_hook_active in both shapes,
// an unresolvable repo, and an exempt-only change still produce a silent allow
// even with a dirty tree and no seat.
func TestStopcheckFailOpenPathsUnchanged(t *testing.T) {
	t.Run("stop_hook_active snake", func(t *testing.T) {
		repo, _ := stopcheckRepo(t, seatNone)
		dirtyTree(t, repo)
		var buf bytes.Buffer
		if err := runHookStopcheck([]byte(`{"stop_hook_active":true}`), &buf); err != nil || buf.Len() != 0 {
			t.Fatalf("out=%q err=%v", buf.String(), err)
		}
	})
	t.Run("stopHookActive camel", func(t *testing.T) {
		repo, _ := stopcheckRepo(t, seatNone)
		dirtyTree(t, repo)
		var buf bytes.Buffer
		if err := runHookStopcheck([]byte(`{"stopHookActive":true}`), &buf); err != nil || buf.Len() != 0 {
			t.Fatalf("out=%q err=%v", buf.String(), err)
		}
	})
	t.Run("exempt-only change", func(t *testing.T) {
		repo, _ := stopcheckRepo(t, seatNone)
		if err := os.WriteFile(filepath.Join(repo, ".satelle", "note.md"), []byte("exempt\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := runHookStopcheck([]byte("{}"), &buf); err != nil || buf.Len() != 0 {
			t.Fatalf("out=%q err=%v", buf.String(), err)
		}
	})
	t.Run("sibling seat, clean tree, silent", func(t *testing.T) {
		stopcheckRepo(t, seatOther)
		var buf bytes.Buffer
		if err := runHookStopcheck([]byte("{}"), &buf); err != nil || buf.Len() != 0 {
			t.Fatalf("a sibling seat with nothing dirty must stay silent: out=%q err=%v", buf.String(), err)
		}
	})
}

// TestStopcheckSiblingNoteShape: the note is pure and names story, session,
// path count and any further live seats without ever reading as a demand.
func TestStopcheckSiblingNoteShape(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	info := seatInfo{ItemID: "sty_abc", State: "in_progress", Owner: "local@host", SessionID: "sess-1", AcquiredAt: now.Add(-10 * time.Minute), HeartbeatAt: now.Add(-time.Minute)}
	note := stopcheckSiblingNote(info, 2, []string{"a.go", "b.go", "c.go"}, now)
	for _, want := range []string{"3 uncommitted", "sty_abc", "sess-1", "+2 more", "not blocked", "satelle story seat"} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q: %s", want, note)
		}
	}
	if strings.Contains(note, "STOP BLOCKED") || strings.Contains(note, "Engage a story now") {
		t.Errorf("the sibling note must not read as a block or a demand: %s", note)
	}
	unstamped := stopcheckSiblingNote(seatInfo{ItemID: "sty_x", State: "plan"}, 0, []string{"a"}, now)
	if !strings.Contains(unstamped, "session unstamped") {
		t.Errorf("unstamped holder should say so: %s", unstamped)
	}
}
