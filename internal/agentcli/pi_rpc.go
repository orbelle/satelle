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
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

// A dispatched pi seat over pi's RPC mode (sty_79ce4858): `pi --mode rpc` reads
// JSONL commands on stdin and writes responses and session events on stdout
// (pi docs: rpc.md, rpc-commands.md, json.md, rpc-extension-ui.md). satelle sends
// the payload as the one prompt command, reads every event as it happens, and
// ends the seat itself once pi reports agent_settled: it fetches the answer and
// the session stats, closes stdin and kills the seat's process group, so a pi that
// would stay alive never holds a gate. Every pi RPC record name lives in this
// file ([[satelle-agent-agnostic]] §1).

// piRPCColdFlags are the startup flags that keep a dispatched pi seat cold: no
// session record, and none of pi's discovered extensions, skills, prompt
// templates, context files or MCP servers. The template must carry each one;
// newPiRPCRunner refuses it otherwise.
var piRPCColdFlags = []string{"--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--no-mcp"}

// PiRPCCommand is the cold pi RPC spawn line an interface = "pi-rpc" binding
// starts from: every flag newPiRPCRunner requires, the system prompt as a startup
// flag, and the binding's own tools, model and thinking level. A binding adds its
// provider extension (-e <path>) where pi needs one.
const PiRPCCommand = "pi --mode rpc --no-session --no-extensions --no-skills --no-prompt-templates --no-context-files --no-mcp --no-themes --system-prompt {system} --tools {tools} --model {model} --thinking {effort}"

// piRPCDialogMethods are the extension UI methods that block pi until the client
// answers. satelle answers each with cancelled, so no dialog holds a seat open.
// pi's fire-and-forget methods (notify, setStatus, …) expect no reply.
var piRPCDialogMethods = []string{"select", "confirm", "input", "editor"}

// piRPCGrace bounds each wait after pi has settled or been aborted: for the
// answer and stats replies, for agent_settled after an abort, and for pi to exit
// once its stdin is closed. The seat's process group is killed when it runs out.
const piRPCGrace = 2 * time.Second

// Request ids satelle puts on its commands, matched against pi's responses.
const (
	piRPCPromptID = "satelle-prompt"
	piRPCTextID   = "satelle-text"
	piRPCStatsID  = "satelle-stats"
	piRPCAbortID  = "satelle-abort"
)

// piRPCLabel names this adapter in usage and model reasons.
const piRPCLabel = HarnessPi + " " + InterfacePiRPC

type piRPCRunner struct {
	binary string
	args   []string
}

// newPiRPCRunner builds the runner for an interface = "pi-rpc" binding. It
// refuses, naming every missing flag, a template that is not a pi command or
// that would not start cold: --mode rpc, each of piRPCColdFlags, and
// --system-prompt {system}. Dispatch and `satelle validate` both build runners
// through RunnerFromBinding, so a refused binding never starts a process.
func newPiRPCRunner(command string) (Runner, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 || strings.EqualFold(fields[0], "in-loop") {
		return nil, fmt.Errorf("agentcli: interface=%s requires a pi spawn command, not in-loop/empty", InterfacePiRPC)
	}
	binary, args := fields[0], fields[1:]
	if adapter := adapterOf(binary, args); adapter != HarnessPi {
		return nil, fmt.Errorf("agentcli: interface=%s drives pi only; command %q is the %s adapter", InterfacePiRPC, binary, adapter)
	}
	var missing []string
	if mode, _ := flagValue(args, "--mode"); mode != "rpc" {
		missing = append(missing, "--mode rpc")
	}
	for _, f := range piRPCColdFlags {
		if !slices.Contains(args, f) {
			missing = append(missing, f)
		}
	}
	if sys, _ := flagValue(args, "--system-prompt"); sys != "{system}" {
		missing = append(missing, "--system-prompt {system}")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("agentcli: interface=%s command lacks %s — a dispatched pi seat must start cold, with only the system prompt satelle sends", InterfacePiRPC, strings.Join(missing, ", "))
	}
	return piRPCRunner{binary: binary, args: args}, nil
}

func (p piRPCRunner) Name() string { return p.binary }

func (p piRPCRunner) Command() string {
	return strings.Join(append([]string{p.binary}, p.args...), " ")
}

func (p piRPCRunner) Run(ctx context.Context, req Request) ([]byte, error) {
	out, _, err := p.RunUsage(ctx, req)
	return out, err
}

// spawnArgs is the argv pi starts with: the template through buildArgs, with a
// {payload} token dropped because the payload is the prompt command, not argv.
func (p piRPCRunner) spawnArgs(req Request) []string {
	tmpl := slices.DeleteFunc(slices.Clone(p.args), func(tok string) bool { return tok == "{payload}" })
	return buildArgs(tmpl, req)
}

