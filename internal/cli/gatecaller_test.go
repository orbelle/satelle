package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gatehandle"
	"github.com/bobmcallan/satelle/internal/testutil"
	"github.com/bobmcallan/satelle/internal/verb"
)

// The hand-off is tested against a test-only verb that declares
// DispatchesReviewer and can be held running: it is the same seam every real reviewer-running
// verb goes through (dispatch → handOffGate), without needing a live reviewer. The
// real verbs are exercised against a slow stub reviewer in tests/gate_handoff_test.go.

const gateTestVerb = "gatetest-run"

// gateTestAdvisory stands for the step-self-report note a real `story set`
// writes to stderr; the record it prints to stdout carries `"say"`.
const gateTestAdvisory = "note: log a step-self-report for the step just left"

// assertVerdictOnly fails if block carries anything but the verdict: the
// command's printed record or its advisory stderr note.
func assertVerdictOnly(t *testing.T, what, block, verdict string) {
	t.Helper()
	if !strings.Contains(block, verdict) {
		t.Errorf("%s lacks the verdict %q:\n%s", what, verdict, block)
	}
	if strings.Contains(block, `"say"`) || strings.Contains(block, `"ok"`) {
		t.Errorf("%s carries the record the command printed, not just the verdict:\n%s", what, block)
	}
	if strings.Contains(block, gateTestAdvisory) {
		t.Errorf("%s carries the command's advisory stderr note:\n%s", what, block)
	}
}

type gateTestReq struct {
	// Hold, when set, keeps the gate running until that file exists. A test that
	// needs "the gate is still running" asks for it explicitly instead of sleeping
	// for a duration it hopes outlasts the machine.
	Hold string `json:"hold"`
	Fail bool   `json:"fail"`
	Say  string `json:"say"`
}

// holdGate returns the gatetest arguments that keep the gate running until
// release is called. release also runs on cleanup, so a gate left running by a
// failed test (its child is a detached process) is let go at once; useGateHandOff's
// drain then stops that child before the test's temp dirs are removed.
func holdGate(t *testing.T) (args []string, release func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release-gate")
	var once sync.Once
	release = func() { once.Do(func() { _ = os.WriteFile(path, nil, 0o644) }) }
	t.Cleanup(release)
	return []string{"--hold", path}, release
}

func init() {
	verb.Register(&verb.Verb{
		Name:               gateTestVerb,
		Description:        "test-only: a verb that dispatches a slow reviewer",
		DispatchesReviewer: func(json.RawMessage) bool { return true },
		Invoke: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var r gateTestReq
			_ = json.Unmarshal(raw, &r)
			if r.Hold != "" {
				// The bound is a safety net so a gate whose test is gone ends on its own.
				deadline := time.Now().Add(2 * testutil.WaitBudget)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(r.Hold); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond) // poll tick
				}
			}
			// What the real transition prints: the verdict through the verdict sink,
			// and an advisory note on stderr that a delivery must leave out.
			verb.EmitVerdict("accepted: " + r.Say)
			fmt.Fprintln(os.Stderr, gateTestAdvisory)
			if r.Fail {
				return nil, fmt.Errorf("rejected: %s", r.Say)
			}
			b, _ := json.Marshal(map[string]any{"ok": true, "say": r.Say})
			return b, nil
		},
	})
	c := &cobra.Command{
		Use: "gatetest", Hidden: true, Short: "test only",
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			hold, _ := cmd.Flags().GetString("hold")
			fail, _ := cmd.Flags().GetBool("fail")
			say, _ := cmd.Flags().GetString("say")
			return dispatch(cmd, gateTestVerb, map[string]any{"hold": hold, "fail": fail, "say": say})
		},
	}
	c.Flags().String("hold", "", "")
	c.Flags().Bool("fail", false, "")
	c.Flags().String("say", "", "")
	register(c)
}

// clearHarnessEnv removes every marker that makes this process look like it runs
// inside a harness, so caller-mode tests see a bare shell whatever runs them.
// That includes the dispatch markers: a test run from a dispatched session would
// otherwise look dispatched to every in-loop hook path.
func clearHarnessEnv(t *testing.T) {
	t.Helper()
	markers := agentcli.SessionMarkerEnvNames()
	for _, e := range os.Environ() {
		k, v, _ := strings.Cut(e, "=")
		match := k == config.ScratchEnv || k == config.SessionEnv ||
			k == config.DispatchAgentEnv || k == config.DispatchStepEnv ||
			k == config.DispatchItemEnv || k == config.SpawnEnv
		for _, m := range markers {
			if k == m || strings.HasPrefix(k, m) {
				match = true
				break
			}
		}
		if match {
			key, val := k, v
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, val) })
		}
	}
}

