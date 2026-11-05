package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentartifact"
)

func TestRunnerFromBinding_CommandDefault(t *testing.T) {
	r, err := RunnerFromBinding("", DefaultGrokCommand)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Name() != "grok" {
		t.Fatalf("command path runner = %v", r)
	}
	// Same as RunnerFromCommand.
	r2, err := RunnerFromCommand(DefaultGrokCommand)
	if err != nil || r2.Command() != r.Command() {
		t.Fatalf("command path divergence: %v %q vs %q", err, r.Command(), r2.Command())
	}
}

// TestRunnerFromBindingRejectsTypeSafe (sty_e3eca0b7 AC1): retired interface=
// typesafe is the same unknown-interface class as any other bogus value.
func TestRunnerFromBindingRejectsTypeSafe(t *testing.T) {
	_, errTS := RunnerFromBinding("typesafe", "https://api.typesafe.ai/v1/systemone")
	_, errBogus := RunnerFromBinding("bogus", "https://example.invalid/v1")
	if errTS == nil || errBogus == nil {
		t.Fatal("want unknown-interface errors for typesafe and bogus")
	}
	if !strings.Contains(errTS.Error(), "unknown interface") {
		t.Fatalf("typesafe err = %v", errTS)
	}
	if !strings.Contains(errBogus.Error(), "unknown interface") {
		t.Fatalf("bogus err = %v", errBogus)
	}
}

func TestRunnerFromBinding_ACPRejectsPlaceholders(t *testing.T) {
	if _, err := RunnerFromBinding(InterfaceACP, "claude-acp stdio {system}"); err == nil {
		t.Fatal("expected error for {system} in acp command")
	}
	if _, err := RunnerFromBinding(InterfaceACP, "claude-acp"); err == nil {
		t.Fatal("expected error for bare token")
	}
	if _, err := RunnerFromBinding(InterfaceACP, "grok agent stdio"); err == nil {
		t.Fatal("grok acp must be refused")
	}
	r, err := RunnerFromBinding(InterfaceACP, "claude-acp stdio")
	if err != nil {
		t.Fatal(err)
	}
	if r.Command() != "claude-acp stdio" {
		t.Errorf("Command() = %q", r.Command())
	}
}

func TestToolsAllowMutators(t *testing.T) {
	if toolsAllowMutators("read_file,grep,list_dir") {
		t.Error("read-only should not allow mutators")
	}
	if toolsAllowMutators("Read,Grep,Bash(satelle:*)") {
		t.Error("Bash(satelle:*) alone should not allow mutators")
	}
	if !toolsAllowMutators("read_file,search_replace") {
		t.Error("search_replace should allow mutators")
	}
	if !toolsAllowMutators("Bash") {
		t.Error("unrestricted Bash should allow mutators")
	}
}

func TestIsMutatorToolKind(t *testing.T) {
	if !isMutatorToolKind("edit") || !isMutatorToolKind("execute") {
		t.Error("edit/execute should be mutators")
	}
	if isMutatorToolKind("read") || isMutatorToolKind("search") {
		t.Error("read/search should not be mutators")
	}
}

// writeFakeACPPeer writes an executable Python peer that speaks a minimal ACP session.
// extra is injected at the start of session/prompt (before the final answer chunk).
// Optional final is the text of the trailing agent_message_chunk; omit for the
// default decision JSON, pass "" to emit no trailing chunk (extra alone is the
// full stream — sty_844b6ab1 capture tests use this instead of a second peer).
func writeFakeACPPeer(t *testing.T, extra string, final ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-acp-peer")
	finalText := `{"decision":"accept","notes":"ok"}`
	if len(final) > 0 {
		finalText = final[0]
	}
	finalChunk := ""
	if finalText != "" {
		// JSON-escape finalText for embedding in the Python string literal.
		esc, err := json.Marshal(finalText)
		if err != nil {
			t.Fatal(err)
		}
		finalChunk = `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + string(esc) + `}}}})
`
	}
	// Python peer: initialize, authenticate, session/new, session/prompt + optional permission.
	script := `#!/usr/bin/env python3
import json, sys

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def read():
    line = sys.stdin.readline()
    if not line:
        return None
    return json.loads(line)

while True:
    msg = read()
    if msg is None:
        break
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[{"id":"cached_token"}]}})
    elif method == "authenticate":
        send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif method == "session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"sess_test","modes":{"currentModeId":"default","availableModes":[{"id":"default"}]}}})
    elif method == "session/set_config_option":
        send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif method == "session/prompt":
