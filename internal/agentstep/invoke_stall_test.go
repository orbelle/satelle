package agentstep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// progressingRunner emits a real event every interval for duration, then
// returns out — a fake agent that is slow but genuinely working (sty_752c4ef2
// AC1).
type progressingRunner struct {
	interval, duration time.Duration
	out                string
	events             int
}

func (r *progressingRunner) Name() string    { return "progressing" }
func (r *progressingRunner) Command() string { return "progressing" }
func (r *progressingRunner) Run(ctx context.Context, req agentcli.Request) ([]byte, error) {
	deadline := time.Now().Add(r.duration)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
			r.events++
			if req.OnEvent != nil {
				req.OnEvent(agentcli.Event{Kind: agentcli.EventToolStart, Tool: "Bash"})
				req.OnEvent(agentcli.Event{Kind: agentcli.EventToolEnd, Tool: "Bash"})
			}
		}
	}
	return []byte(r.out), nil
}

// heartbeatOnlyRunner emits ONE real event (a tool start) then goes silent
// except for heartbeats until ctx is cancelled — a fake agent that is stuck,
// not merely slow (sty_752c4ef2 AC2 evidence #2: a heartbeat proves the
// process exists, not that it is progressing).
type heartbeatOnlyRunner struct{}

func (r *heartbeatOnlyRunner) Name() string    { return "heartbeat-only" }
func (r *heartbeatOnlyRunner) Command() string { return "heartbeat-only" }
func (r *heartbeatOnlyRunner) Run(ctx context.Context, req agentcli.Request) ([]byte, error) {
	if req.OnEvent != nil {
		req.OnEvent(agentcli.Event{Kind: agentcli.EventToolStart, Tool: "Bash"})
	}
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
			if req.OnEvent != nil {
				req.OnEvent(agentcli.Event{Kind: agentcli.EventHeartbeat})
			}
		}
	}
}

// TestProgressingAgentNotKilled (AC1): a fake runner keeps emitting real
// events past the old 20m/10m default, with idle_timeout set small and no
// hard timeout configured — the dispatch must complete, never be cut off by
// elapsed time alone.
//
// time-subject: the idle window is what is under test. It is wide against the
// 20ms event cadence (600ms) so a scheduler stall on a slow runner cannot lapse
// it between events, and the run outlasts it (900ms), which is what proves elapsed
// time alone never kills a progressing agent.
func TestProgressingAgentNotKilled(t *testing.T) {
	r := &progressingRunner{interval: 20 * time.Millisecond, duration: 900 * time.Millisecond, out: "DONE"}
	g := New(r, fakeDocs{}, "/repo", "")
	g.idleTimeout = 600 * time.Millisecond // << the old wall-clock defaults; proves elapsed time alone never kills it
	g.agentTimeout = 0                     // no hard ceiling

	start := time.Now()
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Principles: config.PrinciplesNone},
		Section: "coder",
		Rubric:  "do it",
		Payload: map[string]string{},
		Expect:  ExpectPerform,
		Runner:  r,
	})
	if res.Err != nil {
		t.Fatalf("a progressing agent must not be killed: %v", res.Err)
	}
	if string(res.Stdout) != "DONE" {
		t.Errorf("res.Stdout = %q, want %q", res.Stdout, "DONE")
	}
	if elapsed := time.Since(start); elapsed < r.duration {
		t.Errorf("returned before the fake agent finished: elapsed %v, want >= %v", elapsed, r.duration)
	}
	if r.events == 0 {
		t.Fatal("fake agent never got to emit an event — test is not exercising the watchdog")
	}
}