// RunUsage runs one dispatched pi seat and returns pi's last assistant text with
// the usage the run itself reported. A run cut off at req.MaxTurns returns
// ErrMaxTurns and no text.
func (p piRPCRunner) RunUsage(ctx context.Context, req Request) ([]byte, UsageResult, error) {
	onEvent, stopEvents := eventStream(req)
	defer stopEvents()
	run := &piRPCRun{onEvent: onEvent, maxTurns: req.MaxTurns}
	fail := func(err error) ([]byte, UsageResult, error) {
		ev := newEvent(EventFailed)
		ev.Error = err.Error()
		emitEvent(onEvent, ev)
		return nil, piRPCUsageUnavailable("the run ended before pi reported its session stats", run.turns), err
	}

	cmd := exec.Command(p.binary, p.spawnArgs(req)...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	cmd.Env = composeEnv(os.Environ(), req.Env, req.Unset...)
	procgroup.Set(cmd)
	cmd.WaitDelay = piRPCGrace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(fmt.Errorf("agentcli: pi rpc: stdin: %w", err))
	}
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		return fail(fmt.Errorf("agentcli: pi rpc: start %s: %w", p.binary, err))
	}
	emitEvent(onEvent, newStartEvent(cmd.Process.Pid))

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = outW.Close()
		_ = errW.Close()
		close(exited)
	}()
	var stderr bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		var sink io.Writer
		if req.Sink != nil {
			sink = redactingWriter{req.Sink}
		}
		teeLines(errR, &stderr, sink, "[stderr] ")
	}()
	lines := make(chan []byte)
	go readPiRPCLines(outR, req.Sink, lines)

	run.stdin = stdin
	// end stops the seat: stdin closed, a bounded wait for pi to exit, then its
	// whole process group killed whether or not it did.
	end := func() {
		go func() {
			for range lines {
			}
		}()
		_ = stdin.Close()
		select {
		case <-exited:
		case <-time.After(piRPCGrace):
		}
		_ = procgroup.Kill(cmd)
		<-exited
		<-stderrDone
	}
	killed := func() ([]byte, UsageResult, error) {
		_ = procgroup.Kill(cmd)
		end()
		return fail(fmt.Errorf("agentcli: pi rpc: %w", ctx.Err()))
	}

	if err := run.send(map[string]any{"id": piRPCPromptID, "type": "prompt", "message": req.Payload}); err != nil {
		end()
		return fail(fmt.Errorf("agentcli: pi rpc: send prompt: %w", err))
	}
	// Phase 1: the run, until pi settles. An abort for the turn cap waits at most
	// piRPCGrace for that settle.
	var abortWait <-chan time.Time
	for !run.settled {
		select {
		case <-ctx.Done():
			return killed()
		case <-abortWait:
			run.settled = true
		case line, ok := <-lines:
			if !ok {
				end()
				return fail(piRPCExitError("pi exited before it settled", stderr.String()))
			}
			if err := run.handle(line); err != nil {
				end()
				return fail(err)
			}
			if run.aborted && abortWait == nil {
				abortWait = time.After(piRPCGrace)
			}
		}
	}
	// Phase 2: the answer and the stats, each matched by id.
	if !run.aborted {
		_ = run.send(map[string]any{"id": piRPCTextID, "type": "get_last_assistant_text"})
	}
	_ = run.send(map[string]any{"id": piRPCStatsID, "type": "get_session_stats"})
	replyWait := time.After(piRPCGrace)
	for run.awaitingReplies() {
		select {
		case <-ctx.Done():
			return killed()
		case <-replyWait:
			run.gaveUp = true
		case line, ok := <-lines:
			if !ok {
				run.gaveUp = true
				continue
			}
			_ = run.handle(line)
		}
	}
	end()

	usage := run.usage()
	if usage.Available {
		ev := newEvent(EventUsage)
		ev.Usage = &usage
		emitEvent(onEvent, ev)
	}
	if run.aborted {
		err := fmt.Errorf("%w: pi rpc aborted the run when it started turn %d of a %d-turn budget", ErrMaxTurns, run.turns, req.MaxTurns)
		ev := newEvent(EventFailed)
		ev.Error = err.Error()
		emitEvent(onEvent, ev)
		return nil, usage, err
	}
	if strings.TrimSpace(run.answer()) == "" {
		err := Mark(piRPCExitError("pi settled without an answer", stderr.String()), ErrNoAnswer)
		ev := newEvent(EventFailed)
		ev.Error = err.Error()
		emitEvent(onEvent, ev)
		return nil, usage, err
	}
	emitEvent(onEvent, newEvent(EventCompleted))
	return []byte(run.answer()), usage, nil
}