` + extra + finalChunk + `
        send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif method == "session/cancel":
        pass
    else:
        if mid is not None:
            send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestACPRunner_FakePeerDecision(t *testing.T) {
	peer := writeFakeACPPeer(t, "")
	// Multi-token spawn: executable peer + dummy arg (peer ignores argv).
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	var sink bytes.Buffer
	out, err := r.Run(context.Background(), Request{
		SystemPrompt: "rubric",
		Payload:      `{"story":{"id":"sty_1"}}`,
		AllowedTools: "read_file,grep,list_dir",
		Model:        "grok-4.5",
		Sink:         &sink,
	})
	if err != nil {
		t.Fatalf("Run: %v\nsink:\n%s", err, sink.String())
	}
	if !strings.Contains(string(out), `"decision":"accept"`) {
		t.Fatalf("stdout = %q, want decision accept", out)
	}
	if sink.Len() == 0 {
		t.Error("expected Sink to receive progressive ACP lines")
	}
}

func TestACPRunner_RejectsEffortPlaceholder(t *testing.T) {
	if _, err := RunnerFromBinding(InterfaceACP, "grok agent {effort} stdio"); err == nil {
		t.Fatal("expected reject of {effort} placeholder on acp command")
	}
}

func TestACPRunner_PermissionDenyMutator(t *testing.T) {
	// Peer requests permission for edit; client must reject when tools read-only.
	extra := `
        # request permission for an edit tool before the decision chunk
        send({"jsonrpc":"2.0","id":99,"method":"session/request_permission","params":{"sessionId":"sess_test","toolCall":{"toolCallId":"c1","kind":"edit","title":"Write"},"options":[{"optionId":"allow-once","name":"Allow","kind":"allow_once"},{"optionId":"reject-once","name":"Reject","kind":"reject_once"}]}})
        # wait for client response
        resp = read()
        if resp is None or resp.get("result",{}).get("outcome",{}).get("optionId") != "reject-once":
            send({"jsonrpc":"2.0","id":mid,"error":{"code":1,"message":"expected reject-once, got %s" % (resp,)}})
            continue
`
	peer := writeFakeACPPeer(t, extra)
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), Request{
		SystemPrompt: "r",
		Payload:      "{}",
		AllowedTools: "read_file,grep,list_dir",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(out), "accept") {
		t.Fatalf("out = %q", out)
	}
}