// TestHeartbeatOnlyAgentStalls_ExpectPerform (AC2): a dispatch that emits no
// real event for idle_timeout is stopped even though heartbeats keep
// arriving, on the perform path (a named dispatch, e.g. the coder).
//
// time-subject: the 60ms idle window is the subject; the assertions are lower
// bounds on the stall (Idle >= idleTimeout), so a slow runner cannot fail them.
func TestHeartbeatOnlyAgentStalls_ExpectPerform(t *testing.T) {
	r := &heartbeatOnlyRunner{}
	g := New(r, fakeDocs{}, "/repo", "")
	var stalled []map[string]any
	g.SetTelemetry(func(_ context.Context, storyID, actor, kind string, data map[string]any) {
		if kind == "agent-stalled" {
			stalled = append(stalled, data)
		}
	})
	g.idleTimeout = 60 * time.Millisecond
	g.agentTimeout = 0

	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Principles: config.PrinciplesNone},
		Section: "coder",
		Rubric:  "do it",
		Payload: map[string]string{},
		Expect:  ExpectPerform,
		Runner:  r,
		StoryID: "sty_1",
	})
	if res.Err == nil {
		t.Fatal("a heartbeat-only agent must be judged stalled, not left to run forever")
	}
	var se *StallError
	if !errors.As(res.Err, &se) {
		t.Fatalf("res.Err = %v, want a *StallError", res.Err)
	}
	if se.Idle < g.idleTimeout {
		t.Errorf("StallError.Idle = %v, want >= %v", se.Idle, g.idleTimeout)
	}
	if se.LastEvent != "tool: Bash" {
		t.Errorf("StallError.LastEvent = %q, want %q", se.LastEvent, "tool: Bash")
	}
	if classifyOutcome(res.Err) != "stalled" {
		t.Errorf("classifyOutcome = %q, want %q", classifyOutcome(res.Err), "stalled")
	}
	if len(stalled) != 1 {
		t.Fatalf("expected exactly one agent-stalled telemetry event, got %d: %v", len(stalled), stalled)
	}
	if res.Err.Error() == "" || !containsStalled(res.Err.Error()) {
		t.Errorf("refusal text must name the stall, got %q", res.Err.Error())
	}
	if containsDeadlineExceeded(res.Err.Error()) {
		t.Errorf("refusal text must not read like a hard-timeout deadline: %q", res.Err.Error())
	}
}

// TestHeartbeatOnlyAgentStalls_ExpectVerdict (AC2): same stall detection on
// the verdict path (a gate reviewer).
//
// time-subject: the 60ms idle window is the subject; nothing here is an upper
// bound on how fast the stall arrives.
func TestHeartbeatOnlyAgentStalls_ExpectVerdict(t *testing.T) {
	r := &heartbeatOnlyRunner{}
	g := New(r, fakeDocs{}, "/repo", "")
	var stalled []map[string]any
	g.SetTelemetry(func(_ context.Context, storyID, actor, kind string, data map[string]any) {
		if kind == "agent-stalled" {
			stalled = append(stalled, data)
		}
	})
	g.idleTimeout = 60 * time.Millisecond
	g.agentTimeout = 0
	g.backoff = func(int) time.Duration { return 0 }

	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Principles: config.PrinciplesNone},
		Section: "reviewer",
		Rubric:  "judge",
		Payload: map[string]string{},
		Expect:  ExpectVerdict,
		Runner:  r,
		Skill:   "test-skill",
		StoryID: "sty_1",
	})
	if res.Err == nil {
		t.Fatal("a heartbeat-only reviewer must be judged stalled, not retried forever")
	}
	var se *StallError
	if !errors.As(res.Err, &se) {
		t.Fatalf("res.Err = %v, want a *StallError", res.Err)
	}
	if classifyOutcome(res.Err) != "stalled" {
		t.Errorf("classifyOutcome = %q, want %q", classifyOutcome(res.Err), "stalled")
	}
	if len(stalled) != 1 {
		t.Fatalf("expected exactly one agent-stalled telemetry event (no blind retry of a stuck reviewer), got %d", len(stalled))
	}
	if !containsStalled(res.Err.Error()) {
		t.Errorf("refusal text must name the stall, got %q", res.Err.Error())
	}
}

// askingRunner reports a denied interactive question mid-turn, then answers.
type askingRunner struct{}

func (askingRunner) Name() string    { return "asking" }
func (askingRunner) Command() string { return "asking" }
func (askingRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	if req.OnEvent != nil {
		req.OnEvent(agentcli.Event{
			Kind: agentcli.EventInteractiveDenied, Tool: "Ask", Text: "which story?",
			Meta: map[string]string{agentcli.EventMetaResponse: "denied"},
		})
	}
	return []byte(`{"decision":"accept","notes":"ok"}`), nil
}

