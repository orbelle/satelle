package agentstep

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestInvokeScratchEnv covers AC1's one-shot path: every Invoke — reviewer
// (ExpectVerdict) and performer (ExpectPerform) alike — overlays a scratch dir
// under os.TempDir(), 0700, with TMPDIR == SATELLE_SCRATCH, and two
// invocations never share a dir.
func TestInvokeScratchEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("0700 mode assertion is POSIX-specific")
	}
	r := &fakeRunner{out: `{"decision":"accept","notes":"ok"}`}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	g.SetReviewerBinding(config.AgentBinding{Command: "claude", Tools: "Read", Principles: config.PrinciplesNone})

	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: g.reviewerBinding, Section: "reviewer", Rubric: "judge",
		Payload: map[string]string{}, Expect: ExpectVerdict, Runner: r, Skill: "s",
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	dir1 := r.got.Env["TMPDIR"]
	if dir1 == "" || dir1 != r.got.Env[config.ScratchEnv] {
		t.Fatalf("reviewer env TMPDIR/SATELLE_SCRATCH = %q/%q, want equal and non-empty", dir1, r.got.Env[config.ScratchEnv])
	}
	if !strings.HasPrefix(dir1, filepath.Join(os.TempDir(), "satelle")) {
		t.Errorf("scratch dir %q not under os.TempDir()/satelle", dir1)
	}
	// The dir was removed on success — assert the mode was right AT THE TIME it
	// existed by re-deriving a fresh one instead (finishScratch already ran).
	if _, err := os.Stat(dir1); !os.IsNotExist(err) {
		t.Errorf("scratch dir must be removed after a successful reviewer run, got err=%v", err)
	}

	r2 := &fakeRunner{out: "did the work"}
	res2 := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Tools: "Read", Principles: config.PrinciplesNone},
		Section: "planner", Rubric: "do it", Payload: map[string]string{},
		Charter: executorCharter("planner", "plan", "wf"), Expect: ExpectPerform, Runner: r2,
	})
	if res2.Err != nil {
		t.Fatal(res2.Err)
	}
	dir2 := r2.got.Env["TMPDIR"]
	if dir2 == "" || dir2 != r2.got.Env[config.ScratchEnv] {
		t.Fatalf("performer env TMPDIR/SATELLE_SCRATCH = %q/%q, want equal and non-empty", dir2, r2.got.Env[config.ScratchEnv])
	}
	if dir1 == dir2 {
		t.Errorf("two invocations shared a scratch dir: %q", dir1)
	}
}

// TestInvokeScratchDirMode0700 asserts the created directory's mode directly,
// by hooking a runner that stats SATELLE_SCRATCH while it still exists.
func TestInvokeScratchDirMode0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("0700 mode assertion is POSIX-specific")
	}
	var gotMode os.FileMode
	r := &statRunner{fn: func(req agentcli.Request) ([]byte, error) {
		info, err := os.Stat(req.Env[config.ScratchEnv])
		if err != nil {
			return nil, err
		}
		gotMode = info.Mode().Perm()
		return []byte("ok"), nil
	}}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Tools: "Read", Principles: config.PrinciplesNone},
		Section: "planner", Payload: map[string]string{}, Expect: ExpectPerform, Runner: r,
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if gotMode != 0o700 {
		t.Errorf("scratch dir mode = %v, want 0700", gotMode)
	}
}

type statRunner struct {
	fn func(agentcli.Request) ([]byte, error)
}

func (s *statRunner) Name() string    { return "stat" }
func (s *statRunner) Command() string { return "stat" }
func (s *statRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	return s.fn(req)
}

// TestCharterNamesScratch (AC2): the generated charter every binding receives
// names the exact scratch dir path and says never in the repository tree — no
// skill or principle file carries this instruction.
func TestCharterNamesScratch(t *testing.T) {
	g := New(&fakeRunner{}, fakeDocs{workflow: testWorkflow}, "/repo", "")
	for _, inv := range []invocation{
		{charter: reviewerCharter(), scratch: "/tmp/satelle/x/y/z"},
		{charter: executorCharter("coder", "in_progress", "default"), scratch: "/tmp/satelle/x/y/z"},
		{charter: consultCharter("reviewer-consult", "reviewer", "in_progress"), scratch: "/tmp/satelle/x/y/z"},
	} {
		req, err := g.buildRequest(context.Background(), inv)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(req.SystemPrompt, "/tmp/satelle/x/y/z") {
			t.Errorf("charter missing scratch path:\n%s", req.SystemPrompt)
		}
		if !strings.Contains(req.SystemPrompt, "never in the repository tree") {
			t.Errorf("charter missing the never-in-tree instruction:\n%s", req.SystemPrompt)
		}
	}
}