// readPiRPCLines splits pi's stdout on LF only (pi's framing: a JSON string may
// hold U+2028), tees each record to sink and hands it to the run loop. lines is
// closed when stdout ends.
func readPiRPCLines(r io.Reader, sink io.Writer, lines chan<- []byte) {
	defer close(lines)
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			if sink != nil && !isHiddenReasoningLine(trimmed) {
				_, _ = sink.Write([]byte(RedactSecrets(string(line))))
			}
			lines <- trimmed
		}
		if err != nil {
			return
		}
	}
}

// redactingWriter writes to w with RedactSecrets applied, as every raw trace
// satelle tees is.
type redactingWriter struct{ w io.Writer }

func (r redactingWriter) Write(p []byte) (int, error) {
	_, err := r.w.Write([]byte(RedactSecrets(string(p))))
	return len(p), err
}

func piRPCExitError(what, stderr string) error {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return fmt.Errorf("agentcli: pi rpc: %s: %s", what, msg)
	}
	return fmt.Errorf("agentcli: pi rpc: %s", what)
}

// piRPCRecord is the union of the pi RPC records satelle reads.
type piRPCRecord struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success *bool           `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
	// message_update
	AssistantMessageEvent *struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
	// message_end's message object; an extension_ui_request's message is a
	// string, so it is decoded only for message_end.
	Message json.RawMessage `json:"message"`
	// tool_execution_start / tool_execution_end
	ToolName string          `json:"toolName"`
	Args     json.RawMessage `json:"args"`
	IsError  bool            `json:"isError"`
	// extension_ui_request
	Method string `json:"method"`
}

type piRPCMessage struct {
	Role          string          `json:"role"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	ResponseModel string          `json:"responseModel"`
	Content       json.RawMessage `json:"content"`
}

// piRPCStats is the get_session_stats data satelle records.
type piRPCStats struct {
	Tokens *struct {
		Input      int `json:"input"`
		Output     int `json:"output"`
		CacheRead  int `json:"cacheRead"`
		CacheWrite int `json:"cacheWrite"`
	} `json:"tokens"`
	Cost *float64 `json:"cost"`
}

// piRPCRun is the state of one seat's protocol exchange. Only the run loop
// touches it.
type piRPCRun struct {
	stdin    io.Writer
	onEvent  EventHandler
	maxTurns int

	settled, aborted, gaveUp bool
	turns, tools             int
	model                    string
	lastText                 string // the last assistant message_end's text
	text                     *string
	gotText, gotStats        bool
	stats                    *piRPCStats
}

func (r *piRPCRun) send(cmd map[string]any) error {
	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_, err = r.stdin.Write(append(b, '\n'))
	return err
}

func (r *piRPCRun) awaitingReplies() bool {
	if r.gaveUp {
		return false
	}
	return !r.gotStats || (!r.aborted && !r.gotText)
}

// answer is pi's last assistant text: get_last_assistant_text's reply, or the
// last assistant message_end's text when that reply carried none.
func (r *piRPCRun) answer() string {
	if r.text != nil && strings.TrimSpace(*r.text) != "" {
		return *r.text
	}
	return r.lastText
}

// handle applies one pi record: events become satelle events, the turn cap is
// enforced on turn_start, dialogs are cancelled, and responses are matched to
// satelle's commands. An error ends the run (a refused prompt).
func (r *piRPCRun) handle(line []byte) error {
	var rec piRPCRecord
	if json.Unmarshal(line, &rec) != nil {
		return nil
	}
	switch rec.Type {
	case "response":
		return r.response(rec)
	case "turn_start":
		r.turns++
		ev := newEvent(EventMessage)
		ev.Text = fmt.Sprintf("turn %d", r.turns)
		emitEvent(r.onEvent, ev)
		if r.maxTurns > 0 && r.turns > r.maxTurns && !r.aborted {
			r.aborted = true
			_ = r.send(map[string]any{"id": piRPCAbortID, "type": "abort"})
		}
	case "message_update":
		ev := newEvent(EventMessage)
		if u := rec.AssistantMessageEvent; u != nil && u.Type == "text_delta" {
			ev.Text = u.Delta
		}
		emitEvent(r.onEvent, ev)
	case "message_end":
		var m piRPCMessage
		if json.Unmarshal(rec.Message, &m) == nil && m.Role == "assistant" {
			if id := m.modelID(); id != "" {
				r.model = id
			}
			r.lastText = m.text()
			emitEvent(r.onEvent, newEvent(EventMessage))
		}
	case "tool_execution_start":
		r.tools++
		ev := newEvent(EventToolStart)
		ev.Tool = rec.ToolName
		ev.Command = toolCommandRaw(rec.Args)
		emitEvent(r.onEvent, ev)
	case "tool_execution_end":
		ev := newEvent(EventToolEnd)
		ev.Tool = rec.ToolName
		if rec.IsError {
			ev.Status = "error"
		}
		emitEvent(r.onEvent, ev)
	case "extension_ui_request":
		if slices.Contains(piRPCDialogMethods, rec.Method) {
			_ = r.send(map[string]any{"type": "extension_ui_response", "id": rec.ID, "cancelled": true})
			ev := newEvent(EventInteractiveDenied)
			ev.Tool = "extension_ui:" + rec.Method
			ev.Meta = map[string]string{EventMetaResponse: "denied"}
			emitEvent(r.onEvent, ev)
		}
	case "agent_settled":
		r.settled = true
	}
	return nil
}