// TestInvokeLedgersInteractiveDenied (sty_32795645 AC2): a denied or
// auto-answered ask-the-user question is ledgered with tool and question text.
func TestInvokeLedgersInteractiveDenied(t *testing.T) {
	g := New(askingRunner{}, fakeDocs{}, "/repo", "")
	var mu sync.Mutex
	var got []map[string]any
	g.SetTelemetry(func(_ context.Context, storyID, actor, kind string, data map[string]any) {
		if kind == "agent-interactive-denied" {
			mu.Lock()
			got = append(got, data)
			mu.Unlock()
		}
	})
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Principles: config.PrinciplesNone},
		Section: "reviewer",
		Rubric:  "judge",
		Payload: map[string]string{},
		Expect:  ExpectVerdict,
		Runner:  askingRunner{},
		StoryID: "sty_1",
	})
	if res.Err != nil {
		t.Fatalf("Invoke: %v", res.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0]["tool"] != "Ask" || got[0]["question"] != "which story?" || got[0]["response"] != "denied" {
		t.Fatalf("ledgered = %+v, want one entry with tool Ask, question, response denied", got)
	}
}

// TestIdleTimeoutConfigMovesStallPoint (AC3): idle_timeout is read from
// configuration; changing it moves the stall point with no code change.
//
// time-subject: the configured idle window is the subject. The two windows are
// far apart (100ms, 1.5s) so the smaller stalls well before the larger could on
// any runner, and each ceiling is wide enough that a loaded machine cannot cross it.
func TestIdleTimeoutConfigMovesStallPoint(t *testing.T) {
	// The ceiling is the NEXT configured value, not a multiple of this one: the
	// point under test is that the stall point MOVES with configuration, and a
	// fixed 2x bound flaked on a loaded CI runner (50ms config stalled at 129ms,
	// sty_ef930f81 release). 100ms must still stall before 1.5s could.
	ceilings := map[time.Duration]time.Duration{
		100 * time.Millisecond:  1500 * time.Millisecond,
		1500 * time.Millisecond: 15 * time.Second,
	}
	for _, idle := range []time.Duration{100 * time.Millisecond, 1500 * time.Millisecond} {
		t.Run(idle.String(), func(t *testing.T) {
			r := &heartbeatOnlyRunner{}
			g := New(r, fakeDocs{}, "/repo", "")
			g.idleTimeout = idle
			g.agentTimeout = 0

			start := time.Now()
			res := g.Invoke(context.Background(), InvokeRequest{
				Binding: config.AgentBinding{Command: "claude", Principles: config.PrinciplesNone},
				Section: "coder",
				Rubric:  "do it",
				Payload: map[string]string{},
				Expect:  ExpectPerform,
				Runner:  r,
			})
			elapsed := time.Since(start)
			var se *StallError
			if !errors.As(res.Err, &se) {
				t.Fatalf("res.Err = %v, want a *StallError", res.Err)
			}
			// Generous slack: the point under test is that the stall point MOVES
			// with configuration, not exact timing (sty_752c4ef2 integration-
			// coverage-review note on AC3).
			if elapsed < idle {
				t.Errorf("stalled after %v, want >= configured idle_timeout %v", elapsed, idle)
			}
			if elapsed >= ceilings[idle] {
				t.Errorf("stalled after %v, want before %v (configured idle_timeout %v must move the stall point)", elapsed, ceilings[idle], idle)
			}
		})
	}
}