// useGateHandOff turns the hand-off on for a test: agent mode, the detached copy
// is this test binary re-executed as the CLI, and the bounded wait is `wait`.
func useGateHandOff(t *testing.T, wait string, args []string) {
	t.Helper()
	clearHarnessEnv(t)
	t.Setenv(gateModeEnv, string(gateModeAgent))
	t.Setenv(gateWaitEnv, wait)
	oldArgv, oldChild := gateArgv, gateChildCommand
	gateArgv = func() []string { return args }
	gateChildCommand = func(argv []string) (*exec.Cmd, error) {
		c := exec.Command(os.Args[0], argv...)
		c.Env = append(os.Environ(), "SATELLE_CLI_REEXEC=1")
		gateChildren.record(t, c)
		return c, nil
	}
	t.Cleanup(func() { gateArgv, gateChildCommand = oldArgv, oldChild })
	t.Cleanup(func() { drainGateChildren(t) })
}

// gateChildren records the detached gate children each test started, so cleanup
// can stop them. A child outlives the call that handed off — that is the
// hand-off — so without this it wakes when holdGate's release runs on cleanup and
// writes its verdict, result and output into the runtime gates/<id> directory
// while the t.TempDir RemoveAll registered earlier is deleting it ("unlinkat …
// gates/gw_…: directory not empty").
var gateChildren childLog

type childLog struct {
	mu   sync.Mutex
	cmds map[*testing.T][]*exec.Cmd
}

func (l *childLog) record(t *testing.T, c *exec.Cmd) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cmds == nil {
		l.cmds = map[*testing.T][]*exec.Cmd{}
	}
	l.cmds[t] = append(l.cmds[t], c)
}

func (l *childLog) take(t *testing.T) []*exec.Cmd {
	l.mu.Lock()
	defer l.mu.Unlock()
	cmds := l.cmds[t]
	delete(l.cmds, t)
	return cmds
}

// drainGateChildren kills every gate child this test started and waits until
// each is gone, so nothing writes into the test's tree once it returns. A killed
// child writes nothing more, so the drain does not depend on whether the test
// released its hold first. The production goroutine in startGateChild owns Wait
// and the exit status; the drain only kills and probes the pid, which is why a
// second Wait (a race under -race) is never made here.
func drainGateChildren(t *testing.T) {
	t.Helper()
	for _, c := range gateChildren.take(t) {
		if c.Process == nil {
			continue // never started
		}
		pid := c.Process.Pid
		_ = c.Process.Kill() // an already-exited child answers with a harmless error
		deadline := time.Now().Add(testutil.WaitBudget)
		for {
			live := gatehandle.ProbePID(pid)
			if live == gatehandle.Gone {
				break
			}
			if live == gatehandle.Unknown {
				t.Logf("gate child %d: exit cannot be verified on this platform", pid)
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("gate child %d still running %s after being killed", pid, testutil.WaitBudget)
				break
			}
			time.Sleep(5 * time.Millisecond) // poll tick
		}
	}
}

func gateStoreForTest(t *testing.T) *gatehandle.Store {
	t.Helper()
	s, ok := hookGateStore()
	if !ok {
		t.Fatal("no gate store resolvable from the temp repo")
	}
	return s
}

// --- caller mode ----------------------------------------------------------

func TestGateCallerMode(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(gateModeEnv, "")
	old := stderrIsTerminal
	t.Cleanup(func() { stderrIsTerminal = old })

	stderrIsTerminal = func() bool { return true }
	if got := gateCallerMode(config.Config{}, t.TempDir()); got != gateModeInteractive {
		t.Errorf("a person at a terminal = %s, want interactive", got)
	}
	stderrIsTerminal = func() bool { return false }
	if got := gateCallerMode(config.Config{}, t.TempDir()); got != gateModeAgent {
		t.Errorf("no terminal = %s, want agent", got)
	}
	stderrIsTerminal = func() bool { return true }
	t.Setenv("GROK_AGENT", "1")
	if got := gateCallerMode(config.Config{}, t.TempDir()); got != gateModeAgent {
		t.Errorf("a harness-identified session at a terminal = %s, want agent", got)
	}
	// The override wins both ways.
	t.Setenv(gateModeEnv, "interactive")
	stderrIsTerminal = func() bool { return false }
	if got := gateCallerMode(config.Config{}, t.TempDir()); got != gateModeInteractive {
		t.Errorf("SATELLE_GATE_MODE=interactive = %s", got)
	}
	_ = os.Unsetenv("GROK_AGENT")
	t.Setenv(gateModeEnv, "agent")
	stderrIsTerminal = func() bool { return true }
	if got := gateCallerMode(config.Config{}, t.TempDir()); got != gateModeAgent {
		t.Errorf("SATELLE_GATE_MODE=agent = %s", got)
	}
}

