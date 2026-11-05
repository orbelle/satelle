package cli

import (
	"context"
	"encoding/json"
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

// --- caller mode ----------------------------------------------------------

// --- the hand-off, end to end --------------------------------------------

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

// --- the notification path ------------------------------------------------

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