// TestDispatchExecutorHonorsDefaultsIdleTimeout (AC3): the resolution ladder
// production wiring uses — config.AgentsConfig.ResolveIdleTimeout, wired
// through SetIdleTimeoutResolver exactly as internal/cli/app.go's
// PersistentPreRunE does — moves the stall point when ONLY [defaults]
// idle_timeout changes in real agents.toml text, with no per-binding
// idle_timeout override and no code change. Unlike
// TestIdleTimeoutConfigMovesStallPoint (which pokes g.idleTimeout directly),
// this drives the real DispatchExecutor path a one-shot dispatch takes, so it
// exercises the [defaults] ladder a raw g.idleTimeout poke never reaches. A
// SetIdleTimeoutResolver left unwired (the app.go regression this story's
// AC3 review round found) would fall back to the binding alone against the
// engine-wide default, so both configured values below would stall at the
// same point and this test would fail.
//
// time-subject: the configured idle window is the subject. As in
// TestIdleTimeoutConfigMovesStallPoint, the ceiling is the next configured
// value rather than a multiple of this one, so slowness cannot cross it.
func TestDispatchExecutorHonorsDefaultsIdleTimeout(t *testing.T) {
	ceilings := map[time.Duration]time.Duration{
		100 * time.Millisecond:  1500 * time.Millisecond,
		1500 * time.Millisecond: 15 * time.Second,
	}
	for _, want := range []time.Duration{100 * time.Millisecond, 1500 * time.Millisecond} {
		t.Run(want.String(), func(t *testing.T) {
			dir := t.TempDir()
			body := fmt.Sprintf("[defaults]\nidle_timeout = %q\n\n[architect]\ncommand = \"fake -p {system}\"\ntools = \"read_file\"\n", want.String())
			if err := os.WriteFile(filepath.Join(dir, config.AgentsConfigName), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			ac, err := config.LoadAgents(dir)
			if err != nil {
				t.Fatal(err)
			}

			docs := fakeDocs{workflow: dispatchWF, skillBody: "alignment rubric", skillFound: true}
			g, _ := newEngine(t, "", docs)
			r := &heartbeatOnlyRunner{}
			g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
			g.SetNamedAgents(ac.NamedBinding)
			g.SetIdleTimeoutResolver(func(section string, b config.AgentBinding) (time.Duration, error) {
				return ac.ResolveIdleTimeout(b, DefaultIdleTimeout)
			})

			start := time.Now()
			_, err = g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan")
			elapsed := time.Since(start)
			var se *StallError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want a *StallError", err)
			}
			if elapsed < want || elapsed >= ceilings[want] {
				t.Errorf("stalled after %v, want in [%v, %v) for the configured [defaults] idle_timeout", elapsed, want, ceilings[want])
			}
		})
	}
}

// TestHardTimeoutStillCapsProgressingAgent (AC3): a configured hard timeout
// still bounds total time even when the agent keeps progressing — the
// optional ceiling, when authored, wins over the stall detector.
//
// time-subject: the 50ms hard timeout is the subject. The agent would keep
// progressing for 30s, so "bounded by the timeout" and "ran to completion" stay
// far apart on any runner.
func TestHardTimeoutStillCapsProgressingAgent(t *testing.T) {
	r := &progressingRunner{interval: 5 * time.Millisecond, duration: 30 * time.Second, out: "DONE"}
	g := New(r, fakeDocs{}, "/repo", "")
	g.idleTimeout = time.Minute // idle would never fire first

	start := time.Now()
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Principles: config.PrinciplesNone},
		Section: "coder",
		Rubric:  "do it",
		Payload: map[string]string{},
		Expect:  ExpectPerform,
		// A named dispatch's hard ceiling is resolved by its CALLER
		// (DispatchExecutor: binding.TimeoutDuration(g.agentTimeout)) and
		// passed explicitly — ExpectPerform does not fall back to
		// g.agentTimeout on its own, only ExpectVerdict does. Set it here the
		// way a real caller would.
		Timeout: 50 * time.Millisecond,
		Runner:  r,
	})
	if res.Err == nil {
		t.Fatal("a configured hard timeout must still stop a progressing agent")
	}
	var se *StallError
	if errors.As(res.Err, &se) {
		t.Fatalf("hard-timeout stop must not be reported as a stall: %v", se)
	}
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Errorf("res.Err = %v, want context.DeadlineExceeded", res.Err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("hard timeout did not bound the run: took %v", elapsed)
	}
}

// TestActivityDetailThrottledDuringDispatch (sty_752c4ef2 AC5): a fake mirror
// pusher (the SetActivityDetail sink) records at least one push before a long
// dispatch returns, and its EventAt/EventCount advance strictly between
// pushes — proving the throttle refreshes, rather than stamping once and going
// stale for the rest of a multi-minute dispatch.
//
// time-subject: the 20ms push throttle is the subject. The dispatch runs for
// 1s, 50 throttle windows, so a slow runner that drops most of the 5ms ticks
// still lands the two pushes the assertion needs.
func TestActivityDetailThrottledDuringDispatch(t *testing.T) {
	old := activityDetailThrottle
	activityDetailThrottle = 20 * time.Millisecond
	t.Cleanup(func() { activityDetailThrottle = old })

	r := &progressingRunner{interval: 5 * time.Millisecond, duration: time.Second, out: "DONE"}
	g := New(r, fakeDocs{}, "/repo", "")
	g.idleTimeout = time.Minute

	var mu sync.Mutex
	var pushes []ActivityDetail
	g.SetActivityDetail(func(itemID string, d ActivityDetail) {
		mu.Lock()
		defer mu.Unlock()
		pushes = append(pushes, d)
	})

	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Model: "sonnet", Principles: config.PrinciplesNone},
		Section: "coder",
		Rubric:  "do it",
		Payload: map[string]string{},
		Expect:  ExpectPerform,
		Runner:  r,
		StoryID: "sty_1",
	})
	if res.Err != nil {
		t.Fatalf("dispatch failed: %v", res.Err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pushes) < 2 {
		t.Fatalf("expected at least 2 throttled pushes over a %v dispatch with a %v throttle, got %d", r.duration, activityDetailThrottle, len(pushes))
	}
	for i, p := range pushes {
		if p.Agent != "coder" || p.Model != "sonnet" {
			t.Errorf("push[%d] = %+v, want agent=coder model=sonnet", i, p)
		}
		if i > 0 {
			if !p.EventAt.After(pushes[i-1].EventAt) {
				t.Errorf("push[%d].EventAt did not advance: prev=%v cur=%v", i, pushes[i-1].EventAt, p.EventAt)
			}
			if p.EventCount <= pushes[i-1].EventCount {
				t.Errorf("push[%d].EventCount did not advance: prev=%d cur=%d", i, pushes[i-1].EventCount, p.EventCount)
			}
		}
	}
}