// runACPAsk runs a one-shot dispatch against a peer that sends extra mid-turn,
// under a short deadline (a stall would hit it) and returns the output plus the
// InteractiveDenied events seen.
func runACPAsk(t *testing.T, extra string) ([]byte, []Event) {
	t.Helper()
	peer := writeFakeACPPeer(t, extra)
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var denied []Event
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := r.Run(ctx, Request{
		SystemPrompt: "r",
		Payload:      "{}",
		AllowedTools: "read_file,grep,list_dir",
		OnEvent: func(ev Event) {
			if ev.Kind == EventInteractiveDenied {
				mu.Lock()
				denied = append(denied, ev)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatalf("Run stalled or failed: %v", err)
	}
	if !strings.Contains(string(out), "accept") {
		t.Fatalf("out = %q, want the verdict", out)
	}
	mu.Lock()
	defer mu.Unlock()
	return out, append([]Event(nil), denied...)
}

func TestACPRunner_AskPermissionDenied(t *testing.T) {
	extra := `
        send({"jsonrpc":"2.0","id":98,"method":"session/request_permission","params":{"sessionId":"sess_test","toolCall":{"toolCallId":"c1","kind":"other","title":"Ask: which story?"},"options":[{"optionId":"allow-once","name":"Allow","kind":"allow_once"},{"optionId":"reject-once","name":"Reject","kind":"reject_once"}]}})
        resp = read()
        if resp is None or resp.get("result",{}).get("outcome",{}).get("optionId") != "reject-once":
            send({"jsonrpc":"2.0","id":mid,"error":{"code":1,"message":"expected reject-once, got %s" % (resp,)}})
            continue
`
	_, denied := runACPAsk(t, extra)
	if len(denied) != 1 {
		t.Fatalf("interactive_denied events = %d, want 1", len(denied))
	}
	if !strings.Contains(denied[0].Tool, "Ask") || denied[0].Text != "which story?" || denied[0].Meta[EventMetaResponse] != "denied" {
		t.Errorf("event = %+v, want tool Ask, text %q, response denied", denied[0], "which story?")
	}
}

func TestACPRunner_ElicitationAutoAnswered(t *testing.T) {
	for _, method := range []string{"session/elicitation", "_x.ai/ask_user"} {
		t.Run(method, func(t *testing.T) {
			extra := `
        send({"jsonrpc":"2.0","id":77,"method":"` + method + `","params":{"question":"pick one?"}})
        resp = read()
        if resp is None or resp.get("id") != 77 or "result" not in resp or "no user is available" not in json.dumps(resp):
            send({"jsonrpc":"2.0","id":mid,"error":{"code":1,"message":"expected auto-answer, got %s" % (resp,)}})
            continue
`
			_, denied := runACPAsk(t, extra)
			if len(denied) != 1 || denied[0].Tool != method || denied[0].Text != "pick one?" || denied[0].Meta[EventMetaResponse] != "auto-answered" {
				t.Fatalf("events = %+v, want one auto-answered %s / pick one?", denied, method)
			}
		})
	}
}

func TestACPRunner_GrokAskUserQuestionGetsOutcomeShape(t *testing.T) {
	fixture, err := os.ReadFile("testdata/grok_ask_user_question.request.json")
	if err != nil {
		t.Fatal(err)
	}
	extra := "\n        send(json.loads('''" + strings.TrimSpace(string(fixture)) + `'''))
        resp = read()
        want = {"outcome": "accepted", "answers": {"Which colour should I pick, red or blue?": "no user is available; decide from the payload and state your assumption"}}
        if resp is None or resp.get("id") != 0 or resp.get("result") != want:
            send({"jsonrpc":"2.0","id":mid,"error":{"code":1,"message":"expected grok outcome shape, got %s" % (resp,)}})
            continue
`
	_, denied := runACPAsk(t, extra)
	if len(denied) != 1 || denied[0].Tool != "_x.ai/ask_user_question" || denied[0].Text != "Which colour should I pick, red or blue?" || denied[0].Meta[EventMetaResponse] != "auto-answered" {
		t.Fatalf("events = %+v, want one auto-answered grok ask", denied)
	}
}

func TestACPRunner_TraceRecordsPeerRequests(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv(acpTraceEnv, trace)
	extra := `
        send({"jsonrpc":"2.0","id":77,"method":"_x.ai/ask_user","params":{"question":"pick one?"}})
        read()
`
	runACPAsk(t, extra)
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("trace not written: %v", err)
	}
	if s := string(b); !strings.Contains(s, `"method":"_x.ai/ask_user"`) || !strings.Contains(s, `"question":"pick one?"`) {
		t.Errorf("trace = %q, want the ask method and params", s)
	}
}

func TestACPRunner_UnknownRequestGetsMethodNotFound(t *testing.T) {
	extra := `
        send({"jsonrpc":"2.0","id":55,"method":"fs/unheard_of","params":{}})
        resp = read()
        if resp is None or resp.get("id") != 55 or resp.get("error",{}).get("code") != -32601:
            send({"jsonrpc":"2.0","id":mid,"error":{"code":1,"message":"expected -32601, got %s" % (resp,)}})
            continue
`
	_, denied := runACPAsk(t, extra)
	if len(denied) != 0 {
		t.Errorf("non-ask unknown method emitted %d interactive_denied events", len(denied))
	}
}

func TestACPRunner_TimeoutKills(t *testing.T) {
	// Peer blocks forever on prompt — context cancel must return.
	dir := t.TempDir()
	path := filepath.Join(dir, "hang-peer")
	script := `#!/usr/bin/env python3
import json, sys, time
def send(obj):
    sys.stdout.write(json.dumps(obj)+"\n"); sys.stdout.flush()
def read():
    line=sys.stdin.readline()
    return json.loads(line) if line else None
while True:
    msg=read()
    if not msg: break
    mid, method = msg.get("id"), msg.get("method")
    if method=="initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"authMethods":[]}})
    elif method=="session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"s"}})
    elif method=="session/prompt":
        time.sleep(60)
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := RunnerFromBinding(InterfaceACP, path+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	// time-subject: the deadline is what is under test — a silent peer must trip it
	// as a timeout error. A slow start only makes it trip earlier; any error passes.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = r.Run(ctx, Request{SystemPrompt: "x", Payload: "{}"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// sty_844b6ab1 — ACP capture segments on tool_call fences and keeps the last
// agent_message run (CaptureAnswer default). Tests extend writeFakeACPPeer:
// extra = pre-final updates; optional final overrides/suppresses the trailing
// answer chunk ("" = extra alone is the whole stream).

// TestACPCapture_DropsNarrationFencedByToolCall (AC3): narration chunks, then
// tool_call / tool_call_update, then the default decision answer.
// CaptureAnswer (default) must return only the answer.
func TestACPCapture_DropsNarrationFencedByToolCall(t *testing.T) {
	extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"I'll reconstruct the story context"}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":" and then summarise."}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"list_dir","kind":"read"}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed"}}})
`
	peer := writeFakeACPPeer(t, extra)
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), Request{
		SystemPrompt: "rubric",
		Payload:      `{}`,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "I'll reconstruct") || strings.Contains(got, "then summarise") {
		t.Fatalf("narration leaked into captured output: %q", got)
	}
	if !strings.Contains(got, `"decision":"accept"`) {
		t.Fatalf("answer missing from captured output: %q", got)
	}
	if strings.Contains(got, "summarise.{\"decision\"") || strings.Contains(got, "summarise.{") {
		t.Fatalf("narration was concatenated onto answer: %q", got)
	}
}

// TestACPCapture_SingleUnfencedRun (AC5): multi-chunk answer with no tool fence
// must be returned in full — never last-chunk truncation.
func TestACPCapture_SingleUnfencedRun(t *testing.T) {
	extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Moved from "}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"backlog to plan "}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"after intent review."}}}})
`
	peer := writeFakeACPPeer(t, extra, "") // no trailing decision — extra is the full stream
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), Request{SystemPrompt: "x", Payload: "{}"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "Moved from backlog to plan after intent review."
	if string(out) != want {
		t.Fatalf("unfenced multi-chunk capture = %q, want %q", out, want)
	}
}

// TestACPCapture_FullKeepsVerdictBeforeTrailingChatter (AC6 half): with
// CaptureFull, decision JSON before a tool fence + trailing "Done." all stay
// in the blob so parseDecision can still find the verdict.
func TestACPCapture_FullKeepsVerdictBeforeTrailingChatter(t *testing.T) {
	extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"{\"decision\":\"accept\",\"notes\":\"ok\"}"}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"noop","kind":"other"}}})
`
	peer := writeFakeACPPeer(t, extra, "Done.")
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	full, err := r.Run(context.Background(), Request{
		SystemPrompt: "x", Payload: "{}", Capture: CaptureFull,
	})
	if err != nil {
		t.Fatalf("CaptureFull Run: %v", err)
	}
	if !strings.Contains(string(full), `"decision":"accept"`) {
		t.Fatalf("CaptureFull dropped decision: %q", full)
	}
	if !strings.Contains(string(full), "Done.") {
		t.Fatalf("CaptureFull dropped trailing chatter: %q", full)
	}
	answer, err := r.Run(context.Background(), Request{
		SystemPrompt: "x", Payload: "{}", Capture: CaptureAnswer,
	})
	if err != nil {
		t.Fatalf("CaptureAnswer Run: %v", err)
	}
	if strings.Contains(string(answer), `"decision":"accept"`) {
		t.Fatalf("CaptureAnswer unexpectedly kept earlier decision segment: %q", answer)
	}
	if string(answer) != "Done." {
		t.Fatalf("CaptureAnswer = %q, want Done.", answer)
	}
}

// TestACPCapture_ExcludesThoughtChunks (AC7): thought text must not appear in
// the captured output, and must not segment an answer run.
func TestACPCapture_ExcludesThoughtChunks(t *testing.T) {
	extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"SECRET_REASONING_BEFORE"}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Moved from "}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"SECRET_REASONING_MID"}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"backlog to plan."}}}})
`
	peer := writeFakeACPPeer(t, extra, "")
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), Request{SystemPrompt: "x", Payload: "{}"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "SECRET_REASONING") {
		t.Fatalf("agent_thought_chunk leaked into capture: %q", got)
	}
	if got != "Moved from backlog to plan." {
		t.Fatalf("thought segmented or truncated answer: %q", got)
	}
}

func TestACPEmitsNormalizedEventsAndFiltersThoughtsFromRawTrace(t *testing.T) {
	extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"SECRET_REASONING"}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Working"}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"read_file","kind":"read"}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed"}}})
`
	peer := writeFakeACPPeer(t, extra)
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	var events []Event
	_, err = r.Run(context.Background(), Request{
		SystemPrompt: "x",
		Payload:      "{}",
		Sink:         &raw,
		OnEvent:      func(ev Event) { events = append(events, ev) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw.String(), "SECRET_REASONING") {
		t.Fatalf("thought leaked into raw trace: %s", raw.String())
	}
	var kinds []EventKind
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
		if strings.Contains(ev.Text, "SECRET_REASONING") {
			t.Fatalf("thought leaked into normalized event: %#v", ev)
		}
	}
	for _, want := range []EventKind{EventStart, EventMessage, EventToolStart, EventToolEnd, EventCompleted} {
		found := false
		for _, got := range kinds {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %s in %v", want, kinds)
		}
	}
}

func TestACPFinalResponseUsesStructuredArtifactDecoder(t *testing.T) {
	final := `{"artifact":{"name":"notes","type":"design","body":"## AC1\ncovered"}}`
	peer := writeFakeACPPeer(t, "", final)
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), Request{SystemPrompt: "x", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := agentartifact.Decode(out)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "notes" || a.Type != "design" || !strings.Contains(a.Body, "AC1") {
		t.Fatalf("artifact = %#v", a)
	}
}

// TestACPCapture_InterleavedToolFenced (AC8 fenced limb): the solidsafe-shaped
// pattern — narration, tool fence, answer — keeps only the answer.
func TestACPCapture_InterleavedToolFenced(t *testing.T) {
	extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"I'll pull the story record so the step summary reflects what actually moved."}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"read_file","kind":"read"}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed"}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Checking"}}}})
