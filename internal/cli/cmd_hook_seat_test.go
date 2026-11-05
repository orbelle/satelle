package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// liveSeatRepo scaffolds a repo with a wildcard workflow, an in_progress story,
// and a confirmed live seat for the current owner (sty_3bb1d8be tests).
func liveSeatRepo(t *testing.T) (repo, storyID string) {
	t.Helper()
	repo = tempRepo(t)
	// Ensure cwd is the repo so foreign-tree fence and relative paths work.
	t.Chdir(repo)

	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
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
	// Non-exempt target for gate tests.
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
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, time.Now().UTC()); err != nil {
		_ = db.Close()
		t.Fatalf("doc sync: %v", err)
	}
	sty, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind:               workitem.KindStory,
		Title:              "seat heartbeat test",
		Body:               "goal",
		AcceptanceCriteria: "1. ok",
		Status:             "in_progress",
		Category:           "chore",
	}, time.Now().UTC())
	if err != nil {
		_ = db.Close()
		t.Fatalf("create story: %v", err)
	}
	storyID = sty.ID
	owner := lease.ResolveOwner()
	if _, _, _, err := db.Leases.Acquire(ctx, storyID, "story", owner, "in_progress", true); err != nil {
		_ = db.Close()
		t.Fatalf("acquire: %v", err)
	}
	if err := db.Leases.Confirm(ctx, storyID, "in_progress"); err != nil {
		_ = db.Close()
		t.Fatalf("confirm: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return repo, storyID
}

func ageLease(t *testing.T, id string, d time.Duration) {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	l, err := db.Leases.Get(ctx, id)
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
	at := l.HeartbeatAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := db.Leases.SetHeartbeat(ctx, id, at.Add(-d)); err != nil {
		t.Fatalf("set heartbeat: %v", err)
	}
}

func heartbeatOf(t *testing.T, id string) time.Time {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	l, err := db.Leases.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
	return l.HeartbeatAt
}

// runRootIn is runRoot with stdin payload (hook handlers read the event from stdin).
// runRootIn runs the command tree with stdin bound to the given text. Flag
// state is reset first: the tree is registered once, so flag variables (and
// their Changed bits) persist across runs in the same process.
func runRootIn(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	resetFlagState(root)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := executeRoot(root)
	return buf.String(), err
}

// forceRelease drops the lease so the story stays performing but the seat is gone.
func forceRelease(t *testing.T, id string) {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Leases.ForceRelease(context.Background(), id); err != nil {
		t.Fatalf("force release: %v", err)
	}
}

func editEvent(path string) string {
	return fmt.Sprintf(`{"tool_input":{"file_path":%q}}`, path)
}

// TestBindSessionIDPublishesInLoopModel (sty_7069bced / epic:model-selection
// order:3): every hook invocation that (re)binds a session identity also
// captures the caller's model as the in-loop tier — known from the payload,
// "unknown" when the payload carries none, never silently skipped.
func TestBindSessionIDPublishesInLoopModel(t *testing.T) {
	// hookHarnessFlag is a shared package var other hook tests set (--harness);
	// pin it like they do so this test's expectation does not depend on suite
	// run order.
	prevHarness := hookHarnessFlag
	hookHarnessFlag = ""
	t.Cleanup(func() { hookHarnessFlag = prevHarness })

	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv(config.SessionEnv, "sess-model-a")
	bindSessionID([]byte(`{"session_id":"sess-model-a","transcript_path":"/home/u/.claude/projects/p/sess-model-a.jsonl","model":"claude-opus-5-5","permission_mode":"default","hook_event_name":"SessionStart"}`))
	model, exe, _ := config.ResolveSessionModel("sess-model-a", verb.SessionModelRoleInLoop)
	if model != "claude-opus-5-5" {
		t.Fatalf("in-loop model = %q, want claude-opus-5-5", model)
	}
	if exe != "claude" {
		t.Fatalf("in-loop executable = %q, want claude (Claude-fingerprinted envelope)", exe)
	}
	// An unrecognised envelope is recorded "unknown", never claude by default.
	t.Setenv(config.SessionEnv, "sess-model-c")
	bindSessionID([]byte(`{"session_id":"sess-model-c","model":"some-model"}`))
	if _, exe, _ := config.ResolveSessionModel("sess-model-c", verb.SessionModelRoleInLoop); exe != "unknown" {
		t.Fatalf("unrecognised envelope executable = %q, want unknown", exe)
	}

	t.Setenv(config.SessionEnv, "sess-model-b")
	bindSessionID([]byte(`{"session_id":"sess-model-b"}`))
	if model, _, _ := config.ResolveSessionModel("sess-model-b", verb.SessionModelRoleInLoop); model != "unknown" {
		t.Fatalf("no resolvable model must publish \"unknown\", got %q", model)
	}
}

func TestHookGateTwoSessionsSameTree(t *testing.T) {
	repo, storyID := liveSeatRepo(t)
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = db.Leases.ForceRelease(ctx, storyID)
	if _, _, _, err := db.Leases.AcquireWith(ctx, lease.AcquireOpts{
		ItemID: storyID, Kind: "story", Owner: lease.ResolveOwner(),
		State: "in_progress", StorySeat: true, SessionID: "sess-A",
		Worktree: repo,
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	out, err := runRootIn(t, `{"session_id":"sess-B","tool_input":{"file_path":"internal/foo.go"}}`, "hook", "gate")
	if err == nil {
		t.Fatalf("sess-B should not inherit sess-A in-flight; got allow:\n%s", out)
	}
	if strings.Contains(out, "the driving session cannot edit") {
		t.Fatalf("sess-B must not be addressed as the driver:\n%s", out)
	}
	if strings.Contains(out, "IN FLIGHT") {
		t.Fatalf("sess-B must not inherit in-flight gating:\n%s", out)
	}
}

func bashEvent(cmd string) string {
	return fmt.Sprintf(`{"tool_input":{"command":%q}}`, cmd)
}

// TestHookHandlersDoNotRefreshStaleSeat (AC2): past HeartbeatTTL, handlers leave
// heartbeat unchanged and the lease remains stealable.
func TestHookHandlersDoNotRefreshStaleSeat(t *testing.T) {
	_, id := liveSeatRepo(t)
	ageLease(t, id, lease.HeartbeatTTL+time.Minute)
	before := heartbeatOf(t, id)
	for _, sub := range []string{"gate", "commitgate", "prompt"} {
		payload := `{}`
		if sub == "gate" {
			payload = editEvent("internal/foo.go")
		} else if sub == "commitgate" {
			payload = bashEvent("git commit -m x")
		}
		_, _ = runRootIn(t, payload, "hook", sub)
		after := heartbeatOf(t, id)
		if !after.Equal(before) {
			t.Errorf("hook %s refreshed a stale seat: before=%v after=%v", sub, before, after)
		}
	}
	// Still stealable by another owner.
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, out, _, err := db.Leases.Acquire(context.Background(), id, "story", "other@host", "in_progress", true)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if out != lease.OutcomeStolen {
		t.Errorf("outcome = %v, want OutcomeStolen", out)
	}
}

// TestHookHandlersDoNotRefreshForeignOwner (AC3).
func TestHookHandlersDoNotRefreshForeignOwner(t *testing.T) {
	repo := tempRepo(t)
	t.Chdir(repo)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	_ = os.MkdirAll(wfDir, 0o755)
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
	_ = os.MkdirAll(filepath.Join(repo, "internal"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "internal", "foo.go"), []byte("package internal\n"), 0o644)

	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	sty, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "foreign", Body: "g", AcceptanceCriteria: "1. ok",
		Status: "in_progress", Category: "chore",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.Leases.Acquire(ctx, sty.ID, "story", "other@host", "in_progress", true); err != nil {
		t.Fatal(err)
	}
	if err := db.Leases.Confirm(ctx, sty.ID, "in_progress"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	ageLease(t, sty.ID, 5*time.Minute)
	before := heartbeatOf(t, sty.ID)
	for _, sub := range []string{"gate", "commitgate", "prompt"} {
		payload := `{}`
		if sub == "gate" {
			payload = editEvent("internal/foo.go")
		} else if sub == "commitgate" {
			payload = bashEvent("git commit -m x")
		}
		_, _ = runRootIn(t, payload, "hook", sub)
		if !heartbeatOf(t, sty.ID).Equal(before) {
			t.Errorf("hook %s refreshed foreign owner lease", sub)
		}
	}
}

// normaliseSeatAges rewrites formatSeat age tokens ("acquired 0s", "heartbeat 1s",
// "stale 30m") so two prompt payloads that differ only by wall-clock age compare equal.
func normaliseSeatAges(s string) string {
	// Single left-to-right pass: replace digit runs that sit after a known label
	// and before a duration unit. Avoid re-scanning already-normalised text.
	var b strings.Builder
	i := 0
	labels := []string{"acquired ", "heartbeat ", "stale "}
	for i < len(s) {
		matched := false
		for _, label := range labels {
			if strings.HasPrefix(s[i:], label) {
				b.WriteString(label)
				i += len(label)
				// consume digits
				j := i
				for j < len(s) && s[j] >= '0' && s[j] <= '9' {
					j++
				}
				if j > i && j < len(s) {
					unit := s[j]
					if unit == 's' || unit == 'm' || unit == 'h' || unit == 'd' {
						b.WriteString("<age>")
						b.WriteByte(unit)
						i = j + 1
						matched = true
						break
					}
				}
				// not an age token — write the raw slice we already advanced past label
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// denyReasonContains reports whether the JSON deny payload's reason contains
// the canonical text (accounting for JSON escaping of quotes and < >).
func denyReasonContains(payload, reason string) bool {
	if strings.Contains(payload, reason) {
		return true
	}
	esc := strings.ReplaceAll(reason, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	esc = strings.ReplaceAll(esc, `<`, `\u003c`)
	esc = strings.ReplaceAll(esc, `>`, `\u003e`)
	if strings.Contains(payload, esc) {
		return true
	}
	// Distinctive stem without punctuation that JSON rewrites.
	stem := reason
	if i := strings.Index(stem, " — "); i > 0 {
		stem = stem[:i]
	}
	if i := strings.Index(stem, ". "); i > 40 {
		stem = stem[:i]
	}
	return strings.Contains(payload, stem) || strings.Contains(payload, strings.ReplaceAll(stem, `"`, `\"`))
}

// TestInitHookScaffoldsRouteToSameHandlers (AC7): Claude and Grok scaffolded
// settings both route PreToolUse/UserPromptSubmit to the same satelle hook
// subcommands (via the shared wrapper script).
func TestInitHookScaffoldsRouteToSameHandlers(t *testing.T) {
	repo := t.TempDir()
	claude := string(buildClaudeHookSettings(repo))
	grok := string(buildGrokHookSettings(repo))

	// PreToolUse Edit → gate; Bash → commitgate; UserPromptSubmit → prompt.
	// Both harnesses use renderHookCommand → "sh <script> <sub> <harness>".
	for _, harnessJSON := range []struct {
		name string
		body string
	}{
		{"claude", claude},
		{"grok", grok},
	} {
		// gate sub appears in an Edit matcher command
		if !strings.Contains(harnessJSON.body, " gate ") && !strings.Contains(harnessJSON.body, " gate\"") {
			// renderHookCommand: "sh <path> gate claude"
			if !strings.Contains(harnessJSON.body, " gate ") {
				// path may end with gate as separate argv: "…/satelle-hook.sh gate claude"
				if !strings.Contains(harnessJSON.body, "satelle-hook.sh gate ") {
					t.Errorf("%s scaffold missing gate subcommand route:\n%s", harnessJSON.name, harnessJSON.body)
				}
			}
		}
		if !strings.Contains(harnessJSON.body, "satelle-hook.sh commitgate ") {
			t.Errorf("%s scaffold missing commitgate subcommand route:\n%s", harnessJSON.name, harnessJSON.body)
		}
		if !strings.Contains(harnessJSON.body, promptHookCommand) {
			t.Errorf("%s scaffold missing promptHookCommand %q", harnessJSON.name, promptHookCommand)
		}
	}
	// Shared wrapper body invokes the same satelle hook <sub> for both harnesses.
	script := parameterizedHookScriptBody()
	for _, sub := range []string{"gate", "commitgate"} {
		if !strings.Contains(script, "hook "+sub) && !strings.Contains(script, `hook "$sub"`) && !strings.Contains(script, "hook $sub") {
			// Script takes sub as argv: satelle hook "$1"
			if !strings.Contains(script, "hook") {
				t.Errorf("wrapper script missing hook invocation for %s", sub)
			}
		}
	}
	// No harness-specific heartbeat command.
	if strings.Contains(claude, "hook heartbeat") || strings.Contains(grok, "hook heartbeat") {
		t.Error("scaffold must not introduce a harness-specific heartbeat path")
	}
}

// liveSeatRepoWithAdvance is liveSeatRepo but the workflow has an intermediate
// forward state (in_progress → integration → done) so AdvanceOptions is non-empty
// and runHookPrompt must emit the engaged form (sty_e16a2cd7).
func liveSeatRepoWithAdvance(t *testing.T) (repo, storyID string) {
	t.Helper()
	repo = tempRepo(t)
	t.Chdir(repo)

	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "integrated", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[integrated]
status = "integration"
agent = "executor"
reviewers = ["ac-rev", "scope-rev"]
reviewer_agent = "reviewer"
requires = ["coded"]

[closed]
status = "done"
terminal = true
requires = ["integrated"]
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
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, time.Now().UTC()); err != nil {
		_ = db.Close()
		t.Fatalf("doc sync: %v", err)
	}
	sty, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind:               workitem.KindStory,
		Title:              "seat advance prompt test",
		Body:               "goal",
		AcceptanceCriteria: "1. ok",
		Status:             "in_progress",
		Category:           "chore",
	}, time.Now().UTC())
	if err != nil {
		_ = db.Close()
		t.Fatalf("create story: %v", err)
	}
	storyID = sty.ID
	owner := lease.ResolveOwner()
	if _, _, _, err := db.Leases.Acquire(ctx, storyID, "story", owner, "in_progress", true); err != nil {
		_ = db.Close()
		t.Fatalf("acquire: %v", err)
	}
	if err := db.Leases.Confirm(ctx, storyID, "in_progress"); err != nil {
		_ = db.Close()
		t.Fatalf("confirm: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return repo, storyID
}