// TestScratchKeptOnFailure (AC3): a failed one-shot dispatch keeps its
// scratch dir and ledgers scratch_kept with the path.
func TestScratchKeptOnFailure(t *testing.T) {
	r := &fakeRunner{err: errors.New("boom")}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	var rows []map[string]any
	g.SetInvocationRecorder(func(_ context.Context, itemID string, payload map[string]any) error {
		rows = append(rows, payload)
		return nil
	})
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Tools: "Read", Principles: config.PrinciplesNone},
		Section: "planner", Payload: map[string]string{}, Expect: ExpectPerform, Runner: r, StoryID: "sty_1",
	})
	if res.Err == nil {
		t.Fatal("want an error")
	}
	dir := r.got.Env[config.ScratchEnv]
	if dir == "" {
		t.Fatal("no scratch dir recorded on the request")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("scratch dir must survive a failed dispatch: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	var kept bool
	for _, row := range rows {
		if row["phase"] == "scratch_kept" && row["scratch_dir"] == dir {
			kept = true
		}
	}
	if !kept {
		t.Errorf("no scratch_kept ledger row for %q: %#v", dir, rows)
	}
}

// TestOpenSessionScratchEnv_Chat and TestOpenSessionScratchEnv_Consult (AC1
// live path): a live session — driving or consult role — opens with the same
// TMPDIR/SATELLE_SCRATCH overlay a one-shot dispatch gets.
func TestOpenSessionScratchEnv_Chat(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "orchestrator" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Interface: "stream", Tools: "Read", Command: "claude -p {tools}"}, true
	})
	var got agentcli.Request
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			got = req
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAsWithModel(context.Background(), "orchestrator", SessionRoleDriving,
		workitem.Item{ID: "sty_live", Status: "in_progress"}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if got.Env["TMPDIR"] == "" || got.Env["TMPDIR"] != got.Env[config.ScratchEnv] {
		t.Fatalf("chat session env = %#v", got.Env)
	}
}

func TestOpenSessionScratchEnv_Consult(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "reviewer-consult" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Role: "reviewer", Interface: "stream", Tools: "Read", Command: "claude -p {tools}"}, true
	})
	var got agentcli.Request
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			got = req
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAsWithModel(context.Background(), "reviewer-consult", SessionRoleConsult,
		workitem.Item{ID: "sty_live", Status: "in_progress"}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if got.Env["TMPDIR"] == "" || got.Env["TMPDIR"] != got.Env[config.ScratchEnv] {
		t.Fatalf("consult session env = %#v", got.Env)
	}
}

// closingErrSess is a live session fake whose Close() fails, for AC3's live twin.
type closingErrSess struct{ closedSess }

func (closingErrSess) Close() error { return errors.New("close boom") }

// TestLiveSessionScratchKeptOnCloseError (AC3 live twin): a session that
// errors on Close keeps its scratch dir and ledgers scratch_kept.
func TestLiveSessionScratchKeptOnCloseError(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		return config.AgentBinding{Interface: "stream", Tools: "Read", Command: "claude -p {tools}"}, true
	})
	var got agentcli.Request
	var rows []map[string]any
	g.SetInvocationRecorder(func(_ context.Context, _ string, payload map[string]any) error {
		rows = append(rows, payload)
		return nil
	})
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			got = req
			return closingErrSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAsWithModel(context.Background(), "orchestrator", SessionRoleDriving,
		workitem.Item{ID: "sty_live", Status: "in_progress"}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err == nil {
		t.Fatal("want the Close error surfaced")
	}
	dir := got.Env[config.ScratchEnv]
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("scratch dir must survive a Close error: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	var kept bool
	for _, row := range rows {
		if row["phase"] == "scratch_kept" && row["scratch_dir"] == dir {
			kept = true
		}
	}
	if !kept {
		t.Errorf("no scratch_kept ledger row: %#v", rows)
	}
}

// --- Leftover sweep (AC5/AC6) ------------------------------------------------

// newGitRepo creates a real git repo at t.TempDir() with one committed file,
// so untrackedSnapshot / SweepLeftovers have real `git ls-files` semantics.
func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.go")
	run("commit", "-q", "-m", "init")
	return dir
}

var basicLeftoverRule = config.LeftoverRule{
	Patterns:     []string{".ac-evidence*"},
	ContentRegex: `^\s*package \w+\s*$`,
	MaxBytes:     64,
	Action:       "move",
}

// TestConsultSessionCloseDoesNotSweep (AC5 live path): a read-only consult
// session leaves a matching file untouched — it never edits, so it is never
// treated as the source of leftovers.
func TestConsultSessionCloseDoesNotSweep(t *testing.T) {
	repo := newGitRepo(t)
	g := New(&fakeRunner{}, fakeDocs{}, repo, "")
	g.SetLeftoverRule(basicLeftoverRule)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		return config.AgentBinding{Role: "reviewer", Interface: "stream", Tools: "Read", Command: "claude -p {tools}"}, true
	})
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAsWithModel(context.Background(), "reviewer-consult", SessionRoleConsult,
		workitem.Item{ID: "sty_live", Status: "in_progress"}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".ac-evidence-tmp.md"), []byte("debris\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".ac-evidence-tmp.md")); err != nil {
		t.Errorf("a consult session must never sweep: %v", err)
	}
}