// TestCommandTransportHonorsWatchdog (sty_752c4ef2 AC4): the watchdog needs no
// transport-specific code because it wraps the SAME ctx every runner spawns
// its child through (exec.CommandContext). This proves it against a REAL
// command-interface Runner and a real subprocess, not just a fake Runner: a
// shell script that prints nothing (so no real event ever fires past the
// transport's own EventStart) while alive must be killed once idle_timeout
// elapses, not run to completion.
//
// time-subject: the 80ms idle window is the subject; the subprocess sleeps 60s
// so "killed" and "ran to completion" stay far apart whatever the machine speed.
func TestCommandTransportHonorsWatchdog(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "silent-agent.sh")
	// exec replaces the shell's own process image with sleep, so the watchdog's
	// kill (SIGKILL on this one PID) closes the stdout pipe immediately. A plain
	// "sleep 5 &" child would inherit the pipe fd and keep it open after the
	// shell dies, masking the kill behind pipe-EOF latency — a test-script
	// artifact, not something runProcess needs to handle specially.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := agentcli.RunnerFromBinding(agentcli.InterfaceCommand, script+" --noop")
	if err != nil {
		t.Fatal(err)
	}
	g := New(runner, fakeDocs{}, "/repo", "")
	g.idleTimeout = 80 * time.Millisecond
	g.agentTimeout = 0

	start := time.Now()
	_, _, err = g.runOnce(context.Background(), runner, agentcli.Request{SystemPrompt: "x"}, 0, g.idleTimeout)
	if err == nil {
		t.Fatal("a silent subprocess must be judged stalled, not run to completion")
	}
	var se *StallError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a *StallError", err)
	}
	if elapsed := time.Since(start); elapsed >= 30*time.Second {
		t.Errorf("subprocess was not killed by the watchdog: took %v (script sleeps 60s)", elapsed)
	}
}

// TestActivityDetailCarriesRealSubprocessPid (sty_752c4ef2 AC5): the
// ActivityDetail pushed to the SetActivityDetail sink carries the SPAWNED
// child's real OS pid, not zero — proven against a real command-interface
// subprocess (not a fake Runner), since the pid is not a value agentstep
// invents: agentcli stamps it on EventStart from cmd.Process.Pid.
func TestActivityDetailCarriesRealSubprocessPid(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude-quick-agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := agentcli.RunnerFromBinding(agentcli.InterfaceCommand, script+" --noop")
	if err != nil {
		t.Fatal(err)
	}
	g := New(runner, fakeDocs{}, dir, "")
	g.idleTimeout = time.Minute
	g.agentTimeout = 0

	var mu sync.Mutex
	var pushes []ActivityDetail
	g.SetActivityDetail(func(_ string, d ActivityDetail) {
		mu.Lock()
		defer mu.Unlock()
		pushes = append(pushes, d)
	})

	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: script + " --noop", Principles: config.PrinciplesNone},
		Section: "coder",
		Rubric:  "do it",
		Payload: map[string]string{},
		Expect:  ExpectPerform,
		Runner:  runner,
		StoryID: "sty_1",
	})
	if res.Err != nil {
		t.Fatalf("dispatch failed: %v", res.Err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pushes) == 0 {
		t.Fatal("expected at least one ActivityDetail push")
	}
	if pushes[0].Pid == 0 {
		t.Error("ActivityDetail.Pid must carry the spawned subprocess's real pid, got 0")
	}
	if pushes[0].Pid == os.Getpid() {
		t.Error("ActivityDetail.Pid must be the CHILD's pid, not the test process's own")
	}
}

