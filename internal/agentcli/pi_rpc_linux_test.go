//go:build linux

package agentcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeFakePiRPCPeer copies testdata/pi_rpc_peer.py, the stand-in for
// `pi --mode rpc`, in as an executable named pi (adapterOf recognises pi by its
// binary name) and returns its path.
func writeFakePiRPCPeer(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("testdata", "pi_rpc_peer.py"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(path, script, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// piRPCPeerRecord is what one fake peer process recorded.
type piRPCPeerRecord struct {
	pid      int
	argv     []string
	in       []map[string]any
	abortAt  int
	startups int
}

func readPiRPCPeerRecord(t *testing.T, path string) piRPCPeerRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rec piRPCPeerRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			Pid     int            `json:"pid"`
			Argv    []string       `json:"argv"`
			In      map[string]any `json:"in"`
			AbortAt int            `json:"abort_after_turn"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		switch {
		case line.Argv != nil:
			rec.pid, rec.argv = line.Pid, line.Argv
			rec.startups++
		case line.In != nil:
			rec.in = append(rec.in, line.In)
		case line.AbortAt > 0:
			rec.abortAt = line.AbortAt
		}
	}
	return rec
}

// piRPCDispatch builds a pi-rpc runner for the fake peer through
// RunnerFromBinding, the constructor dispatch uses, and returns it with the
// request that drives it.
func piRPCDispatch(t *testing.T, mode string, env map[string]string) (Runner, Request, string) {
	t.Helper()
	peer := writeFakePiRPCPeer(t)
	record := filepath.Join(t.TempDir(), "record.jsonl")
	r, err := RunnerFromBinding(InterfacePiRPC, strings.Replace(PiRPCCommand, "pi ", peer+" ", 1))
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]string{"FAKE_PI_MODE": mode, "FAKE_PI_RECORD": record}
	for k, v := range env {
		full[k] = v
	}
	req := Request{
		SystemPrompt: "SYSTEM RUBRIC\nline two", Payload: `{"id":"sty_x","title":"work"}`,
		AllowedTools: "read,grep,find,ls", Model: "cursor/claude-opus-5-5-fast", Effort: "low",
		Env: full, HeartbeatInterval: -1,
	}
	return r, req, record
}

// AC1: the seat ends as soon as pi settles after its answer — the answer comes
// back, stdin is closed, and the whole process group is killed even though the
// peer ignores EOF and left a grandchild behind.
func TestPiRPCSeatEndsAtSettleAndKillsItsGroup(t *testing.T) {
	r, req, record := piRPCDispatch(t, "answer", map[string]string{"FAKE_PI_LINGER": "1"})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	out, err := r.Run(ctx, req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "the answer" {
		t.Errorf("out = %q, want the answer", out)
	}
	if elapsed >= 5*time.Second {
		t.Errorf("the seat took %s to end after settling, want under 5s of a 60s deadline", elapsed)
	}
	pgid := readPiRPCPeerRecord(t, record).pid
	if !waitGroupGone(pgid, time.Second) || groupMembers(pgid) != 0 {
		t.Errorf("process group %d still has %d live members", pgid, groupMembers(pgid))
	}
}

// AC2: every turn, message and tool call reaches satelle as a real event, and
// the RPC path emits no CPU-liveness progress even when asked for it.
func TestPiRPCEventsReachSatelle(t *testing.T) {
	r, req, _ := piRPCDispatch(t, "answer", nil)
	var events []Event
	req.OnEvent = func(ev Event) { events = append(events, ev) }
	req.LivenessInterval = 50 * time.Millisecond
	if _, err := r.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range events {
		if ev.Kind == EventProgress {
			t.Fatal("the pi rpc path emitted a CPU-liveness progress event")
		}
		k := string(ev.Kind)
		if ev.Tool != "" {
			k += ":" + ev.Tool
		}
		if ev.Text != "" {
			k += ":" + ev.Text
		}
		kinds = append(kinds, k)
	}
	want := []string{"start", "message:turn 1", "message:looking", "tool_start:grep", "tool_end:grep", "message:turn 2", "message", "usage", "completed"}
	if !slices.Equal(kinds, want) {
		t.Errorf("events = %v, want %v", kinds, want)
	}
}

// AC4: the run's own stats and events are its usage, with --no-session and no
// pi session record anywhere.
func TestPiRPCUsageFromTheRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r, req, _ := piRPCDispatch(t, "answer", map[string]string{"FAKE_PI_PROVIDER": "cursor", "FAKE_PI_MODEL": "claude-opus-5-5-fast"})
	_, u, err := r.(UsageRunner).RunUsage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Available || u.FreshInputTokens != 9926 || u.OutputTokens != 450 || u.CacheReadInputTokens != 10446 || u.CacheCreationInputTokens != 74 {
		t.Errorf("tokens = %+v", u)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.399 || !u.TurnsAvailable || u.Turns != 2 || u.ModelResolved != "cursor/claude-opus-5-5-fast" {
		t.Errorf("cost/turns/model = %v/%d/%q", u.CostUSD, u.Turns, u.ModelResolved)
	}
	if u.Adapter != HarnessPi || u.UnavailableReason != "" {
		t.Errorf("adapter=%q reason=%q", u.Adapter, u.UnavailableReason)
	}
}

// AC5: two pi-rpc bindings with their own model and effort each spawn pi with
// their own --model and --thinking, through buildArgs as the pi command does.
func TestPiRPCBindingsKeepTheirOwnModelAndEffort(t *testing.T) {
	for _, c := range []struct{ model, effort string }{
		{"cursor/claude-opus-5-5-fast", "low"},
		{"cursor/grok-4.7-fast", "high"},
	} {
		r, req, record := piRPCDispatch(t, "answer", nil)
		req.Model, req.Effort = c.model, c.effort
		if _, err := r.Run(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		argv := readPiRPCPeerRecord(t, record).argv
		if want := buildArgs(r.(piRPCRunner).args, req); !slices.Equal(argv, want) {
			t.Errorf("argv = %q, want buildArgs' %q", argv, want)
		}
		if v, _ := flagValue(argv, "--model"); v != c.model {
			t.Errorf("--model = %q, want %q", v, c.model)
		}
		if v, _ := flagValue(argv, "--thinking"); v != c.effort {
			t.Errorf("--thinking = %q, want %q", v, c.effort)
		}
	}
}

// AC6: each dispatch is a fresh, cold pi process: the cold flags, the system
// prompt as a startup flag, and exactly one prompt command carrying the payload.
func TestPiRPCEveryDispatchStartsCold(t *testing.T) {
	var pids []int
	for range 2 {
		r, req, record := piRPCDispatch(t, "answer", nil)
		if _, err := r.Run(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		rec := readPiRPCPeerRecord(t, record)
		pids = append(pids, rec.pid)
		if rec.startups != 1 {
			t.Errorf("one dispatch started %d processes", rec.startups)
		}
		for _, f := range append([]string{"--mode"}, piRPCColdFlags...) {
			if !slices.Contains(rec.argv, f) {
				t.Errorf("argv %q lacks %s", rec.argv, f)
			}
		}
		if v, _ := flagValue(rec.argv, "--mode"); v != "rpc" {
			t.Errorf("--mode = %q", v)
		}
		if v, _ := flagValue(rec.argv, "--system-prompt"); v != req.SystemPrompt {
			t.Errorf("--system-prompt = %q, want the system prompt", v)
		}
		var prompts []map[string]any
		for _, in := range rec.in {
			if in["type"] == "prompt" {
				prompts = append(prompts, in)
			}
		}
		if len(prompts) != 1 || prompts[0]["message"] != req.Payload {
			t.Errorf("prompt commands = %v, want exactly one carrying the payload", prompts)
		}
	}
	if pids[0] == pids[1] {
		t.Errorf("two dispatches shared pid %d", pids[0])
	}
}

// AC7: a dialog request is answered with cancelled under its id, after which the
// run completes.
func TestPiRPCDialogDoesNotHoldTheSeat(t *testing.T) {
	r, req, record := piRPCDispatch(t, "dialog", nil)
	var denied []Event
	req.OnEvent = func(ev Event) {
		if ev.Kind == EventInteractiveDenied {
			denied = append(denied, ev)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := r.Run(ctx, req)
	if err != nil || string(out) != "the answer" {
		t.Fatalf("out=%q err=%v, want the run to complete", out, err)
	}
	var replies []map[string]any
	for _, in := range readPiRPCPeerRecord(t, record).in {
		if in["type"] == "extension_ui_response" {
			replies = append(replies, in)
		}
	}
	if len(replies) != 1 || replies[0]["id"] != "ui-2" || replies[0]["cancelled"] != true {
		t.Errorf("dialog replies = %v, want one cancelled reply to ui-2", replies)
	}
	if len(denied) != 1 {
		t.Errorf("interactive-denied events = %d, want 1", len(denied))
	}
}

// A context that ends mid-run kills the seat's group at once.
func TestPiRPCContextEndKillsTheGroup(t *testing.T) {
	r, req, record := piRPCDispatch(t, "turns", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := r.Run(ctx, req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	pgid := readPiRPCPeerRecord(t, record).pid
	if !waitGroupGone(pgid, time.Second) {
		t.Errorf("process group %d outlived the deadline", pgid)
	}
}
