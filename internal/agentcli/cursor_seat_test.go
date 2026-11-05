package agentcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These tests drive the cursor-agent dispatch seat (sty_10c52ab3) against a fake
// executable named cursor-agent, so the adapter is chosen exactly as in
// production (by the binary's name) and no real model is called. The captured
// real runs under testdata/cursor are the evidence the fakes imitate.

// writeFakeCursor writes a cursor-agent stand-in that records its argv and stdin
// under $OUT, then prints $FIXTURE on stdout (or $FAIL on stderr with exit 1).
func writeFakeCursor(t *testing.T) (bin, out string) {
	t.Helper()
	dir := t.TempDir()
	out = filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "cursor-agent")
	script := `#!/bin/sh
touch "$OUT/ran"
printf '%s\n' "$@" > "$OUT/argv"
cat > "$OUT/stdin"
if [ -n "$FAIL" ]; then cat "$FAIL" >&2; exit 1; fi
if [ -n "$FIXTURE" ]; then cat "$FIXTURE"; fi
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, out
}

func cursorFixturePath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(cursorDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCursorAdapterIsDecidedOnTheBinary(t *testing.T) {
	for command, want := range map[string]string{
		"cursor-agent -p":                         HarnessCursor,
		"/home/u/.local/bin/cursor-agent -p":      HarnessCursor,
		"cursor-agent-beta -p":                    HarnessCursor,
		"cursor-agent -p --model grok-4.7-high":   HarnessCursor, // the model is not the adapter
		"cursor-agent -p --model claude-opus-5-5": HarnessCursor,
		"cursor -p":                   HarnessUnknown,
		"agent -p":                    HarnessUnknown,
		"mybot --engine cursor-agent": HarnessUnknown,
		"/opt/grok-tools/cursor-agent -p --x grok": HarnessCursor,
	} {
		if got := AdapterName(command); got != want {
			t.Errorf("AdapterName(%q) = %q, want %q", command, got, want)
		}
	}
}

// AC1/AC2: a performer rendered from CursorCommand carries no positional prompt
// and no {system} token, and both the instructions and the work item reach the
// run on stdin; nothing is written into the worktree.
func TestCursorCommandRendersArgvAndStdin(t *testing.T) {
	bin, out := writeFakeCursor(t)
	work := t.TempDir()
	r, err := RunnerFromCommand(bin + strings.TrimPrefix(CursorCommand, "cursor-agent"))
	if err != nil {
		t.Fatalf("CursorCommand must be a valid template: %v", err)
	}
	stdout, err := r.Run(context.Background(), Request{
		SystemPrompt: "RUBRIC",
		Payload:      `{"story":"sty_1"}`,
		Model:        "composer-2.5",
		Dir:          work,
		Env:          map[string]string{"OUT": out, "FIXTURE": cursorFixturePath(t, "1a-json.out")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantArgv := "-p\n--trust\n--output-format\njson\n--model\ncomposer-2.5\n"
	if got := readFile(t, filepath.Join(out, "argv")); got != wantArgv {
		t.Errorf("argv = %q, want %q (no positional prompt)", got, wantArgv)
	}
	wantStdin := "RUBRIC\n\n---\n\n" + `{"story":"sty_1"}`
	if got := readFile(t, filepath.Join(out, "stdin")); got != wantStdin {
		t.Errorf("stdin = %q, want %q", got, wantStdin)
	}
	if ents, _ := os.ReadDir(work); len(ents) != 0 {
		t.Errorf("worktree must stay untouched, found %d entries", len(ents))
	}

	text, usage := UnwrapUsage(stdout)
	if string(text) != "PONG" {
		t.Errorf("decision text = %q, want .result PONG", text)
	}
	if !usage.Available || usage.InputTokens != 12678 || usage.FreshInputTokens != 8814 || usage.OutputTokens != 32 {
		t.Errorf("cursor usage must be the envelope's reported figures: %+v", usage)
	}
}

func TestCursorStdinWithoutInstructionsIsThePayload(t *testing.T) {
	if got := cursorStdin(Request{Payload: "P"}); got != "P" {
		t.Errorf("stdin = %q, want the bare payload", got)
	}
}

// AC2: a positional prompt suppresses stdin (fixture 8b), so both prompt tokens are
// refused by the constructor, each with a reason that names cursor.
func TestCursorTemplateRefusesSystemAndPayloadTokens(t *testing.T) {
	for _, tok := range []string{"{system}", "{payload}"} {
		t.Run(tok, func(t *testing.T) {
			_, err := RunnerFromCommand("cursor-agent -p --trust --output-format json " + tok)
			if err == nil {
				t.Fatalf("%s must be refused", tok)
			}
			msg := err.Error()
			if !strings.Contains(msg, "cursor") || !strings.Contains(msg, tok) || !strings.Contains(msg, "stdin") {
				t.Errorf("refusal must name cursor, %s and stdin: %q", tok, msg)
			}
		})
	}
}

// AC5: only --output-format json is read by the command transport.
func TestCursorTemplateOutputFormat(t *testing.T) {
	for name, command := range map[string]string{
		"stream-json":          "cursor-agent -p --output-format stream-json --model {model}",
		"text":                 "cursor-agent -p --output-format text --model {model}",
		"missing":              "cursor-agent -p --model {model}",
		"fused-text":           "cursor-agent -p --output-format=text",
		"repeated-stream-json": "cursor-agent -p --output-format json --output-format stream-json",
		"repeated-fused-text":  "cursor-agent -p --output-format json --output-format=text",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RunnerFromCommand(command)
			if err == nil {
				t.Fatalf("%q must be refused", command)
			}
			if !strings.Contains(err.Error(), "cursor") || !strings.Contains(err.Error(), "--output-format json") {
				t.Errorf("refusal must name cursor and the json format: %q", err)
			}
		})
	}
	if _, err := RunnerFromCommand("cursor-agent -p --output-format=json"); err != nil {
		t.Errorf("--output-format=json is the json format: %v", err)
	}
}

// AC5: the stream and cloud transports do not fit cursor.
func TestCursorTransportRefusals(t *testing.T) {
	_, err := RunnerFromBinding(InterfaceStream, "cursor-agent -p --input-format stream-json --output-format stream-json")
	if err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Errorf("stream binding must be refused with a cursor-named reason: %v", err)
	}
	if _, err := OpenerFromBinding(InterfaceStream, "cursor-agent -p --output-format stream-json"); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Errorf("stream opener must be refused with a cursor-named reason: %v", err)
	}
	if err := CloudLaunchAvailable(HarnessCursor); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Errorf("cloud launch must be unavailable for cursor by name: %v", err)
	}
	if _, err := RunnerFromBinding(InterfaceACP, "cursor-agent acp"); err != nil {
		t.Errorf("cursor acp is offered: %v", err)
	}
	if _, err := RunnerFromBinding(InterfaceACP, "cursor-agent -p --output-format json"); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Errorf("a one-shot cursor line is not an ACP spawn: %v", err)
	}
}

// AC3: the json envelope is cursor's, never claude's.
func TestCursorEnvelopeIsUnwrappedAsCursor(t *testing.T) {
	text, u := UnwrapUsage([]byte(cursorRead(t, "1a-json.out")))
	if string(text) != "PONG" {
		t.Errorf("decision text = %q, want PONG", text)
	}
	if !u.Available || u.InputTokens != 12678 || u.FreshInputTokens != 8814 {
		t.Errorf("usage must be cursor's camelCase figures, not a zero claude read: %+v", u)
	}
	if !strings.Contains(u.ModelResolved, "cursor") || !IsModelUnavailable(u.ModelResolved) {
		t.Errorf("ModelResolved = %q, want a cursor-named no-model marker", u.ModelResolved)
	}

	// A claude envelope is still claude's (it has neither request_id nor camelCase usage).
	claude, cu := UnwrapUsage([]byte(`{"type":"result","subtype":"success","result":"OK","usage":{"input_tokens":3,"output_tokens":4}}`))
	if string(claude) != "OK" || !cu.Available || cu.InputTokens != 3 || cu.OutputTokens != 4 {
		t.Errorf("claude envelope regressed: %q %+v", claude, cu)
	}
}

// AC3: cursor's failure is plain text on stderr with exit 1 (fixture 1g); it
// surfaces as a dispatch error that quotes the text.
func TestCursorFailureSurfacesAsDispatchError(t *testing.T) {
	bin, out := writeFakeCursor(t)
	r, err := RunnerFromCommand(bin + " -p --trust --output-format json --model {model}")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), Request{
		SystemPrompt: "S", Payload: "P", Model: "no-such-model",
		Env: map[string]string{"OUT": out, "FAIL": cursorFixturePath(t, "1g-fail.err")},
	})
	if err == nil {
		t.Fatal("exit 1 must be a dispatch error")
	}
	if !strings.Contains(err.Error(), "Cannot use this model: no-such-model") {
		t.Errorf("error must quote cursor's text: %.200s", err)
	}
}

// AC4: a read-only cursor command spawn is held to ask mode.
func TestCursorReviewerSpawnReadOnly(t *testing.T) {
	run := func(t *testing.T, template string, readOnly bool) (out string, err error) {
		t.Helper()
		bin, out := writeFakeCursor(t)
		r, rerr := RunnerFromCommand(bin + " " + template)
		if rerr != nil {
			t.Fatalf("template %q: %v", template, rerr)
		}
		_, err = r.Run(context.Background(), Request{
			SystemPrompt: "S", Payload: "P", ReadOnly: readOnly,
			Env: map[string]string{"OUT": out, "FIXTURE": cursorFixturePath(t, "1a-json.out")},
		})
		return out, err
	}
	argv := func(out string) []string {
		return strings.Fields(readFile(t, filepath.Join(out, "argv")))
	}

	t.Run("mode appended when the template names none", func(t *testing.T) {
		out, err := run(t, "-p --output-format json", true)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(argv(out), " "); got != "-p --output-format json --mode ask" {
			t.Errorf("argv = %q, want --mode ask appended", got)
		}
	})
	for _, mode := range []string{"ask"} {
		t.Run("mode "+mode+" kept", func(t *testing.T) {
			out, err := run(t, "-p --output-format json --mode "+mode, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(argv(out), " "); got != "-p --output-format json --mode "+mode {
				t.Errorf("argv = %q, want the template's own mode kept", got)
			}
		})
	}
	t.Run("a mode other than ask is refused and nothing starts", func(t *testing.T) {
		for tmpl, mode := range map[string]string{
			"-p --output-format json --mode agent": "agent",
			"-p --output-format json --mode=agent": "agent",
			"-p --output-format json --mode plan":  "plan",
			// cursor honours the last --mode, so a later one must not slip past an earlier ask.
			"-p --output-format json --mode ask --mode agent": "agent",
			"-p --output-format json --mode ask --mode=agent": "agent",
			"-p --output-format json --mode=agent --mode ask": "agent",
		} {
			bin, out := writeFakeCursor(t)
			r, err := RunnerFromCommand(bin + " " + tmpl)
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.Run(context.Background(), Request{ReadOnly: true, Env: map[string]string{"OUT": out}})
			if err == nil || !strings.Contains(err.Error(), "cursor") || !strings.Contains(err.Error(), "--mode "+mode) {
				t.Errorf("%q: want a cursor-named refusal of --mode %s, got %v", tmpl, mode, err)
			}
			if _, statErr := os.Stat(filepath.Join(out, "ran")); statErr == nil {
				t.Errorf("%q: the process must not start", tmpl)
			}
		}
	})
	t.Run("a performer is left alone", func(t *testing.T) {
		out, err := run(t, "-p --output-format json --mode agent", false)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(argv(out), " "); strings.Contains(got, "ask") {
			t.Errorf("a performer must not be forced read-only: %q", got)
		}
	})
}

// AC4/AC6: validation asks agentcli whether the ceiling holds.
func TestCursorReadOnlyEnforced(t *testing.T) {
	cases := []struct {
		name    string
		adapter string
		args    []string
		iface   string
		want    bool
		wantErr string
	}{
		{"command no mode", HarnessCursor, []string{"-p", "--output-format", "json"}, InterfaceCommand, true, ""},
		{"command plan", HarnessCursor, []string{"--mode", "plan"}, InterfaceCommand, false, "--mode plan"},
		{"command ask", HarnessCursor, []string{"--mode=ask"}, "", true, ""},
		{"command ask then agent", HarnessCursor, []string{"--mode", "ask", "--mode", "agent"}, InterfaceCommand, false, "--mode agent"},
		{"command ask then fused agent", HarnessCursor, []string{"--mode", "ask", "--mode=agent"}, InterfaceCommand, false, "--mode agent"},
		{"command agent", HarnessCursor, []string{"--mode", "agent"}, InterfaceCommand, false, "--mode agent"},
		{"acp", HarnessCursor, []string{"acp"}, InterfaceACP, true, ""},
		{"stream", HarnessCursor, nil, InterfaceStream, false, ""},
		{"claude", HarnessClaude, nil, InterfaceCommand, false, ""},
		{"grok acp", HarnessGrok, nil, InterfaceACP, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadOnlyEnforced(tc.adapter, tc.args, tc.iface)
			if got != tc.want {
				t.Errorf("enforced = %v, want %v", got, tc.want)
			}
			if tc.wantErr == "" && err != nil {
				t.Errorf("unexpected error %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), "cursor") || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("want a cursor-named error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// The grant descriptions say what is true of cursor: the tool grant is advisory
// and the mode is the ceiling.
func TestCursorDescriptionsAndPreflight(t *testing.T) {
	r, err := RunnerFromCommand("cursor-agent -p --output-format json")
	if err != nil {
		t.Fatal(err)
	}
	iso := DescribeReviewer(r, Request{ReadOnly: true, AllowedTools: "Read"})
	if iso.Adapter != "cursor command" || iso.OfferedTools != nil ||
		!strings.Contains(iso.OfferedSource, "no --tools allow-list") || !strings.Contains(iso.OfferedSource, "--mode ask") {
		t.Errorf("DescribeReviewer = %+v", iso)
	}
	if gaps := PreflightRunner(r, "Read"); len(gaps) != 0 {
		t.Errorf("a cursor reviewer held by ask mode has no isolation gap: %+v", gaps)
	}

	bad, err := RunnerFromCommand("cursor-agent -p --output-format json --mode agent")
	if err != nil {
		t.Fatal(err)
	}
	gaps := PreflightRunner(bad, "Read")
	if len(gaps) != 1 || !strings.Contains(gaps[0].What, "cursor") || !strings.Contains(gaps[0].What, "--mode agent") {
		t.Errorf("a writing mode must be a cursor-named gap: %+v", gaps)
	}

	acp, err := RunnerFromBinding(InterfaceACP, "cursor-agent acp")
	if err != nil {
		t.Fatal(err)
	}
	if iso := DescribeReviewer(acp, Request{ReadOnly: true}); iso.Adapter != "cursor acp" ||
		!strings.Contains(iso.OfferedSource, "session/set_mode ask") {
		t.Errorf("DescribeReviewer(acp) = %+v", iso)
	}
	if gaps := PreflightRunner(acp, ""); len(gaps) != 0 {
		t.Errorf("a cursor ACP reviewer is held by set_mode: %+v", gaps)
	}

	if !strings.Contains(ToolsGrantNote(HarnessCursor), "advisory") || ToolsGrantNote(HarnessClaude) != "" {
		t.Error("only cursor's tools grant is advisory")
	}
	if !strings.Contains(ReadOnlyCeilingNote(HarnessCursor), "ask mode") || ReadOnlyCeilingNote(HarnessGrok) != "" {
		t.Error("only cursor's ceiling is a forced mode")
	}
	if SystemDelivery(HarnessCursor) != SystemOnStdin || SystemDelivery(HarnessClaude) != SystemOnArgv {
		t.Error("only cursor takes its instructions on stdin")
	}
	if !ReadsMaterialByPath(HarnessCursor) || ReadsMaterialByPath(HarnessGrok) {
		t.Error("only cursor reads satelle's material by path without a grant")
	}
}

// writeFakeCursorACP writes a cursor-agent that speaks ACP. It logs every request
// (method and params) to $PEERLOG, answers session/set_mode with an error when
// $FAIL_SET_MODE is set, and on session/prompt reports one read tool call and a
// decision.
func writeFakeCursorACP(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "peer.log")
	bin = filepath.Join(dir, "cursor-agent")
	script := `#!/usr/bin/env python3