// TestGateCallerMode_RepoHandoffChoice (sty_8ee31f26 AC1, AC3): the repo's
// [gate] handoff decides whether an agent-facing caller hands off, and
// SATELLE_GATE_MODE overrides it either way. Under auto the adapter's facts
// decide: a grok scaffold (15s cutoff) hands off, a claude-only repo (120s) does
// not.
func TestGateCallerMode_RepoHandoffChoice(t *testing.T) {
	old := stderrIsTerminal
	t.Cleanup(func() { stderrIsTerminal = old })

	cases := []struct {
		name     string
		handoff  string
		env      string
		scaffold string // harness scaffold dir the repo carries
		terminal bool
		want     gateMode
	}{
		{"off: no terminal is still foreground", config.GateHandoffOff, "", ".claude", false, gateModeInteractive},
		{"off: grok scaffold is still foreground", config.GateHandoffOff, "", ".grok", false, gateModeInteractive},
		{"on: claude-only, no terminal hands off", config.GateHandoffOn, "", ".claude", false, gateModeAgent},
		{"on: a person at a terminal stays interactive", config.GateHandoffOn, "", ".claude", true, gateModeInteractive},
		{"auto: grok in play hands off", config.GateHandoffAuto, "", ".grok", false, gateModeAgent},
		{"auto: claude only stays foreground", config.GateHandoffAuto, "", ".claude", false, gateModeInteractive},
		{"default (unset) is auto: claude only stays foreground", "", "", ".claude", false, gateModeInteractive},
		{"auto: a terminal stays interactive even with grok", config.GateHandoffAuto, "", ".grok", true, gateModeInteractive},
		{"env interactive beats on", config.GateHandoffOn, "interactive", ".grok", false, gateModeInteractive},
		{"env agent beats off", config.GateHandoffOff, "agent", ".claude", true, gateModeAgent},
		{"env agent beats auto with claude only", config.GateHandoffAuto, "agent", ".claude", false, gateModeAgent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearHarnessEnv(t)
			t.Setenv(gateModeEnv, tc.env)
			repo := t.TempDir()
			if err := os.MkdirAll(repo+"/"+tc.scaffold, 0o755); err != nil {
				t.Fatal(err)
			}
			stderrIsTerminal = func() bool { return tc.terminal }
			cfg := config.Config{Gate: config.GateConfig{Handoff: tc.handoff}}
			if got := gateCallerMode(cfg, repo); got != tc.want {
				t.Errorf("gateCallerMode(handoff=%q env=%q scaffold=%s terminal=%v) = %s, want %s",
					tc.handoff, tc.env, tc.scaffold, tc.terminal, got, tc.want)
			}
		})
	}
}

// A grok session started from a Claude Code shell inherits CLAUDECODE=1. The
// pending line must quote grok's limitation, not claude's wake — the session's
// own published in-loop row decides, the environment is the fallback.
func TestDrivingHarness_PublishedRowBeatsInheritedMarkers(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv("CLAUDECODE", "1")
	if got := drivingHarness(); got != agentcli.HarnessClaude {
		t.Fatalf("with only claude's marker the fallback is %q, want claude", got)
	}
	t.Setenv(config.SessionEnv, "sess-nested")
	config.PublishSessionModel("sess-nested", verb.SessionModelRoleInLoop, "unknown", agentcli.HarnessGrok, "")
	if got := drivingHarness(); got != agentcli.HarnessGrok {
		t.Fatalf("a grok session's published row must beat the inherited claude marker, got %q", got)
	}
}