// TestStreamTransportHonorsWatchdog (sty_752c4ef2 AC4): the same silent-
// subprocess proof as TestCommandTransportHonorsWatchdog, against a REAL
// interface=stream Runner. The watchdog needs no transport-specific code
// because every transport spawns its child through exec.CommandContext(ctx,
// ...) against the SAME context the watchdog wraps — this proves that holds
// for stream too, not just command.
//
// time-subject: the 80ms idle window is the subject (see the command variant).
func TestStreamTransportHonorsWatchdog(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "silent-stream-agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := agentcli.RunnerFromBinding(agentcli.InterfaceStream, script+" --stream")
	if err != nil {
		t.Fatal(err)
	}
	g := New(runner, fakeDocs{}, dir, "")
	g.idleTimeout = 80 * time.Millisecond
	g.agentTimeout = 0

	start := time.Now()
	_, _, err = g.runOnce(context.Background(), runner, agentcli.Request{SystemPrompt: "x"}, 0, g.idleTimeout)
	if err == nil {
		t.Fatal("a silent stream subprocess must be judged stalled, not run to completion")
	}
	var se *StallError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a *StallError", err)
	}
	if elapsed := time.Since(start); elapsed >= 30*time.Second {
		t.Errorf("stream subprocess was not killed by the watchdog: took %v (script sleeps 60s)", elapsed)
	}
}

// TestACPTransportHonorsWatchdog (sty_752c4ef2 AC4): same proof against a
// REAL interface=acp Runner.
//
// time-subject: the 80ms idle window is the subject (see the command variant).
func TestACPTransportHonorsWatchdog(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "silent-acp-agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := agentcli.RunnerFromBinding(agentcli.InterfaceACP, script+" --acp")
	if err != nil {
		t.Fatal(err)
	}
	g := New(runner, fakeDocs{}, dir, "")
	g.idleTimeout = 80 * time.Millisecond
	g.agentTimeout = 0

	start := time.Now()
	_, _, err = g.runOnce(context.Background(), runner, agentcli.Request{SystemPrompt: "x"}, 0, g.idleTimeout)
	if err == nil {
		t.Fatal("a silent ACP subprocess must be judged stalled, not run to completion")
	}
	var se *StallError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a *StallError", err)
	}
	if elapsed := time.Since(start); elapsed >= 30*time.Second {
		t.Errorf("ACP subprocess was not killed by the watchdog: took %v (script sleeps 60s)", elapsed)
	}
}

func containsStalled(s string) bool {
	return strings.Contains(strings.ToLower(s), "stall")
}

func containsDeadlineExceeded(s string) bool {
	return strings.Contains(strings.ToLower(s), "deadline exceeded")
}

// scriptStep is one event an eventScriptRunner emits, after waiting wait.
type scriptStep struct {
	wait time.Duration
	ev   agentcli.Event
}

// eventScriptRunner emits a fixed event script, then returns "DONE".
type eventScriptRunner struct{ steps []scriptStep }

func (r *eventScriptRunner) Name() string    { return "event-script" }
func (r *eventScriptRunner) Command() string { return "event-script" }
func (r *eventScriptRunner) Run(ctx context.Context, req agentcli.Request) ([]byte, error) {
	for _, s := range r.steps {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.wait):
		}
		if req.OnEvent != nil {
			req.OnEvent(s.ev)
		}
	}
	return []byte("DONE"), nil
}

// dispatchActivity runs one ExpectPerform dispatch of r with the activity
// throttle set to throttle and returns every ActivityDetail pushed.
func dispatchActivity(t *testing.T, r agentcli.Runner, throttle time.Duration) []ActivityDetail {
	t.Helper()
	old := activityDetailThrottle
	activityDetailThrottle = throttle
	t.Cleanup(func() { activityDetailThrottle = old })

	g := New(r, fakeDocs{}, "/repo", "")
	g.idleTimeout = time.Minute
	var mu sync.Mutex
	var pushes []ActivityDetail
	g.SetActivityDetail(func(_ string, d ActivityDetail) {
		mu.Lock()
		defer mu.Unlock()
		pushes = append(pushes, d)
	})
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "cursor", Principles: config.PrinciplesNone},
		Section: "coder",
		Rubric:  "do it",
		Payload: map[string]string{},
		Expect:  ExpectPerform,
		Runner:  r,
		StoryID: "sty_1",
	})
	if res.Err != nil {
		t.Fatalf("dispatch failed: %v", res.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]ActivityDetail(nil), pushes...)
}

