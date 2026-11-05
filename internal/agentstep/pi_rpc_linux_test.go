//go:build linux

package agentstep

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A dispatched pi seat over pi's RPC mode (sty_79ce4858), driven end to end
// through the engine against the fake `pi --mode rpc` peer agentcli's tests use
// (agentcli/testdata/pi_rpc_peer.py).

// piRPCBinding is an interface = "pi-rpc" binding of the fake peer, playing the
// run mode names and recording into the returned file.
func piRPCBinding(t *testing.T, mode string, env map[string]string) (config.AgentBinding, string) {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "agentcli", "testdata", "pi_rpc_peer.py"))
	if err != nil {
		t.Fatal(err)
	}
	peer := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(peer, script, 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "record.jsonl")
	full := map[string]string{"FAKE_PI_MODE": mode, "FAKE_PI_RECORD": record}
	for k, v := range env {
		full[k] = v
	}
	return config.AgentBinding{
		Role: config.RoleAgent, Interface: config.InterfacePiRPC,
		Command: strings.Replace(agentcli.PiRPCCommand, "pi ", peer+" ", 1),
		Tools:   "read,grep,find,ls", Model: "cursor/claude-opus-5-5-fast", Effort: "low",
		Env: full,
	}, record
}

// peerPid is the pid (and so the process group) the fake peer recorded.
func peerPid(t *testing.T, record string) int {
	t.Helper()
	f, err := os.Open(record)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			Pid int `json:"pid"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Pid > 0 {
			return line.Pid
		}
	}
	t.Fatal("the peer recorded no pid")
	return 0
}

// groupGone polls until no process remains in group pgid.
func groupGone(pgid int, within time.Duration) bool {
	for deadline := time.Now().Add(within); ; {
		if syscall.Kill(-pgid, 0) == syscall.ESRCH {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond) // poll tick
	}
}

// piRPCEngine is an engine whose one named agent is binding, built through the
// real runner constructor, with every telemetry row and attached artifact kept.
func piRPCEngine(t *testing.T, binding config.AgentBinding) (*Engine, *[]attemptEvent, *int) {
	t.Helper()
	wf := spineWF("", "", "", "in_progress|coder|planning-rubric", "done")
	const rubric = "---\nname: planning-rubric\ntype: skill\ndescription: test rubric\n---\nDo the work."
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: rubric, skillFound: true})
	g.repoRoot = t.TempDir() // the seat runs here, so it must exist
	g.newRunner = agentcli.RunnerFromBinding
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) { return binding, true })
	var events []attemptEvent
	g.SetTelemetry(func(_ context.Context, _, _, kind string, data map[string]any) {
		events = append(events, attemptEvent{kind: kind, data: data})
	})
	attached := 0
	g.SetArtifactAttacher(func(_ context.Context, _ workitem.Item, name, typ, _ string) (string, string, error) {
		attached++
		return name, typ, nil
	})
	return g, &events, &attached
}

// AC2: a pi seat that starts one turn and then hangs while burning CPU is ended
// by idle_timeout, well before the busy cap, and its whole group is killed. The
// pi rpc runner emits no CPU-liveness progress, so the busy cap never extends it.
//
// time-subject: the 300ms idle window is the subject; the 10s busy cap and the
// spin that never ends keep "stalled on idle" far from every other outcome.
func TestPiRPCStalledSeatEndsAtIdleTimeout(t *testing.T) {
	binding, record := piRPCBinding(t, "stall", nil)
	runner, err := agentcli.RunnerFromBinding(binding.ResolvedInterface(), binding.CommandTemplate())
	if err != nil {
		t.Fatal(err)
	}
	g := New(runner, fakeDocs{}, "/repo", "")
	req := agentcli.Request{SystemPrompt: "x", Payload: "work", Env: binding.Env, AllowedTools: binding.Tools}

	start := time.Now()
	_, _, err = g.runOnceBusy(context.Background(), runner, req, 0, 300*time.Millisecond, 10*time.Second)
	elapsed := time.Since(start)
	var se *StallError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a *StallError", err)
	}
	if se.BusyCapExceeded {
		t.Error("the stall was judged by the busy cap, want idle_timeout")
	}
	if elapsed >= 5*time.Second {
		t.Errorf("the stalled seat ran %s, want it ended near the 300ms idle_timeout and well before the 10s busy cap", elapsed)
	}
	if pgid := peerPid(t, record); !groupGone(pgid, 2*time.Second) {
		t.Errorf("process group %d outlived the stall", pgid)
	}
}

// readArgv is the argv the fake peer recorded.
func readArgv(t *testing.T, record string) []string {
	t.Helper()
	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(b), "\n", 2)[0]), &first); err != nil {
		t.Fatal(err)
	}
	return first.Argv
}