// AC1: the bound is derived from the repo's configured harnesses; grok among
// them keeps it under grok's 15s cutoff. This repo scaffolds both.
func TestGateWaitBound_FollowsConfiguredHarnesses(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(gateWaitEnv, "")
	repo := t.TempDir()
	for _, d := range []string{".claude", ".grok"} {
		if err := os.MkdirAll(repo+"/"+d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := gateWaitBound(repo); got >= 15*time.Second {
		t.Errorf("a claude+grok repo waits %s, want under grok's 15s cutoff", got)
	}
	claudeOnly := t.TempDir()
	_ = os.MkdirAll(claudeOnly+"/.claude", 0o755)
	if got := gateWaitBound(claudeOnly); got <= 15*time.Second {
		t.Errorf("a claude-only repo waits %s, want the longer claude bound", got)
	}
	// An in-loop grok session bounds a repo that scaffolds only claude.
	t.Setenv("GROK_AGENT", "1")
	if got := gateWaitBound(claudeOnly); got >= 15*time.Second {
		t.Errorf("a grok driver in a claude-scaffolded repo waits %s, want under 15s", got)
	}
}

// --- the hand-off, end to end --------------------------------------------

// AC1: a gate that finishes inside the bound returns its verdict block inline,
// exactly as it would have, with nothing left for a hook to deliver.
func TestHandOff_FastGateReturnsVerdictInline(t *testing.T) {
	_ = tempRepo(t)
	args := []string{"gatetest", "--say", "fast"}
	useGateHandOff(t, "20s", args)

	start := time.Now()
	out, err := runRoot(t, args...)
	if err != nil && !errors.Is(err, errGateHandedOff) {
		t.Fatalf("a finished accept must return cleanly: %v\n%s", err, out)
	}
	assertVerdictOnly(t, "the inline return", out, "accepted: fast")
	if strings.Contains(out, `"gate":"pending"`) {
		t.Fatalf("a fast gate must not report pending:\n%s", out)
	}
	store := gateStoreForTest(t)
	if left := store.Undelivered(); len(left) != 0 {
		t.Fatalf("an inline replay must mark the run delivered, left: %v", left)
	}
	// It really went through a detached run: exactly one handle, replayed.
	entries, _ := os.ReadDir(store.Dir())
	if len(entries) != 1 || !store.Delivered(entries[0].Name()) {
		t.Fatalf("expected one handle, delivered by the inline replay; got %v", entries)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("a fast gate took %s", time.Since(start))
	}
}

// AC1/AC2: a gate that outlasts the bound returns a handle inside it — with no
// progress — and its verdict is then delivered exactly once, by the hook side.
func TestHandOff_SlowGateReturnsHandleThenDeliversOnce(t *testing.T) {
	_ = tempRepo(t)
	hold, release := holdGate(t)
	args := append([]string{"gatetest", "--say", "slow"}, hold...)
	useGateHandOff(t, "300ms", args) // time-subject: the hand-off bound is what the test crosses

	start := time.Now()
	out, err := runRoot(t, args...)
	took := time.Since(start)
	if err != nil && !errors.Is(err, errGateHandedOff) {
		t.Fatalf("pending must exit clean: %v\n%s", err, out)
	}
	// The gate is held until released, so a return at all — well inside the gate's
	// own safety bound — means the call handed off rather than waiting for it.
	if took >= testutil.WaitBudget {
		t.Fatalf("returned after %s, want inside the bound plus start-up, not waiting for the held gate", took)
	}
	var p pendingPayload
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &p); jerr != nil {
		t.Fatalf("pending output is not the pending payload: %v\n%s", jerr, out)
	}
	if p.Gate != "pending" || !strings.HasPrefix(p.Handle, gatehandle.Prefix) {
		t.Fatalf("payload = %+v", p)
	}
	// Nothing the reviewer prints while running reaches the agent's stream.
	if strings.Contains(out, "accepted: slow") {
		t.Fatalf("the still-running gate's output leaked into the pending return:\n%s", out)
	}

	store := gateStoreForTest(t)
	// A delivery that does not wait, while it is running, says nothing.
	if got := deliverFinishedGates(store, "", 0); got != "" {
		t.Fatalf("delivered a run that had not finished:\n%s", got)
	}
	// The wake: a delivery that waits gets the verdict.
	release()
	got := deliverFinishedGates(store, "", 3*testutil.WaitBudget)
	if !strings.Contains(got, p.Handle) {
		t.Fatalf("delivery missing the handle:\n%s", got)
	}
	assertVerdictOnly(t, "the delivery", got, "accepted: slow")
	// Exactly once.
	if again := deliverFinishedGates(store, "", 0); again != "" {
		t.Fatalf("a delivered run was delivered again:\n%s", again)
	}
}

// dirListing is the names and sizes under dir, so two calls can show nothing was
// written or removed between them.
func dirListing(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		fmt.Fprintf(&b, "%s %d\n", p, info.Size())
		return nil
	})
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	return b.String()
}