`
	// Post-tool "Checking" + default decision form one unfenced answer segment.
	peer := writeFakeACPPeer(t, extra)
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), Request{SystemPrompt: "x", Payload: "{}"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "I'll pull the story record") {
		t.Fatalf("pre-tool narration leaked: %q", got)
	}
	if !strings.Contains(got, "Checking") || !strings.Contains(got, `"decision":"accept"`) {
		t.Fatalf("post-tool answer incomplete: %q", got)
	}
}

func TestACPRunner_ModelSetConfigRejected(t *testing.T) {
	// Peer rejects model set_config_option.
	script := `#!/usr/bin/env python3
import sys, json
def send(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
while True:
    line = sys.stdin.readline()
    if not line:
        break
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[{"id":"cached_token"}]}})
    elif method == "authenticate":
        send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif method == "session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"sess_test"}})
    elif method == "session/set_config_option":
        params = msg.get("params") or {}
        if params.get("configId") == "model":
            send({"jsonrpc":"2.0","id":mid,"error":{"code":-32000,"message":"unknown model"}})
        else:
            send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif method == "session/prompt":
        send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	dir := t.TempDir()
	path := dir + "/peer.py"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := RunnerFromBinding(InterfaceACP, path+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), Request{
		SystemPrompt: "rubric",
		Payload:      `{}`,
		Model:        "opus",
	})
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("want model set_config_option error, got %v", err)
	}
}

