package agentcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type streamRunner struct {
	binary string
	args   []string
	how    string
}

// newStreamRunner builds a stream-json runner from a multi-token spawn command.
// Rejects empty/in-loop and bare single tokens (same ceiling as ACP). Template
// placeholders {system} and {payload} are banned — they ride the first user
// message, not argv. {tools}/{model}/{effort} remain spawn-time flags and are
// substituted via buildArgs (empty drops the preceding flag).
func newStreamRunner(command string) (Runner, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 || strings.EqualFold(fields[0], "in-loop") {
		return nil, fmt.Errorf("agentcli: interface=stream requires a multi-token spawn command (e.g. DefaultClaudeStreamCommand), not in-loop/empty")
	}
	if len(fields) == 1 {
		return nil, fmt.Errorf("agentcli: interface=stream command %q: bare single token rejected — use a full stream spawn line", fields[0])
	}
	for _, tok := range fields {
		if tok == "{system}" || tok == "{payload}" {
			return nil, fmt.Errorf("agentcli: interface=stream command must not contain placeholder %s — system/payload ride the first user message, not argv", tok)
		}
	}
	return streamRunner{binary: fields[0], args: fields[1:], how: strings.Join(fields, " ")}, nil
}

func (s streamRunner) Name() string    { return s.binary }
func (s streamRunner) Command() string { return s.how }

func (s streamRunner) Open(ctx context.Context, req Request, pol PermissionPolicy) (Session, error) {
	return openStreamSession(ctx, s, req, pol)
}

func (s streamRunner) Run(ctx context.Context, req Request) ([]byte, error) {
	out, _, err := s.RunUsage(ctx, req)
	return out, err
}

// RunUsage implements UsageRunner: a stream-json result's modelUsage arrives on
// an EventUsage alongside the already-unwrapped decision text, so Run's bytes
// alone never carry it — runOnce reads it from here instead of UnwrapUsage
// (sty_87b86044 AC2).
func (s streamRunner) RunUsage(ctx context.Context, req Request) ([]byte, UsageResult, error) {
	pol := defaultPermissionPolicy(toolsAllowMutators(req.AllowedTools))
	if req.ReadOnly {
		pol = ReviewerPermissionPolicy(req.AllowedTools)
	}
	sess, err := openStreamSession(ctx, s, req, pol)
	if err != nil {
		return nil, UsageResult{}, err
	}
	out, usage, err := runOneShotUsage(ctx, sess, req)
	if adapterOf(s.binary, s.args) == HarnessPi {
		// satelle reads no pi stream protocol: never a silent zero.
		usage = piTransportUnsupported(InterfaceStream)
	}
	usage.stampAdapter(adapterOf(s.binary, s.args))
	return out, usage, err
}

type streamSession struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	wmu       sync.Mutex
	pol       PermissionPolicy
	onEvent   EventHandler
	ev        <-chan Event
	closeEv   func()
	capMu     sync.Mutex
	captured  strings.Builder
	closed    chan struct{}
	closeOnce sync.Once
	// toolNames maps a tool_use block id to its tool name so the matching
	// tool_result (which carries only the id) can be reported as EventToolEnd
	// under the same name. Touched only by readLoop (sty_1de7494c).
	toolNames map[string]string
}

func openStreamSession(ctx context.Context, s streamRunner, req Request, pol PermissionPolicy) (Session, error) {
	if pol == nil {
		pol = defaultPermissionPolicy(toolsAllowMutators(req.AllowedTools))
	}
	if err := checkDeny(s.binary, s.args, InterfaceStream, req); err != nil {
		return nil, err
	}
	args := reviewerArgs(s.binary, buildArgs(s.args, req), req)
	cmd := exec.CommandContext(ctx, s.binary, args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	cmd.Env = composeEnv(os.Environ(), req.Env, req.Unset...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("agentcli: stream: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("agentcli: stream: stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("agentcli: stream: stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("agentcli: stream: start %s: %w", s.binary, err)
	}
	onEvent, ev, closeEv := fanoutEvents(req.OnEvent)
	sess := &streamSession{
		cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr,
		pol: pol, onEvent: onEvent, ev: ev, closeEv: closeEv,
		closed: make(chan struct{}),
	}
	go sess.readLoop()
	go io.Copy(io.Discard, stderr)
	emitEvent(onEvent, newStartEvent(cmd.Process.Pid))
	return sess, nil
}

func (s *streamSession) Events() <-chan Event { return s.ev }

func (s *streamSession) Captured() []byte {
	s.capMu.Lock()
	defer s.capMu.Unlock()
	return []byte(s.captured.String())
}

func (s *streamSession) Send(ctx context.Context, turn Turn) error {
	var text string
	if sp := strings.TrimSpace(turn.System); sp != "" {
		text = sp
		if t := strings.TrimSpace(turn.Text); t != "" {
			text += "\n\n" + t
		}
	} else {
		text = turn.Text
	}
	if strings.TrimSpace(text) == "" {
		text = "{}"
	}
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}
	return s.writeJSON(msg)
}

func (s *streamSession) writeJSON(v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

func (s *streamSession) Cancel() error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Kill()
	}
	return nil
}

func (s *streamSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()
		waitErr := s.cmd.Wait()
		close(s.closed)
		if s.closeEv != nil {
			s.closeEv()
		}
		if waitErr != nil && len(bytes.TrimSpace(s.Captured())) == 0 {
			err = fmt.Errorf("agentcli: stream: process: %w", waitErr)
		}
	})
	return err
}