// sty_66439878: the detached gate child outlives the hand-off call by design, but
// not the test. Once drainGateChildren returns, the child is gone — even though
// its hold was released and it was about to write its verdict — so nothing writes
// into gates/<handle> and the directory can be removed.
func TestHandOff_NoChildOutlivesTheTestDrain(t *testing.T) {
	_ = tempRepo(t)
	hold, release := holdGate(t)
	args := append([]string{"gatetest", "--say", "drain"}, hold...)
	useGateHandOff(t, "300ms", args)

	out, err := runRoot(t, args...)
	if err != nil && !errors.Is(err, errGateHandedOff) {
		t.Fatalf("pending must exit clean: %v\n%s", err, out)
	}
	var p pendingPayload
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &p); jerr != nil || p.Gate != "pending" {
		t.Fatalf("want a pending payload: %v\n%s", jerr, out)
	}
	store := gateStoreForTest(t)
	m, merr := store.Meta(p.Handle)
	if merr != nil || m.PID <= 0 {
		t.Fatalf("the handle records no child pid: %+v %v", m, merr)
	}
	if got := gatehandle.ProbePID(m.PID); got != gatehandle.Alive {
		t.Fatalf("the held child = %v, want Alive before the drain", got)
	}

	release() // the child wakes and starts writing its verdict...
	drainGateChildren(t)

	// ...and the drain returned only once it was gone.
	if got := gatehandle.ProbePID(m.PID); got != gatehandle.Gone {
		t.Fatalf("after the drain the child = %v, want Gone", got)
	}
	handleDir := filepath.Join(store.Dir(), p.Handle)
	before := dirListing(t, handleDir)
	time.Sleep(250 * time.Millisecond) // time-subject: several of the gate's 10ms ticks, for a writer that should be gone
	if after := dirListing(t, handleDir); after != before {
		t.Fatalf("something still wrote into %s after the drain:\nbefore:\n%s\nafter:\n%s", handleDir, before, after)
	}
	if err := os.RemoveAll(handleDir); err != nil {
		t.Fatalf("the handle dir could not be removed after the drain: %v", err)
	}
}

// AC1: a rejected gate replays its failure — the child already printed the error
// to the stderr the caller now sees, so it must not be printed twice.
func TestHandOff_FailedGateReplaysAsFailure(t *testing.T) {
	_ = tempRepo(t)
	args := []string{"gatetest", "--fail", "--say", "no"}
	useGateHandOff(t, "20s", args)

	out, err := runRoot(t, args...)
	var ge *gateExitError
	if !errors.As(err, &ge) {
		t.Fatalf("a rejected gate must return a replayed failure, got %v\n%s", err, out)
	}
	if strings.Count(out, "rejected: no") != 1 {
		t.Fatalf("the rejection text must appear exactly once in what the caller sees:\n%s", out)
	}
	if _, show := finishExecute(err); show {
		t.Error("Execute must not print a replayed failure a second time")
	}
}

// `story rework` runs a consulting reviewer for minutes and is not a verb, so it
// reaches the hand-off through its own call to handOffGate. A step with no
// rework loop makes the detached run fail fast and deterministically — enough to
// show the relay is handed off and its outcome replayed.
func TestHandOff_ReworkRelayIsHandedOff(t *testing.T) {
	_ = tempRepo(t)
	created, err := runRoot(t, "story", "create", "--title", "Rework fixture", "--acceptance", "1. x")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, created)
	}
	var st struct {
		ID string `json:"id"`
	}
	if jerr := json.Unmarshal([]byte(created), &st); jerr != nil || st.ID == "" {
		t.Fatalf("parse create: %v\n%s", jerr, created)
	}
	args := []string{"story", "rework", st.ID}
	useGateHandOff(t, "20s", args)

	out, err := runRoot(t, args...)
	var ge *gateExitError
	if !errors.As(err, &ge) {
		t.Fatalf("the detached relay's failure must be replayed, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "declares no rework loop") {
		t.Fatalf("the relay's own message did not come back through the hand-off:\n%s", out)
	}
	store := gateStoreForTest(t)
	entries, _ := os.ReadDir(store.Dir())
	if len(entries) != 1 {
		t.Fatalf("expected the relay to run as one handed-off handle, got %d", len(entries))
	}
	if m, _ := store.Meta(entries[0].Name()); m.Verb != "story-rework" || m.Story != st.ID {
		t.Fatalf("handle = %+v", m)
	}
}

// A CLI command that builds its own reviewer engine (engineForCmd) bypasses the
// verb registry, so it must reach the hand-off itself. Guard: every non-test file
// that does so — other than the wiring in app.go and cmd_node.go — also calls
// handOffGate, so a new reviewer-running command cannot skip the contract
// (sty_c4b92c9e AC6). cmd_node.go is skipped: node join is launched by
// satelle.dev, never by an agent, and must hold its process.
func TestEveryEngineBuildingCommandHandsOff(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "app.go" || name == "cmd_node.go" {
			continue
		}
		b, _ := os.ReadFile(name)
		src := string(b)
		if strings.Contains(src, "engineForCmd(") && !strings.Contains(src, "handOffGate(") {
			t.Errorf("%s builds a reviewer engine (engineForCmd) but never calls handOffGate — an agent calling it would sit through the whole run", name)
		}
	}
}

// The bound holds for a verb that dispatches nothing: it never hands off.
func TestHandOff_NonDispatchingVerbRunsInline(t *testing.T) {
	_ = tempRepo(t)
	useGateHandOff(t, "1ms", []string{"story", "list"})
	if _, err := runRoot(t, "story", "list"); err != nil {
		t.Fatal(err)
	}
	if got := gateStoreForTest(t).Undelivered(); len(got) != 0 {
		t.Fatalf("a read verb created a handle: %v", got)
	}
}