func (r *piRPCRun) response(rec piRPCRecord) error {
	ok := rec.Success != nil && *rec.Success
	switch rec.ID {
	case piRPCPromptID:
		if !ok {
			return fmt.Errorf("agentcli: pi rpc: pi refused the prompt: %s", rec.Error)
		}
		var data struct {
			Disposition string `json:"disposition"`
		}
		// A handled prompt starts no run, so no agent_settled follows it.
		if json.Unmarshal(rec.Data, &data) == nil && data.Disposition == "handled" {
			r.settled = true
		}
	case piRPCTextID:
		r.gotText = true
		var data struct {
			Text *string `json:"text"`
		}
		if ok && json.Unmarshal(rec.Data, &data) == nil {
			r.text = data.Text
		}
	case piRPCStatsID:
		r.gotStats = true
		var stats piRPCStats
		if ok && json.Unmarshal(rec.Data, &stats) == nil {
			r.stats = &stats
		}
	}
	return nil
}

// modelID is the provider/model pi reports it ran, the concrete response model
// when pi names one.
func (m *piRPCMessage) modelID() string {
	model := m.ResponseModel
	if model == "" {
		model = m.Model
	}
	if model == "" {
		return ""
	}
	if m.Provider != "" && !strings.HasPrefix(model, m.Provider+"/") {
		return m.Provider + "/" + model
	}
	return model
}

// text joins the message's text blocks.
func (m *piRPCMessage) text() string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		var s string
		_ = json.Unmarshal(m.Content, &s)
		return s
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}

// usage is what the run reported: tokens, cache split and cost from
// get_session_stats, turns counted from turn_start, and the model from the last
// assistant message_end. No pi session file is read.
func (r *piRPCRun) usage() UsageResult {
	if r.stats == nil || r.stats.Tokens == nil {
		return piRPCUsageUnavailable("get_session_stats returned no token usage", r.turns)
	}
	u := piStatsUsage(*r.stats)
	u.Turns, u.TurnsAvailable = r.turns, true
	if r.model != "" {
		u.ModelResolved = r.model
	}
	return u
}

// piStatsUsage maps get_session_stats onto a UsageResult: pi's input, cacheRead
// and cacheWrite are disjoint parts of the prompt, as Anthropic's are.
func piStatsUsage(s piRPCStats) UsageResult {
	u := UsageResult{Adapter: HarnessPi, Available: true, CacheSplitAvailable: true}
	u.FreshInputTokens = s.Tokens.Input
	u.CacheReadInputTokens = s.Tokens.CacheRead
	u.CacheCreationInputTokens = s.Tokens.CacheWrite
	u.InputTokens = u.FreshInputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	u.OutputTokens = s.Tokens.Output
	u.TotalTokens = u.InputTokens + u.OutputTokens
	u.ModelResolved = noModelReport(piRPCLabel)
	switch {
	case s.Cost == nil:
		u.CostUnavailableReason = "pi adapter: get_session_stats carried no cost"
	case *s.Cost > 0:
		c := *s.Cost
		u.CostUSD = &c
	default:
		u.CostUnavailableReason = piUnpricedCostReason
	}
	return u
}

// piRPCUsageUnavailable is the pi-named usage of a run whose stats never came;
// the turns it counted are still a measurement.
func piRPCUsageUnavailable(why string, turns int) UsageResult {
	u := unavailableUsage(HarnessPi, why)
	u.Adapter = HarnessPi
	u.ModelResolved = noModelReport(piRPCLabel)
	u.Turns, u.TurnsAvailable = turns, true
	return u
}

var _ UsageRunner = piRPCRunner{}

// errPiRPCNotLive is OpenerFromBinding's answer for a pi-rpc binding: a
// dispatched pi seat is one-shot, and satelle opens no live pi session yet.
var errPiRPCNotLive = fmt.Errorf("%w: interface=%s runs one-shot dispatches only; satelle opens no live pi session", ErrNotLiveCapable, InterfacePiRPC)