func (s *streamSession) readLoop() {
	sc := bufio.NewScanner(s.stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var raw map[string]any
		if json.Unmarshal(line, &raw) != nil {
			continue
		}
		typ, _ := raw["type"].(string)
		switch typ {
		case "system":
			// The stream-json transport's own init record names the model the
			// session actually opened with (sty_7069bced) — the orchestrator
			// session capture (cmd_story_chat) reads this once, before any turn.
			if subtype, _ := raw["subtype"].(string); subtype == "init" {
				m, _ := raw["model"].(string)
				tools := streamInitTools(raw)
				if strings.TrimSpace(m) != "" || tools != nil {
					ev := newEvent(EventSessionInit)
					ev.Model = strings.TrimSpace(m)
					ev.Tools = tools
					emitEvent(s.onEvent, ev)
				}
			}
		case "assistant":
			// tool_use blocks are the tool boundary for tools the binding
			// pre-allowed (no can_use_tool control request is raised for them),
			// so they are reported here, synchronously, as EventToolStart.
			for _, tu := range streamToolUses(raw) {
				if s.toolNames == nil {
					s.toolNames = map[string]string{}
				}
				if tu.id != "" {
					s.toolNames[tu.id] = tu.name
				}
				ev := newEvent(EventToolStart)
				ev.Tool = tu.name
				ev.Command = tu.command
				ev.Status = "running"
				emitEvent(s.onEvent, ev)
			}
			if txt := streamAssistantText(raw); txt != "" {
				ev := newEvent(EventMessage)
				ev.Text = SafeText(txt)
				emitEvent(s.onEvent, ev)
			}
		case "user":
			// The CLI echoes tool results as user-role tool_result blocks.
			for _, tr := range streamToolResults(raw) {
				ev := newEvent(EventToolEnd)
				ev.Tool = s.toolNames[tr.id]
				ev.Status = "done"
				if tr.isError {
					ev.Status = "error"
				}
				emitEvent(s.onEvent, ev)
			}
		case "control_request":
			s.handleControl(raw)
		case "result":
			if res, ok := raw["result"].(string); ok {
				s.capMu.Lock()
				s.captured.Reset()
				s.captured.WriteString(res)
				s.capMu.Unlock()
			}
			if ur := usageFromMap(raw); ur != nil {
				ev := newEvent(EventUsage)
				ev.Usage = ur
				emitEvent(s.onEvent, ev)
			}
			// Per-turn result: capture + completed so one-shot drain unblocks.
			// Keep reading so a second Send can land without respawning.
			emitEvent(s.onEvent, newEvent(EventCompleted))
		case "error":
			ev := newEvent(EventFailed)
			ev.Error = fmt.Sprint(raw["error"])
			emitEvent(s.onEvent, ev)
			return
		}
	}
	emitEvent(s.onEvent, newEvent(EventCompleted))
	if s.closeEv != nil {
		s.closeEv()
	}
}

// streamInitTools reads the tool names the harness offers the session off a
// system/init record's `tools` array. Nil when the record carries none; a
// present empty array is non-nil (the harness offers no tools).
func streamInitTools(raw map[string]any) []string {
	arr, ok := raw["tools"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, t := range arr {
		switch v := t.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			if n, _ := v["name"].(string); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

func streamAssistantText(raw map[string]any) string {
	msg, _ := raw["message"].(map[string]any)
	if msg == nil {
		return ""
	}
	content, _ := msg["content"].([]any)
	var b strings.Builder
	for _, c := range content {
		m, _ := c.(map[string]any)
		if m["type"] == "text" {
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
			}
		}
	}
	return b.String()
}

type streamToolUse struct{ id, name, command string }

type streamToolResult struct {
	id      string
	isError bool
}

func streamContentBlocks(raw map[string]any) []map[string]any {
	msg, _ := raw["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	content, _ := msg["content"].([]any)
	out := make([]map[string]any, 0, len(content))
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// streamToolUses returns the tool_use blocks of an assistant record, in order.
func streamToolUses(raw map[string]any) []streamToolUse {
	var out []streamToolUse
	for _, m := range streamContentBlocks(raw) {
		if m["type"] != "tool_use" {
			continue
		}
		name, _ := m["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		id, _ := m["id"].(string)
		out = append(out, streamToolUse{id: id, name: name, command: toolCommand(m["input"])})
	}
	return out
}

// streamToolResults returns the tool_result blocks of a user record, in order.
func streamToolResults(raw map[string]any) []streamToolResult {
	var out []streamToolResult
	for _, m := range streamContentBlocks(raw) {
		if m["type"] != "tool_result" {
			continue
		}
		id, _ := m["tool_use_id"].(string)
		isErr, _ := m["is_error"].(bool)
		out = append(out, streamToolResult{id: id, isError: isErr})
	}
	return out
}

func (s *streamSession) handleControl(raw map[string]any) {
	reqID, _ := raw["request_id"].(string)
	if reqID == "" {
		reqID, _ = raw["requestId"].(string)
	}
	req, _ := raw["request"].(map[string]any)
	subtype, _ := req["subtype"].(string)
	if subtype == "" {
		subtype, _ = req["type"].(string)
	}
	tool, _ := req["tool_name"].(string)
	if tool == "" {
		tool, _ = req["toolName"].(string)
	}
	behavior := "allow"
	answer := map[string]any{}
	if subtype == "can_use_tool" || subtype == "permission" {
		if isInteractiveAskTool(tool, "") {
			// No human is attached to any satelle session: an interactive
			// question is always denied, whatever the policy says.
			behavior = "deny"
			answer["message"] = noUserAnswer
			input, _ := json.Marshal(req["input"])
			emitEvent(s.onEvent, interactiveDeniedEvent(tool, questionText(input, ""), "denied"))
		} else if dec := s.pol(PermissionRequest{ToolName: tool, Kind: toolNameKind(tool)}); !dec.Allow {
			behavior = "deny"
		}
	}
	answer["behavior"] = behavior
	_ = s.writeJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
			"response":   answer,
		},
	})
}