// AC4: from an interactive terminal the gate runs in the foreground — no handle,
// no detached copy — and the progress sink is the stderr it always was.
func TestInteractive_RunsInForegroundWithoutAHandle(t *testing.T) {
	_ = tempRepo(t)
	clearHarnessEnv(t)
	t.Setenv(gateModeEnv, string(gateModeInteractive))
	oldChild := gateChildCommand
	gateChildCommand = func([]string) (*exec.Cmd, error) {
		t.Error("an interactive call started a detached copy")
		return nil, errors.New("must not start")
	}
	t.Cleanup(func() { gateChildCommand = oldChild })

	var out string
	var err error
	stderr := captureStderr(t, func() { out, err = runRoot(t, "gatetest", "--say", "here") })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// The verb speaks on the real stderr and the payload on stdout, as before.
	if !strings.Contains(stderr, "accepted: here") || !strings.Contains(out, `"say": "here"`) {
		t.Fatalf("interactive run lost its output:\nstdout=%s\nstderr=%s", out, stderr)
	}
	if got := gateStoreForTest(t).Undelivered(); len(got) != 0 {
		t.Fatalf("an interactive call created a handle: %v", got)
	}
}

func TestGateProgressSink_ByCaller(t *testing.T) {
	dir := t.TempDir()
	clearHarnessEnv(t)

	// Interactive: stderr, exactly as before.
	t.Setenv(gateModeEnv, string(gateModeInteractive))
	got := captureStderr(t, func() { gateProgressSink(config.Config{}, dir, dir)("gate 1/2: reviewing") })
	if got != "gate 1/2: reviewing\n" {
		t.Errorf("interactive progress = %q, want the line on stderr", got)
	}
	// Agent-facing: nothing on the agent's stream.
	t.Setenv(gateModeEnv, string(gateModeAgent))
	if got := captureStderr(t, func() { gateProgressSink(config.Config{}, dir, dir)("gate 1/2: reviewing") }); got != "" {
		t.Errorf("agent-facing progress reached stderr: %q", got)
	}
	// A detached run keeps it in the handle's progress log.
	old := gateRun
	gateRun.id, gateRun.runtime = "gw_test", dir
	t.Cleanup(func() { gateRun = old })
	m, err := gatehandle.New(dir).Create(gatehandle.Meta{ID: "gw_test", Verb: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := captureStderr(t, func() { gateProgressSink(config.Config{}, dir, dir)("gate 2/2: judging") }); got != "" {
		t.Errorf("a detached run's progress reached stderr: %q", got)
	}
	b, _ := os.ReadFile(gatehandle.New(dir).ProgressPath(m.ID))
	if string(b) != "gate 2/2: judging\n" {
		t.Errorf("progress log = %q", b)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

// --- the pending line -----------------------------------------------------

// AC3/AC5: the pending line quotes the notification path when the harness has
// one and the harness-named limitation when it does not — and never a polling
// instruction either way.
func TestPendingMessage_NamesPathOrLimitation_NeverAPollInstruction(t *testing.T) {
	for _, h := range []string{agentcli.HarnessClaude, agentcli.HarnessGrok, "", "mystery"} {
		msg := pendingMessage("gw_abc", h)
		facts := agentcli.FactsFor(h)
		if !strings.Contains(msg, "gw_abc") {
			t.Errorf("%q: pending line lacks the handle: %s", h, msg)
		}
		if strings.Contains(strings.ToLower(msg), "poll with") || strings.Contains(strings.ToLower(msg), "check with") {
			t.Errorf("%q: pending line carries a polling instruction: %s", h, msg)
		}
		if !strings.Contains(msg, "do not poll") {
			t.Errorf("%q: pending line does not forbid polling: %s", h, msg)
		}
		if facts.CompletionNotification.Available {
			if !strings.Contains(msg, "delivered into this session as a notification") {
				t.Errorf("%q: available harness does not name the notification path: %s", h, msg)
			}
		} else if !strings.Contains(msg, facts.CompletionNotification.Reason) {
			t.Errorf("%q: unavailable harness does not quote its limitation %q: %s", h, facts.CompletionNotification.Reason, msg)
		}
	}
}

// --- the notification path ------------------------------------------------

// AC3: a harness recorded as able to wake its driver must actually have the hook
// the wake rides on installed by the scaffold — the capability cell is tied to
// the code, so it cannot claim a wake nothing wired.
func TestCompletionNotification_CellsMatchTheScaffold(t *testing.T) {
	for _, f := range agentcli.HarnessFactsTable() {
		wired := agentcli.HarnessHooks(f.Harness).HasEvent("Stop")
		if f.CompletionNotification.Available && !wired {
			t.Errorf("%s is recorded as delivering a completion notification but its scaffold installs no Stop hook", f.Harness)
		}
	}
	// And the Stop hook the scaffold writes waits long enough for a gate.
	for name, timeout := range installedStopTimeouts(t) {
		if timeout < float64(stopGateWaitDefault/time.Second) {
			t.Errorf("%s: the Stop hook timeout %vs is shorter than its %s wait", name, timeout, stopGateWaitDefault)
		}
	}
}

// installedStopTimeouts is the timeout, in seconds, the scaffold installs on the
// stopcheck Stop hook of each harness that has one.
func installedStopTimeouts(t *testing.T) map[string]float64 {
	t.Helper()
	got := map[string]float64{}
	for name, doc := range map[string][]byte{"claude": buildClaudeHookSettings(t.TempDir()), "grok": buildGrokHookSettings(t.TempDir())} {
		var s struct {
			Hooks struct {
				Stop []struct {
					Hooks []struct {
						Command string  `json:"command"`
						Timeout float64 `json:"timeout"`
					} `json:"hooks"`
				} `json:"Stop"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(doc, &s); err != nil {
			t.Fatal(err)
		}
		for _, g := range s.Hooks.Stop {
			for _, h := range g.Hooks {
				if strings.Contains(h.Command, "satelle hook stopcheck") {
					got[name] = h.Timeout
				}
			}
		}
		if _, ok := got[name]; !ok {
			t.Errorf("%s: no stopcheck Stop hook in the scaffold", name)
		}
	}
	return got
}

func TestRaiseStopHookTimeout(t *testing.T) {
	mk := func(timeout any) any {
		h := map[string]any{"type": "command", "command": stopcheckHookCommand}
		if timeout != nil {
			h["timeout"] = timeout
		}
		return []any{map[string]any{"hooks": []any{h}}}
	}
	if ev := mk(nil); !raiseStopHookTimeout(ev) {
		t.Error("a Stop hook with no timeout was not raised")
	}
	if ev := mk(float64(60)); !raiseStopHookTimeout(ev) {
		t.Error("a shorter timeout was not raised")
	}
	if ev := mk(float64(stopHookTimeoutSec * 2)); raiseStopHookTimeout(ev) {
		t.Error("a longer operator-set timeout was lowered")
	}
	if raiseStopHookTimeout(nil) {
		t.Error("no Stop event reported a change")
	}
}

// The Stop hook is the wake: a running gate is waited for and its verdict comes
// back as the block reason; with nothing running it stays out of the way.
func TestStopHook_WaitsForRunningGateAndAnswersWithVerdict(t *testing.T) {
	_ = tempRepo(t)
	clearHarnessEnv(t)
	t.Setenv(stopGateWaitEnv, "10s")
	store := gateStoreForTest(t)

	var quiet strings.Builder
	if err := runHookStopcheck(nil, &quiet); err != nil || strings.Contains(quiet.String(), "gate gw_") {
		t.Fatalf("with no gate the hook must not deliver: %v %q", err, quiet.String())
	}

	m, err := store.Create(gatehandle.Meta{Verb: gateTestVerb, Story: "sty_wake", Argv: []string{"story", "set", "sty_wake"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SetPID(m.ID, os.Getpid()) // alive: a running gate
	// launched is taken before the gate's clock starts, so the gate can never
	// finish less than gateRuns after it, however late the hook starts waiting.
	const gateRuns = 400 * time.Millisecond
	launched := time.Now()
	go func() {
		time.Sleep(gateRuns) // time-subject: the gate runs for a known minimum, so a hook that returned sooner did not wait for it
		_ = os.WriteFile(store.VerdictPath(m.ID), []byte("accepted plan→in_progress by satelle-story-plan-review\n"), 0o644)
		_ = os.WriteFile(store.ErrPath(m.ID), []byte(gateTestAdvisory+"\n"), 0o644)
		_ = os.WriteFile(store.OutPath(m.ID), []byte(`{"id":"sty_wake","status":"in_progress"}`), 0o644)
		_ = store.Finish(m.ID, gatehandle.Result{})
	}()

	var out strings.Builder
	if err := runHookStopcheck(nil, &out); err != nil {
		t.Fatal(err)
	}
	if time.Since(launched) < gateRuns {
		t.Fatalf("the Stop hook did not wait for the running gate (returned %s after the gate started)", time.Since(launched))
	}
	var blk stopBlockOut
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &blk); err != nil {
		t.Fatalf("Stop hook output is not a block: %v\n%s", err, out.String())
	}
	if blk.Decision != "block" || !strings.Contains(blk.Reason, m.ID) || !strings.Contains(blk.Reason, "accepted plan→in_progress") {
		t.Fatalf("block = %+v, want the verdict as the reason", blk)
	}
	if strings.Contains(blk.Reason, "sty_wake\",\"status") || strings.Contains(blk.Reason, gateTestAdvisory) {
		t.Fatalf("the Stop-hook block carries the story record or advisory note, not only the verdict:\n%s", blk.Reason)
	}
	var again strings.Builder
	_ = runHookStopcheck(nil, &again)
	if strings.Contains(again.String(), m.ID) {
		t.Fatalf("the verdict was delivered twice:\n%s", again.String())
	}
}

// UserPromptSubmit is the catch-up for a gate that finished between turns.
func TestPromptHook_DeliversFinishedGateOnce(t *testing.T) {
	_ = tempRepo(t)
	clearHarnessEnv(t)
	store := gateStoreForTest(t)
	m, _ := store.Create(gatehandle.Meta{Verb: gateTestVerb, Story: "sty_prompt", Argv: []string{"story", "set", "sty_prompt"}})
	_ = os.WriteFile(store.ErrPath(m.ID), []byte("rejected plan→in_progress: no estimate\n"), 0o644)
	_ = store.Finish(m.ID, gatehandle.Result{ExitCode: 1, Error: "rejected plan→in_progress: no estimate"})

	var first strings.Builder
	if err := runHookPrompt(&first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), m.ID) || !strings.Contains(first.String(), "no estimate") {
		t.Fatalf("the finished gate was not put in front of the model:\n%s", first.String())
	}
	var second strings.Builder
	_ = runHookPrompt(&second)
	if strings.Contains(second.String(), m.ID) {
		t.Fatalf("the finished gate was delivered twice:\n%s", second.String())
	}
}

// A session is told about its own runs, not a sibling's.
func TestDelivery_ScopedToTheOwningSession(t *testing.T) {
	store := gatehandle.New(t.TempDir())
	m, _ := store.Create(gatehandle.Meta{Verb: "x", Session: "sess-A"})
	_ = store.Finish(m.ID, gatehandle.Result{})
	if got := deliverFinishedGates(store, "sess-B", 0); got != "" {
		t.Fatalf("session B was told about session A's gate:\n%s", got)
	}
	if got := deliverFinishedGates(store, "sess-A", 0); !strings.Contains(got, m.ID) {
		t.Fatalf("session A was not told about its own gate: %q", got)
	}
}

// A run whose process died without a result is delivered as a failure — the
// session is never left waiting on a gate that can no longer answer.
func TestDelivery_DiedRunIsDeliveredAsFailure(t *testing.T) {
	store := gatehandle.New(t.TempDir())
	m, _ := store.Create(gatehandle.Meta{Verb: "x", Argv: []string{"story", "set", "sty_d"}, Story: "sty_d"})
	_ = store.SetPID(m.ID, 0x7ffffff0) // no such process
	got := deliverFinishedGates(store, "", 5*time.Second)
	if !strings.Contains(got, "DIED") || !strings.Contains(got, m.ID) {
		t.Fatalf("a died run must be delivered as a failure:\n%s", got)
	}
}

// sty_f3dc2a97: the hand-off decision follows the SESSION's harness, not the
// union of every scaffold a repo keeps installed. A repo with both .claude/ and
// .grok/ must not force a pi session to hand off on grok's 15s cutoff — the
// verdict would then wait on a Stop hook the pi session never fires.
func TestConfiguredDriverHarnesses_FollowsTheSessionNotTheScaffolds(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{".claude", ".grok"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A pi session in a repo with every scaffold installed: pi alone.
	got := configuredDriverHarnesses(dir, []string{"PI_CODING_AGENT=true"})
	if len(got) != 1 || got[0] != agentcli.HarnessPi {
		t.Errorf("pi session in a .claude+.grok repo = %v, want [%s]", got, agentcli.HarnessPi)
	}
	if agentcli.HandoffNeeded(got) {
		t.Error("a pi-only session must not need a hand-off")
	}
	// A grok session in the same repo still hands off — unchanged behaviour.
	grok := configuredDriverHarnesses(dir, []string{"GROK_AGENT=1"})
	if !agentcli.HandoffNeeded(grok) {
		t.Errorf("grok session = %v, want a hand-off (15s cutoff)", grok)
	}
	// No session marker: fall back to the installed scaffolds, as before.
	bare := configuredDriverHarnesses(dir, []string{"PATH=/usr/bin"})
	if len(bare) != 2 {
		t.Errorf("no session marker = %v, want both scaffolds [claude grok]", bare)
	}
}