func pushLabels(ps []ActivityDetail) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.EventLabel
	}
	return out
}

// TestActivityDetailFollowsCommandTransportProgress (sty_5650fbcc AC3): the
// observed cursor run — a command-transport performer whose CLI prints
// nothing, so after EventStart the only thing it reports is the liveness
// probe's EventProgress as its process tree works. The last activity must
// advance past the start, labelled "working", while the real-event count stays
// at the one start event.
//
// time-subject: the 20ms throttle is the subject; progress arrives 60ms apart,
// three throttle windows, so each one is past the throttle on any runner.
func TestActivityDetailFollowsCommandTransportProgress(t *testing.T) {
	r := &eventScriptRunner{steps: []scriptStep{
		{0, agentcli.Event{Kind: agentcli.EventStart}},
		{60 * time.Millisecond, agentcli.Event{Kind: agentcli.EventProgress}},
		{60 * time.Millisecond, agentcli.Event{Kind: agentcli.EventProgress}},
	}}
	pushes := dispatchActivity(t, r, 20*time.Millisecond)
	if len(pushes) < 2 {
		t.Fatalf("pushes = %v, want the start then the performer's later activity", pushLabels(pushes))
	}
	first, last := pushes[0], pushes[len(pushes)-1]
	if first.EventLabel != "start" {
		t.Errorf("first push = %q, want start", first.EventLabel)
	}
	if last.EventLabel != "working" {
		t.Errorf("last push = %q, want working", last.EventLabel)
	}
	if !last.EventAt.After(first.EventAt) {
		t.Errorf("last activity %v did not advance past the start %v", last.EventAt, first.EventAt)
	}
	if last.EventCount != 1 {
		t.Errorf("EventCount = %d, want 1: progress is not a real event", last.EventCount)
	}
}

// TestActivityDetailIdlePerformerStaysAtStart (sty_5650fbcc AC3, negative): a
// performer that reports nothing after starting — no progress, only the
// probe's text-carrying "unavailable" note — keeps its start as the last
// activity, so an idle tree never looks alive.
func TestActivityDetailIdlePerformerStaysAtStart(t *testing.T) {
	r := &eventScriptRunner{steps: []scriptStep{
		{0, agentcli.Event{Kind: agentcli.EventStart}},
		{60 * time.Millisecond, agentcli.Event{Kind: agentcli.EventProgress, Text: "liveness probe unavailable on plan9"}},
		{60 * time.Millisecond, agentcli.Event{Kind: agentcli.EventHeartbeat}},
	}}
	pushes := dispatchActivity(t, r, 20*time.Millisecond)
	if got := pushLabels(pushes); len(got) != 1 || got[0] != "start" {
		t.Errorf("pushes = %v, want only the start", got)
	}
}

// TestActivityDetailToolEventNotOverwrittenByProgress (sty_5650fbcc AC3): on a
// transport that reports tool calls, the tool call is the last activity, and a
// progress tick inside the throttle window does not replace its label.
//
// time-subject: the 50ms throttle is the subject; the tool call lands 100ms
// after the start and the progress immediately after it.
func TestActivityDetailToolEventNotOverwrittenByProgress(t *testing.T) {
	r := &eventScriptRunner{steps: []scriptStep{
		{0, agentcli.Event{Kind: agentcli.EventStart}},
		{100 * time.Millisecond, agentcli.Event{Kind: agentcli.EventToolStart, Tool: "Write"}},
		{0, agentcli.Event{Kind: agentcli.EventProgress}},
	}}
	pushes := dispatchActivity(t, r, 50*time.Millisecond)
	if got := pushLabels(pushes); len(got) != 2 || got[0] != "start" || got[1] != "tool: Write" {
		t.Fatalf("pushes = %v, want [start tool: Write]", got)
	}
	if !pushes[1].EventAt.After(pushes[0].EventAt) {
		t.Errorf("tool activity %v did not advance past the start %v", pushes[1].EventAt, pushes[0].EventAt)
	}
}