import json, os, sys

log = open(os.environ["PEERLOG"], "a")

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

for line in sys.stdin:
    msg = json.loads(line)
    method, mid = msg.get("method"), msg.get("id")
    if method is None:
        continue
    log.write(json.dumps({"method": method, "params": msg.get("params")}) + "\n")
    log.flush()
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1, "agentCapabilities": {}, "authMethods": [{"id": "cursor_login"}]}})
    elif method == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "sess_c",
              "modes": {"currentModeId": "agent", "availableModes": [{"id": "agent"}, {"id": "plan"}, {"id": "ask"}]},
              "models": {"currentModelId": "composer-2.5"},
              "configOptions": [{"id": "model", "currentValue": "composer-2.5[fast=true]", "options": [
                  {"value": "gpt-5.5[reasoning=medium]"}, {"value": "composer-2.5[fast=true]"}]}]}})
    elif method == "session/set_mode":
        if os.environ.get("FAIL_SET_MODE"):
            send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32000, "message": "mode ask unavailable"}})
        else:
            send({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif method == "session/prompt":
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess_c", "update": {
              "sessionUpdate": "tool_call", "toolCallId": "c1", "title": "Read File", "kind": "read", "status": "completed"}}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess_c", "update": {
              "sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "{\"decision\":\"accept\",\"notes\":\"ok\"}"}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif method == "session/cancel":
        pass
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func peerMethods(t *testing.T, log string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(readFile(t, log)), "\n") {
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func hasMethod(lines []string, method string) bool {
	for _, l := range lines {
		if strings.Contains(l, `"method": "`+method+`"`) {
			return true
		}
	}
	return false
}

// AC4/AC5: a cursor ACP reviewer is moved to ask mode before any prompt, and a
// peer that refuses the mode refuses the run; a performer is left in its mode.
func TestCursorACPReviewerForcesAskMode(t *testing.T) {
	run := func(t *testing.T, readOnly, failMode bool) (log string, usage UsageResult, notes []IsolationNote, out []byte, err error) {
		t.Helper()
		bin, log := writeFakeCursorACP(t)
		r, rerr := RunnerFromBinding(InterfaceACP, bin+" acp")
		if rerr != nil {
			t.Fatal(rerr)
		}
		env := map[string]string{"PEERLOG": log}
		if failMode {
			env["FAIL_SET_MODE"] = "1"
		}
		var mu sync.Mutex
		req := Request{
			SystemPrompt: "rubric", Payload: `{"story":"sty_1"}`, Env: env, ReadOnly: readOnly,
			Capture: CaptureFull,
			OnIsolation: func(n IsolationNote) {
				mu.Lock()
				notes = append(notes, n)
				mu.Unlock()
			},
		}
		ur, ok := r.(UsageRunner)
		if !ok {
			t.Fatal("acp runner must report usage")
		}
		out, usage, err = ur.RunUsage(context.Background(), req)
		mu.Lock()
		defer mu.Unlock()
		return log, usage, notes, out, err
	}

	t.Run("reviewer is set to ask before the prompt", func(t *testing.T) {
		log, usage, notes, out, err := run(t, true, false)
		if err != nil {
			t.Fatal(err)
		}
		lines := peerMethods(t, log)
		var order []string
		for _, l := range lines {
			if strings.Contains(l, "session/set_mode") || strings.Contains(l, "session/prompt") {
				order = append(order, l)
			}
		}
		if len(order) != 2 || !strings.Contains(order[0], "session/set_mode") || !strings.Contains(order[0], `"modeId": "ask"`) ||
			!strings.Contains(order[1], "session/prompt") {
			t.Errorf("want set_mode ask then prompt, got %v", order)
		}
		if !strings.Contains(string(out), `"decision":"accept"`) {
			t.Errorf("out = %q", out)
		}
		if usage.Available || !strings.Contains(usage.UnavailableReason, "cursor") {
			t.Errorf("usage must be a cursor-named unavailable: %+v", usage)
		}
		if usage.ModelResolved != "composer-2.5" {
			t.Errorf("model = %q, want the peer's", usage.ModelResolved)
		}
		if len(notes) != 0 {
			t.Errorf("a read in ask mode is inside the ceiling, got notes %+v", notes)
		}
	})
	t.Run("a failed set_mode refuses the run and sends no prompt", func(t *testing.T) {
		log, _, _, _, err := run(t, true, true)
		if err == nil {
			t.Fatal("a reviewer that cannot be made read-only must not run")
		}
		if !strings.Contains(err.Error(), "cursor") || !strings.Contains(err.Error(), "set_mode") {
			t.Errorf("error must name cursor and set_mode: %v", err)
		}
		if hasMethod(peerMethods(t, log), "session/prompt") {
			t.Error("no session/prompt may be sent after a failed set_mode")
		}
	})
	t.Run("a performer is not forced into a mode", func(t *testing.T) {
		log, usage, _, _, err := run(t, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if hasMethod(peerMethods(t, log), "session/set_mode") {
			t.Error("a performer session must not be moved to ask mode")
		}
		if !strings.Contains(usage.UnavailableReason, "cursor") {
			t.Errorf("usage = %+v, want a cursor-named unavailable", usage)
		}
	})
}

// A cursor ACP dispatch sends the peer's parameterised model value for a bare
// model name, and refuses an unknown name before any prompt.
func TestCursorACPModelDispatch(t *testing.T) {
	dispatch := func(t *testing.T, model string) (string, error) {
		t.Helper()
		bin, log := writeFakeCursorACP(t)
		r, err := RunnerFromBinding(InterfaceACP, bin+" acp")
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.Run(context.Background(), Request{
			SystemPrompt: "rubric", Payload: `{}`, Model: model, Env: map[string]string{"PEERLOG": log},
		})
		return log, err
	}

	log, err := dispatch(t, "composer-2.5")
	if err != nil {
		t.Fatal(err)
	}
	var sent string
	for _, l := range peerMethods(t, log) {
		if strings.Contains(l, "session/set_config_option") {
			sent = l
		}
	}
	if !strings.Contains(sent, `"value": "composer-2.5[fast=true]"`) {
		t.Errorf("want the parameterised model value sent, got %q", sent)
	}

	log, err = dispatch(t, "no-such-model")
	if err == nil || !strings.Contains(err.Error(), "cursor") || !strings.Contains(err.Error(), "gpt-5.5[reasoning=medium]") {
		t.Errorf("an unknown model must be refused with the available values, got %v", err)
	}
	if hasMethod(peerMethods(t, log), "session/prompt") {
		t.Error("no session/prompt may be sent after a refused model")
	}
}

// completedToolResult returns the result object of the first completed tool call
// of kind (readToolCall, shellToolCall) in a stream-json capture.
func completedToolResult(t *testing.T, name, kind string) map[string]any {
	t.Helper()
	for _, line := range cursorJSONLines(t, name) {
		if line["type"] != "tool_call" || line["subtype"] != "completed" {
			continue
		}
		call, _ := line["tool_call"].(map[string]any)
		body, _ := call[kind].(map[string]any)
		if res, ok := body["result"].(map[string]any); ok {
			return res
		}
	}
	t.Fatalf("%s: no completed %s", name, kind)
	return nil
}

// TestCursorSeatEvidence pins the dispatch-seat probes (15, 16, 17, 18): a read-only
// seat reads material outside its workspace, plan and ask still run a read-only
// shell, and under approvalMode "unrestricted" the mode (not the approval setting)
// is what refuses a write and a mutating shell.
func TestCursorSeatEvidence(t *testing.T) {
	for _, run := range []string{"15-plan-read-outside", "15-plan-read-adddir", "15-ask-read-outside"} {
		ok, _ := completedToolResult(t, run+".out", "readToolCall")["success"].(map[string]any)
		if c, _ := ok["content"].(string); !strings.Contains(c, "MATERIAL-CODEWORD-PELICAN") {
			t.Errorf("%s: Read did not succeed on the out-of-workspace file: %v", run, ok)
		}
	}
	for run, want := range map[string]string{"15-plan-shell": "PLANSHELL-42", "15-ask-shell": "ASKSHELL-42"} {
		ok, _ := completedToolResult(t, run+".out", "shellToolCall")["success"].(map[string]any)
		if s, _ := ok["stdout"].(string); !strings.Contains(s, want) {
			t.Errorf("%s: a read-only shell command must run: %v", run, ok)
		}
	}

	for _, run := range []string{"16-plan", "16-ask", "16-deny", "16-plan-deny"} {
		var meta struct {
			ApprovalMode string `json:"approvalMode"`
		}
		if err := json.Unmarshal([]byte(cursorRead(t, run+".meta.json")), &meta); err != nil || meta.ApprovalMode != "unrestricted" {
			t.Errorf("%s: must have run under approvalMode unrestricted: %v %+v", run, err, meta)
		}
		fs := cursorRead(t, run+".fs")
		for _, f := range []string{"written.txt: absent", "shell.txt: absent"} {
			if !strings.Contains(fs, f) {
				t.Errorf("%s.fs: want %q, got %q", run, f, fs)
			}
		}
	}

	checkACPModeProbe(t, "17", "17-acp-plan.json", "plan", "MATERIAL-CODEWORD-HERON")
	checkACPModeProbe(t, "18", "18-acp-ask.json", "ask", "MATERIAL-CODEWORD-EGRET")
}

// checkACPModeProbe pins an ACP capture: session/set_mode <mode> returned {}, the
// peer confirmed it, no permission was requested, nothing was written, and the
// material outside the workspace was read.
func checkACPModeProbe(t *testing.T, label, file, mode, codeword string) {
	t.Helper()
	var acp struct {
		SetMode map[string]any  `json:"set_mode"`
		FS      map[string]bool `json:"fs"`
		Frames  []struct {
			Dir string         `json:"dir"`
			Msg map[string]any `json:"msg"`
		} `json:"frames"`
	}
	if err := json.Unmarshal([]byte(cursorRead(t, file)), &acp); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"written.txt", "shell.txt"} {
		if present, ok := acp.FS[f]; !ok || present {
			t.Errorf("%s: %s must be absent: %v", label, f, acp.FS)
		}
	}
	if r, _ := acp.SetMode["result"].(map[string]any); acp.SetMode["id"] != float64(3) || r == nil || len(r) != 0 {
		t.Errorf("%s: session/set_mode %s must return {}: %v", label, mode, acp.SetMode)
	}
	setMode, modeUpdate, material := false, false, false
	for _, f := range acp.Frames {
		switch f.Msg["method"] {
		case "session/request_permission":
			t.Errorf("%s: %s mode made a permission request: %v", label, mode, f.Msg)
		case "session/set_mode":
			params, _ := f.Msg["params"].(map[string]any)
			setMode = f.Dir == "out" && params["modeId"] == mode
		case "session/update":
			params, _ := f.Msg["params"].(map[string]any)
			up, _ := params["update"].(map[string]any)
			if up["sessionUpdate"] == "current_mode_update" && up["currentModeId"] == mode {
				modeUpdate = true
			}
			if out, _ := up["rawOutput"].(map[string]any); out != nil {
				if c, _ := out["content"].(string); strings.Contains(c, codeword) {
					material = true
				}
			}
		}
	}
	if !setMode || !modeUpdate || !material {
		t.Errorf("%s: want set_mode %s (%v), the peer's mode update (%v) and the read of out-of-workspace material (%v)", label, mode, setMode, modeUpdate, material)
	}
}

// A cursor ACP binding's model is mapped onto the parameterised value the peer
// advertises in session/new (testdata/cursor/5-acp.json): a base name maps to the
// first value starting "<model>[", an exact value is kept, an unknown name is
// refused with the available values.
func TestCursorModelValue(t *testing.T) {
	var capture struct {
		Frames []struct {
			Dir string          `json:"dir"`
			Msg json.RawMessage `json:"msg"`
		} `json:"frames"`
	}
	if err := json.Unmarshal([]byte(cursorRead(t, "5-acp.json")), &capture); err != nil {
		t.Fatal(err)
	}
	var sessRes json.RawMessage
	for _, f := range capture.Frames {
		var m struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if f.Dir == "in" && json.Unmarshal(f.Msg, &m) == nil && m.ID == 2 && len(m.Result) > 0 {
			sessRes = m.Result
			break
		}
	}
	if sessRes == nil {
		t.Fatal("5-acp.json has no session/new reply")
	}

	for model, want := range map[string]string{
		"composer-2.5":            "composer-2.5[fast=true]",
		"composer-2.5[fast=true]": "composer-2.5[fast=true]",
		"gemini-3.1-pro":          "gemini-3.1-pro[]",
	} {
		got, err := cursorModelValue(sessRes, model)
		if err != nil || got != want {
			t.Errorf("cursorModelValue(%q) = %q, %v; want %q", model, got, err, want)
		}
	}
	got, err := cursorModelValue(sessRes, "composer-9")
	if err == nil || !strings.Contains(err.Error(), "cursor") || !strings.Contains(err.Error(), "composer-9") ||
		!strings.Contains(err.Error(), "composer-2.5[fast=true]") {
		t.Errorf("an unknown model must be refused naming cursor and listing the values, got %q, %v", got, err)
	}
	// A reply that advertises no model option leaves the model as given.
	if got, err := cursorModelValue(json.RawMessage(`{"sessionId":"s"}`), "composer-2.5"); err != nil || got != "composer-2.5" {
		t.Errorf("no model option: got %q, %v", got, err)
	}
}