func TestACPSession_SecondTurn(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "mark.txt")
	path := filepath.Join(dir, "fake-acp-turns")
	script := `#!/usr/bin/env python3
import json, os, sys

mark = os.environ.get("ACP_MARK")
if mark:
    with open(mark, "a") as f:
        f.write("start\n")

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def read():
    line = sys.stdin.readline()
    if not line:
        return None
    return json.loads(line)

n = 0
while True:
    msg = read()
    if msg is None:
        break
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}})
    elif method == "session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"sess_test"}})
    elif method == "session/set_config_option":
        send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif method == "session/prompt":
        n += 1
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"acp-turn-%d" % n}}}})
        send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif method == "session/cancel":
        pass
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := newACPRunner(path + " stdio")
	if err != nil {
		t.Fatal(err)
	}
	ar := r.(acpRunner)
	sess, err := openACPSession(context.Background(), ar, Request{
		AllowedTools: "read_file,grep,list_dir",
		Env:          map[string]string{"ACP_MARK": mark},
	}, defaultPermissionPolicy(false))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Send(context.Background(), Turn{System: "sys", Text: "one"}); err != nil {
		t.Fatalf("send 1: %v", err)
	}
	got1 := waitEventText(t, sess, "acp-turn-1")
	if err := sess.Send(context.Background(), Turn{Text: "two"}); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	got2 := waitEventText(t, sess, "acp-turn-2")
	if got1 != "acp-turn-1" || got2 != "acp-turn-2" {
		t.Fatalf("texts = %q %q", got1, got2)
	}
	b, err := os.ReadFile(mark)
	if err != nil {
		t.Fatalf("mark: %v", err)
	}
	if n := strings.Count(string(b), "start"); n != 1 {
		t.Fatalf("process started %d times, want 1", n)
	}
}
