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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// acpRunner runs an Agent Client Protocol agent over stdio (epic:agent-dispatch-transport).
// command is the spawn line only (e.g. "grok agent stdio") — no template placeholders.
// System prompt and payload are delivered via session/prompt content blocks.
// One session per Run; no cross-dispatch reuse. Does not set story status.
type acpRunner struct {
	binary string
	args   []string
	how    string // body-free spawn string for Command() evidence
}

// newACPRunner builds an acpRunner from a multi-token spawn command.
// Rejects empty/in-loop, bare single tokens, and argv that still carry template placeholders.
func newACPRunner(command string) (Runner, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 || strings.EqualFold(fields[0], "in-loop") {
		return nil, fmt.Errorf("agentcli: interface=acp requires a multi-token ACP spawn command (e.g. \"grok agent stdio\"), not in-loop/empty")
	}
	if len(fields) == 1 {
		return nil, fmt.Errorf("agentcli: interface=acp command %q: bare single token rejected — use a full ACP spawn line (e.g. \"grok agent stdio\")", fields[0])
	}
	for _, tok := range fields {
		switch tok {
		case "{system}", "{payload}", "{tools}", "{model}", "{effort}", "{settings}":
			return nil, fmt.Errorf("agentcli: interface=acp command must not contain placeholder %s — system/payload/tools/effort ride the ACP session, not argv", tok)
		}
	}
	if adapterOf(fields[0], fields[1:]) == HarnessGrok {
		return nil, fmt.Errorf("agentcli: interface=acp is not supported for grok (it cannot be held to a tool grant); use grok on the command transport")
	}
	if adapterOf(fields[0], fields[1:]) == HarnessCursor {
		if err := cursorACPProblem(fields[1:]); err != nil {
			return nil, err
		}
	}
	return acpRunner{
		binary: fields[0],
		args:   fields[1:],
		how:    strings.Join(fields, " "),
	}, nil
}

func (a acpRunner) Name() string    { return a.binary }
func (a acpRunner) Command() string { return a.how }

func (a acpRunner) Open(ctx context.Context, req Request, pol PermissionPolicy) (Session, error) {
	return openACPSession(ctx, a, req, pol)
}

// acpEffortArgvSupported reports whether the ACP spawn is Grok-shaped and may
// receive the Grok-only --reasoning-effort argv flag (sty_aa726901). Unknown
// peers are false — effort rides session/set_config_option only. Deliberate allowlist: an unknown flag
// can abort a non-Grok spawn.
func acpEffortArgvSupported(binary string, args []string) bool {
	base := strings.ToLower(filepath.Base(binary))
	if strings.Contains(base, "grok") {
		return true
	}
	for _, a := range args {
		if strings.Contains(strings.ToLower(a), "grok") {
			return true
		}
	}
	return false
}

func (a acpRunner) Run(ctx context.Context, req Request) ([]byte, error) {
	sess, err := openACPSession(ctx, a, req, acpPolicyFor(req))
	if err != nil {
		return nil, err
	}
	return runOneShot(ctx, sess, req)
}

// RunUsage implements UsageRunner: the ACP transport reports usage on the
// session/prompt response (when the peer supplies it), never in the captured
// text. A peer that supplies none records unavailable with this adapter's
// reason (sty_c8d45201).
func (a acpRunner) RunUsage(ctx context.Context, req Request) ([]byte, UsageResult, error) {
	sess, err := openACPSession(ctx, a, req, acpPolicyFor(req))
	if err != nil {
		return nil, UsageResult{}, err
	}
	out, usage, err := runOneShotUsage(ctx, sess, req)
	if adapterOf(a.binary, a.args) == HarnessPi {
		// satelle reads no pi acp protocol: never a silent zero.
		return out, piTransportUnsupported(InterfaceACP), err
	}
	if !usage.Available && usage.UnavailableReason == "" {
		usage = acpUnavailableUsage(acpAdapterLabel(a.binary, a.args))
	}
	if usage.ModelResolved == "" {
		usage.ModelResolved = noModelReport(acpAdapterLabel(a.binary, a.args))
	}
	usage.stampAdapter(adapterOf(a.binary, a.args))
	return out, usage, err
}

// acpPolicyFor is the one-shot permission policy of an ACP request: a reviewer
// (req.ReadOnly) allows only tools inside its grant; anything else keeps the
// mutator ceiling.
func acpPolicyFor(req Request) PermissionPolicy {
	if req.ReadOnly {
		return ReviewerPermissionPolicy(req.AllowedTools)
	}
	return defaultPermissionPolicy(toolsAllowMutators(req.AllowedTools))
}

// acpAdapterLabel names the adapter behind an ACP spawn for a no-model reason
// (ModelUnavailableFor): "grok acp", "cursor acp", or "acp <binary>" for a peer
// satelle has no name for. Derived from the spawn, never assumed.
func acpAdapterLabel(binary string, args []string) string {
	if adapterOf(binary, args) == HarnessCursor {
		return cursorACPLabel
	}
	if acpEffortArgvSupported(binary, args) {
		return "grok acp"
	}
	return "acp " + filepath.Base(binary)
}

// acpModelFromResult reads the model a peer states it ran from a session/prompt
// result: `_meta.modelId` first (the id the peer names), then the primary entry
// of a `modelUsage` map on `_meta` or `_meta.usage` (grok keys it by build id,
// e.g. "grok-4.7-build"). Empty when the peer reports neither.
func acpModelFromResult(result json.RawMessage) string {
	var r struct {
		Meta struct {
			ModelID    string          `json:"modelId"`
			ModelUsage json.RawMessage `json:"modelUsage"`
			Usage      struct {
				ModelUsage json.RawMessage `json:"modelUsage"`
			} `json:"usage"`
		} `json:"_meta"`
	}
	if len(result) == 0 || json.Unmarshal(result, &r) != nil {
		return ""
	}
	if id := strings.TrimSpace(r.Meta.ModelID); id != "" {
		return id
	}
	for _, mu := range []json.RawMessage{r.Meta.ModelUsage, r.Meta.Usage.ModelUsage} {
		if primary, _, ok := parseModelUsage(mu); ok {
			return primary
		}
	}
	return ""
}

// acpModelFromReply reads a model id off a session/new or session/set_config_option
// reply: `_meta.modelId`, `models.currentModelId` or a `model` config option's
// currentValue. Empty when the reply names none.
func acpModelFromReply(reply json.RawMessage) string {
	var r struct {
		Meta struct {
			ModelID string `json:"modelId"`
		} `json:"_meta"`
		Models struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
		ConfigOptions []struct {
			ID           string `json:"id"`
			CurrentValue string `json:"currentValue"`
		} `json:"configOptions"`
	}
	if len(reply) == 0 || json.Unmarshal(reply, &r) != nil {
		return ""
	}
	if id := strings.TrimSpace(r.Meta.ModelID); id != "" {
		return id
	}
	if id := strings.TrimSpace(r.Models.CurrentModelID); id != "" {
		return id
	}
	for _, o := range r.ConfigOptions {
		if o.ID == "model" && strings.TrimSpace(o.CurrentValue) != "" {
			return strings.TrimSpace(o.CurrentValue)
		}
	}
	return ""
}

// acpUsageFromResult maps `_meta.usage` of a session/prompt response
// (camelCase inputTokens / outputTokens / cachedReadTokens /
// cacheCreationTokens / totalTokens) and the model the peer ran. The model is
// the prompt result's own (acpModelFromResult), else fallbackModel — what
// session/new or set_config_option reported — else the adapter-named no-model
// reason for adapter. Returns nil only when the peer reported neither usage nor
// a model.
func acpUsageFromResult(result json.RawMessage, fallbackModel, adapter string) *UsageResult {
	var u *UsageResult
	var r struct {
		Meta struct {
			Usage map[string]any `json:"usage"`
		} `json:"_meta"`
	}
	if len(result) > 0 && json.Unmarshal(result, &r) == nil && len(r.Meta.Usage) > 0 {
		if got := grokUsageFromMap(r.Meta.Usage); got.Available {
			u = got
		}
	}
	model := acpModelFromResult(result)
	if model == "" {
		model = fallbackModel
	}
	if u == nil {
		if model == "" {
			return nil
		}
		x := acpUnavailableUsage(adapter)
		u = &x
	}
	if model == "" {
		model = noModelReport(adapter)
	}
	u.ModelResolved = model
	return u
}

type acpSession struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	client    *acpClient
	pol       PermissionPolicy
	onEvent   EventHandler
	ev        <-chan Event
	closeEv   func()
	stopHB    func()
	stderrBuf *bytes.Buffer
	wg        sync.WaitGroup
	closeOnce sync.Once
	sendMu    sync.Mutex
	promptErr error
	ctx       context.Context
	// adapter names the ACP peer (acpAdapterLabel) for a no-model reason.
	adapter string
}

func openACPSession(ctx context.Context, a acpRunner, req Request, pol PermissionPolicy) (Session, error) {
	if err := checkDeny(a.binary, a.args, InterfaceACP, req); err != nil {
		return nil, err
	}
	if pol == nil {
		pol = acpPolicyFor(req)
	}
	onEvent, stopHB := eventStream(req)
	args := append([]string(nil), a.args...)
	// Inject --reasoning-effort into spawn ONLY for Grok-shaped ACP peers
	// (sty_aa726901). --reasoning-effort is a Grok CLI flag, not ACP; unknown
	// peers get effort solely via session/set_config_option in handshake. Prefer insertion
	// before trailing "stdio" so `grok agent --reasoning-effort high stdio`.
	if e := strings.TrimSpace(req.Effort); e != "" && acpEffortArgvSupported(a.binary, a.args) {
		injected := false
		for i, t := range args {
			if t == "stdio" {
				args = append(args[:i], append([]string{"--reasoning-effort", e}, args[i:]...)...)
				injected = true
				break
			}
		}
		if !injected {
			args = append(args, "--reasoning-effort", e)
		}
	}
	cmd := exec.CommandContext(ctx, a.binary, args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	cmd.Env = composeEnv(os.Environ(), req.Env, req.Unset...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		stopHB()
		return nil, fmt.Errorf("agentcli: acp: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stopHB()
		return nil, fmt.Errorf("agentcli: acp: stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stopHB()
		return nil, fmt.Errorf("agentcli: acp: stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		ev := newEvent(EventFailed)
		ev.Error = err.Error()
		emitEvent(onEvent, ev)
		stopHB()
		return nil, fmt.Errorf("agentcli: acp: start %s: %w", a.binary, err)
	}

	fanout, evCh, closeEv := fanoutEvents(onEvent)
	emitEvent(fanout, newStartEvent(cmd.Process.Pid))

	var stderrBuf bytes.Buffer
	sess := &acpSession{
		cmd: cmd, stdin: stdin, pol: pol,
		onEvent: fanout, ev: evCh, closeEv: closeEv, stopHB: stopHB,
		stderrBuf: &stderrBuf, ctx: ctx,
		adapter: acpAdapterLabel(a.binary, a.args),
	}
	sess.wg.Add(1)
	go func() {
		defer sess.wg.Done()
		teeEventLines(stderr, &stderrBuf, req.Sink, "[stderr] ", true, commandAdapter{}, fanout)
	}()

	client := newACPClient(stdin, stdout, req.Sink, fanout)
	client.setMutatorsOK(toolsAllowMutators(req.AllowedTools))
	client.setPolicy(pol)
	client.setCapture(req.Capture)
	if req.ReadOnly {
		client.setReviewer(cursorACPGrant(sess.adapter, req.AllowedTools), strings.ReplaceAll(sess.adapter, " ", "/"), req.OnIsolation)
	}
	sess.client = client

	if err := sess.handshake(ctx, req); err != nil {
		sess.promptErr = err
		if closeErr := sess.Close(); closeErr != nil {
			return nil, closeErr
		}
		return nil, fmt.Errorf("agentcli: acp: %w", err)
	}
	return sess, nil
}

func (s *acpSession) Events() <-chan Event { return s.ev }

func (s *acpSession) Captured() []byte {
	if s.client == nil {
		return nil
	}
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if s.promptErr != nil {
		return []byte(s.client.raw.String())
	}
	return []byte(s.client.capturedLocked())
}

func (s *acpSession) Send(ctx context.Context, turn Turn) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	var blocks []map[string]any
	if sp := strings.TrimSpace(turn.System); sp != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": sp})
	}
	if pay := strings.TrimSpace(turn.Text); pay != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": pay})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": "{}"})
	}
	c := s.client
	c.mu.Lock()
	sid := c.session
	c.mu.Unlock()
	result, err := c.request(ctx, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    blocks,
	})
	if err != nil {
		s.promptErr = fmt.Errorf("session/prompt: %w", err)
		return s.promptErr
	}
	c.mu.Lock()
	sessModel := c.model
	c.mu.Unlock()
	if u := acpUsageFromResult(result, sessModel, s.adapter); u != nil {
		ev := newEvent(EventUsage)
		ev.Usage = u
		emitEvent(s.onEvent, ev)
	}
	emitEvent(s.onEvent, newEvent(EventCompleted))
	return nil
}

func (s *acpSession) Cancel() error {
	if s.client != nil {
		return s.client.tryCancel()
	}
	return nil
}

func (s *acpSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.client != nil {
			_ = s.client.tryCancel()
		}
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		var waitErr error
		if s.cmd != nil {
			waitErr = s.cmd.Wait()
		}
		s.wg.Wait()
		if s.stopHB != nil {
			s.stopHB()
		}
		text := s.Captured()
		if s.promptErr != nil {
			ev := newEvent(EventFailed)
			ev.Error = s.promptErr.Error()
			emitEvent(s.onEvent, ev)
			if s.stderrBuf != nil {
				if msg := strings.TrimSpace(s.stderrBuf.String()); msg != "" {
					err = fmt.Errorf("agentcli: acp: %w: %s", s.promptErr, msg)
				} else {
					err = fmt.Errorf("agentcli: acp: %w", s.promptErr)
				}
			} else {
				err = fmt.Errorf("agentcli: acp: %w", s.promptErr)
			}
		} else if waitErr != nil && s.ctx != nil && s.ctx.Err() != nil {
			ev := newEvent(EventFailed)
			ev.Error = s.ctx.Err().Error()
			emitEvent(s.onEvent, ev)
			err = fmt.Errorf("agentcli: acp: %w", s.ctx.Err())
		} else if waitErr != nil && len(bytes.TrimSpace(text)) == 0 {
			ev := newEvent(EventFailed)
			ev.Error = waitErr.Error()
			emitEvent(s.onEvent, ev)
			if s.stderrBuf != nil {
				if msg := strings.TrimSpace(s.stderrBuf.String()); msg != "" {
					err = fmt.Errorf("agentcli: acp: process: %w: %s", waitErr, msg)
				} else {
					err = fmt.Errorf("agentcli: acp: process: %w", waitErr)
				}
			} else {
				err = fmt.Errorf("agentcli: acp: process: %w", waitErr)
			}
		}
		if s.closeEv != nil {
			s.closeEv()
		}
	})
	return err
}

// acpClientCapabilities is what satelle tells a peer it can serve. Every entry
// advertised true must have a handler in acpServedMethods: a peer that plans
// around a capability the client then answers with "method not found" is
// misled. satelle serves none of fs/terminal (a client-side file read would
// bypass the reviewer grant classification handlePermission performs), so all
// are false, matching every recorded real-agent capture.
func acpClientCapabilities() map[string]any {
	return map[string]any{
		"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
		"terminal": false,
	}
}

// acpServedMethods maps each peer-initiated request the client has a dedicated
// handler for to that handler; readLoop dispatches from it. Any other request
// falls to handleUnknownRequest.
var acpServedMethods = map[string]func(c *acpClient, id int64, params json.RawMessage){
	"session/request_permission": (*acpClient).handlePermission,
}

func (s *acpSession) handshake(ctx context.Context, req Request) error {
	c := s.client
	cwd := req.Dir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	initRes, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]any{"name": "satelle", "version": "0"},
		"clientCapabilities": acpClientCapabilities(),
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	// Authenticate only for Grok-shaped methods that reuse an existing CLI
	// session (cached_token / xai.api_key). Agent CLIs (Claude, Grok)
	// own their own login/configuration — satelle never supplies API keys or
	// drives another provider's login flows. Unknown advertised methods are
	// skipped so session/new uses the peer's already-authenticated CLI state.
	var initObj struct {
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	_ = json.Unmarshal(initRes, &initObj)
	methodID := ""
	for _, m := range initObj.AuthMethods {
		if m.ID == "cached_token" || m.ID == "xai.api_key" {
			methodID = m.ID
			break
		}
	}
	if methodID != "" {
		if _, err := c.request(ctx, "authenticate", map[string]any{
			"methodId": methodID,
			"_meta":    map[string]any{"headless": true},
		}); err != nil {
			return fmt.Errorf("authenticate: %w", err)
		}
	}

	// The binding's tools grant is NOT sent here: session/new carries only cwd
	// and mcpServers, so the peer offers every built-in tool (including an
	// interactive Ask). The grant is applied after the fact, through the
	// permission policy (handlePermission), and interactive asks are denied or
	// auto-answered at runtime (sty_32795645).
	sessRes, err := c.request(ctx, "session/new", map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	var sessObj struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(sessRes, &sessObj); err != nil || sessObj.SessionID == "" {
		return fmt.Errorf("session/new: missing sessionId")
	}
	c.mu.Lock()
	c.session = sessObj.SessionID
	c.model = acpModelFromReply(sessRes)
	c.mu.Unlock()
	if req.ReadOnly && s.adapter == cursorACPLabel {
		// cursor enforces read-only by its mode, not by asking: force ask mode (a plan
		// mode reviewer returns a plan, not a verdict) and refuse the run when the
		// peer will not enter it (probe 18).
		if _, err := s.client.request(ctx, "session/set_mode", map[string]any{"sessionId": sessObj.SessionID, "modeId": cursorReadOnlyMode}); err != nil {
			return cursorSetModeError(err)
		}
	} else if req.ReadOnly {
		// A reviewer session should ASK before it runs a tool: a peer that opens in a
		// never-ask (yolo) mode is moved to an ask mode where it offers one. Where
		// it cannot be, the gap is reported and the run continues (sty_2d5e583a).
		if gap := s.enforceAskMode(ctx, sessRes, sessObj.SessionID); gap != "" && req.OnIsolation != nil {
			req.OnIsolation(IsolationNote{Adapter: strings.ReplaceAll(s.adapter, " ", "/"), Detail: gap})
		}
	}

	// Optional model config (sty_a476a2f8). A peer that explicitly rejects the
	// model value fails the run (so a reported model is the model that ran).
	// Peers that do not implement set_config_option (Method not found / unsupported)
	// are a soft miss — log and continue; the binding still spawned with its
	// default model (same as before for those peers).
	if m := strings.TrimSpace(req.Model); m != "" {
		if s.adapter == cursorACPLabel {
			// cursor's model option values are parameterised ("composer-2.5[fast=true]"):
			// a bare name is mapped to the advertised value, an unknown one refused.
			resolved, err := cursorModelValue(sessRes, m)
			if err != nil {
				return err
			}
			m = resolved
		}
		if cfgRes, err := c.request(ctx, "session/set_config_option", map[string]any{
			"sessionId": sessObj.SessionID,
			"configId":  "model",
			"value":     m,
		}); err == nil {
			if id := acpModelFromReply(cfgRes); id != "" {
				c.mu.Lock()
				c.model = id
				c.mu.Unlock()
			}
		} else {
			es := err.Error()
			if strings.Contains(strings.ToLower(es), "method not found") ||
				strings.Contains(strings.ToLower(es), "not supported") ||
				strings.Contains(strings.ToLower(es), "unknown method") {
				_ = es
			} else {
				return fmt.Errorf("session/set_config_option model=%q rejected by peer: %w", m, err)
			}
		}
	}
	if e := strings.TrimSpace(req.Effort); e != "" {
		_, _ = c.request(ctx, "session/set_config_option", map[string]any{
			"sessionId": sessObj.SessionID,
			"configId":  "reasoning_effort",
			"value":     e,
		})
		_, _ = c.request(ctx, "session/set_config_option", map[string]any{
			"sessionId": sessObj.SessionID,
			"configId":  "effort",
			"value":     e,
		})
	}
	// The session's starting model, once, before any prompt: the peer's own
	// id when it named one, else the model this handshake applied. Empty when
	// neither is known — the capture then records explicit unknown.
	c.mu.Lock()
	started := c.model
	c.mu.Unlock()
	if started == "" {
		started = strings.TrimSpace(req.Model)
	}
	ev := newEvent(EventSessionInit)
	ev.Model = started
	emitEvent(s.onEvent, ev)
	return nil
}

// acpModes is the mode block of a session/new reply.
type acpModes struct {
	Modes struct {
		Current   string `json:"currentModeId"`
		Available []struct {
			ID string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
}

// enforceAskMode tries to make a reviewer's ACP peer send session/request_permission
// for a tool it wants to run, and returns the gap text when it cannot ("" when the
// peer is, or was moved to, an ask mode). A peer advertising no modes is a gap:
// satelle can neither confirm nor force ask mode on it. A peer whose current mode
// never asks is switched to an ask mode — "default" when offered, else the first
// mode that is not a skip mode — and is a gap when none exists or the switch is
// rejected. A gap never stops the session: the caller reports it and prompts.
func (s *acpSession) enforceAskMode(ctx context.Context, sessRes json.RawMessage, sessionID string) string {
	var m acpModes
	if json.Unmarshal(sessRes, &m) != nil || (m.Modes.Current == "" && len(m.Modes.Available) == 0) {
		return noAskMode
	}
	if m.Modes.Current != "" && !skipModeName(m.Modes.Current) {
		return ""
	}
	target := ""
	for _, a := range m.Modes.Available {
		if strings.EqualFold(a.ID, "default") {
			target = a.ID
			break
		}
	}
	if target == "" {
		for _, a := range m.Modes.Available {
			if !skipModeName(a.ID) {
				target = a.ID
				break
			}
		}
	}
	if target == "" {
		return fmt.Sprintf("the peer opened in %q, a mode that never asks permission, and offers no ask mode", m.Modes.Current)
	}
	if _, err := s.client.request(ctx, "session/set_mode", map[string]any{"sessionId": sessionID, "modeId": target}); err != nil {
		return fmt.Sprintf("the peer opened in %q, a mode that never asks permission, and refused to switch to %q: %v", m.Modes.Current, target, err)
	}
	return ""
}

// toolsAllowMutators is true when the binding's tools grant includes a write/edit
// or unrestricted shell capability (performer). Read-only grants (reviewer:
// read_file/Read, Bash(satelle:*) only) keep mutators denied.
func toolsAllowMutators(tools string) bool {
	t := strings.ToLower(tools)
	for _, needle := range []string{"write", "edit", "search_replace", "multiedit", "notebookedit", "run_terminal"} {
		if strings.Contains(t, needle) {
			return true
		}
	}
	// Unrestricted Bash / shell — not the satelle-only pull grant.
	if strings.Contains(t, "bash") && !strings.Contains(t, "satelle") {
		return true
	}
	return false
}

// isMutatorToolKind maps ACP ToolKind values that change the tree or run arbitrary code.
func isMutatorToolKind(kind string) bool {
	switch strings.ToLower(kind) {
	case "edit", "delete", "move", "execute":
		return true
	default:
		return false
	}
}

// --- JSON-RPC ACP client ---

type acpClient struct {
	w   io.WriteCloser
	wmu sync.Mutex

	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan acpRPC
	// raw accumulates every agent_message_chunk in order — CaptureFull and
	// diagnostic partials on session/prompt error (byte-identical to pre-
	// sty_844b6ab1 capture; no inserted separators).
	raw strings.Builder
	// cur is the open agent_message run; segments holds closed runs in order.
	// A tool_call / tool_call_update closes cur into segments (sty_844b6ab1).
	// agent_thought_chunk is neither captured nor segmenting.
	cur      strings.Builder
	segments []string
	session  string
	// model is the resolved model id the peer named on session/new or a
	// set_config_option reply; empty when it named none.
	model      string
	mutatorsOK bool
	pol        PermissionPolicy
	sink       io.Writer
	onEvent    EventHandler
	readerDone chan struct{}
	// capture is set from Request.Capture before handshake returns.
	capture CaptureMode

	// Reviewer isolation (sty_ef3efb51). reviewer is set by setReviewer for a
	// ReadOnly request: handlePermission then allows only a tool inside admits,
	// and handleUpdate REPORTS (never cancels — sty_2d5e583a) a tool outside it that
	// executes without a permission ask (a peer in a never-ask mode) or after a deny.
	reviewer bool
	admits   grantAdmits
	calls    map[string]acpToolCall
	asked    map[string]bool
	denied   map[string]bool
	breached map[string]bool // breach keys already reported, so each is reported once
	notify   func(IsolationNote)
	label    string // "<provider>/acp"
}

type acpRPC struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func newACPClient(w io.WriteCloser, r io.Reader, sink io.Writer, onEvent EventHandler) *acpClient {
	c := &acpClient{
		w:          w,
		pending:    map[int64]chan acpRPC{},
		sink:       sink,
		onEvent:    onEvent,
		readerDone: make(chan struct{}),
	}
	go c.readLoop(r)
	return c
}

func (c *acpClient) setMutatorsOK(v bool) {
	c.mu.Lock()
	c.mutatorsOK = v
	c.mu.Unlock()
}

func (c *acpClient) setCapture(m CaptureMode) {
	c.mu.Lock()
	c.capture = m
	c.mu.Unlock()
}

// setReviewer switches the client to reviewer isolation over grant. notify, when
// set, receives each observed breach; the run is never stopped for one.
func (c *acpClient) setReviewer(grant, label string, notify func(IsolationNote)) {
	c.mu.Lock()
	c.reviewer = true
	c.label = label
	c.admits = admitsFromGrant(grant, false)
	c.calls = map[string]acpToolCall{}
	c.asked = map[string]bool{}
	c.denied = map[string]bool{}
	c.breached = map[string]bool{}
	c.notify = notify
	c.mu.Unlock()
}

func (c *acpClient) isReviewer() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reviewer
}

// recordBreachLocked notes one observed breach (once per key) and returns the
// func that reports it. It never cancels the session or stops the peer: a
// breach is recorded and warned, and the run and its verdict stand
// (sty_2d5e583a). Caller holds c.mu and runs the returned func after releasing it.
func (c *acpClient) recordBreachLocked(key, detail string) func() {
	if c.breached[key] {
		return nil
	}
	c.breached[key] = true
	notify, note := c.notify, IsolationNote{Adapter: c.label, Breach: true, Detail: detail}
	if notify == nil {
		return nil
	}
	return func() { notify(note) }
}

func (c *acpClient) setPolicy(pol PermissionPolicy) {
	c.mu.Lock()
	c.pol = pol
	c.mu.Unlock()
}

func (c *acpClient) allowMutators() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mutatorsOK
}

func (c *acpClient) readLoop(r io.Reader) {
	defer close(c.readerDone)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var msg acpRPC
		if err := json.Unmarshal(line, &msg); err != nil {
			if c.sink != nil {
				_, _ = c.sink.Write([]byte(RedactSecrets(string(line)) + "\n"))
			}
			continue
		}
		if c.sink != nil && !isHiddenReasoningLine(line) {
			_, _ = c.sink.Write([]byte(RedactSecrets(string(line)) + "\n"))
		}
		if msg.Method == "session/update" {
			c.handleUpdate(msg.Params)
			continue
		}
		if msg.Method != "" && msg.ID != nil {
			traceACPRequest(msg.Method, *msg.ID, msg.Params)
		}
		if serve, ok := acpServedMethods[msg.Method]; ok && msg.ID != nil {
			serve(c, *msg.ID, msg.Params)
			continue
		}
		if msg.Method != "" && msg.ID != nil {
			// Any other request FROM the peer: never leave it unanswered — an
			// agent waiting on a reply we never send stalls the dispatch.
			c.handleUnknownRequest(*msg.ID, msg.Method, msg.Params)
			continue
		}
		if msg.ID != nil {
			c.mu.Lock()
			ch := c.pending[*msg.ID]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- msg:
				default:
				}
			}
		}
	}
}

func (c *acpClient) handleUpdate(params json.RawMessage) {
	var p struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			ToolCallID    string          `json:"toolCallId"`
			Title         string          `json:"title"`
			Kind          string          `json:"kind"`
			Status        string          `json:"status"`
			RawInput      json.RawMessage `json:"rawInput"`
			ToolName      string          `json:"toolName"`
			Name          string          `json:"name"`
			Meta          struct {
				ToolName string `json:"toolName"`
				Name     string `json:"name"`
			} `json:"_meta"`
			CurrentModeID string `json:"currentModeId"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	su := p.Update.SessionUpdate
	switch su {
	case "agent_message_chunk":
		if p.Update.Content.Text == "" {
			return
		}
		c.mu.Lock()
		c.raw.WriteString(p.Update.Content.Text)
		c.cur.WriteString(p.Update.Content.Text)
		c.mu.Unlock()
		ev := newEvent(EventMessage)
		ev.Text = p.Update.Content.Text
		emitEvent(c.onEvent, ev)
	case "current_mode_update":
		c.mu.Lock()
		var stop func()
		if c.reviewer && skipModeName(p.Update.CurrentModeID) {
			stop = c.recordBreachLocked("mode:"+p.Update.CurrentModeID,
				fmt.Sprintf("the peer switched to %q, a mode that never asks permission, mid-session", p.Update.CurrentModeID))
		}
		c.mu.Unlock()
		if stop != nil {
			stop()
		}
	case "tool_call", "tool_call_update":
		// Close the open message run so post-tool answer is a new segment.
		// agent_thought_chunk deliberately does NOT close — only the tool
		// fence the wire establishes (sty_844b6ab1 AC1).
		c.mu.Lock()
		c.closeSegmentLocked()
		var stop func()
		if c.reviewer {
			stop = c.noteReviewerToolLocked(p.Update.ToolCallID, acpToolCall{
				Kind: p.Update.Kind, Title: p.Update.Title, RawInput: p.Update.RawInput,
				Names: []string{p.Update.ToolName, p.Update.Name, p.Update.Meta.ToolName, p.Update.Meta.Name},
			}, p.Update.Status)
		}
		c.mu.Unlock()
		if stop != nil {
			stop()
		}
		ev := newEvent(EventToolStart)
		if su == "tool_call_update" {
			ev.Kind = EventToolEnd
		}
		ev.Tool = p.Update.Title
		if ev.Tool == "" {
			ev.Tool = p.Update.Kind
		}
		if ev.Tool == "" {
			ev.Tool = p.Update.ToolCallID
		}
		ev.Command = toolCommandRaw(p.Update.RawInput)
		ev.Status = p.Update.Status
		if ev.Status == "" && ev.Kind == EventToolStart {
			ev.Status = "running"
		}
		emitEvent(c.onEvent, ev)
		// agent_thought_chunk and every other sessionUpdate: ignore (not captured,
		// not segmenting). AC7 locks thought exclusion at the capture boundary.
	}
}

// noteReviewerToolLocked folds one tool_call / tool_call_update into the
// per-call record and declares a breach when a tool that is not positively
// identified as inside the grant is running (or has finished) without the peer
// having asked permission, or after satelle denied it. A tool no signal
// identifies is a breach too: only a granted read tool may run unasked. Caller
// holds c.mu.
func (c *acpClient) noteReviewerToolLocked(id string, in acpToolCall, status string) func() {
	if id == "" {
		return nil
	}
	call := c.calls[id]
	if in.Kind != "" {
		call.Kind = in.Kind
	}
	if in.Title != "" {
		call.Title = in.Title
	}
	if len(in.RawInput) > 0 {
		call.RawInput = in.RawInput
	}
	call.Names = append(call.Names, in.Names...)
	c.calls[id] = call
	switch strings.ToLower(status) {
	case "in_progress", "completed":
	default:
		return nil
	}
	class, name := classifyACPCall(call)
	if c.admits.allows(class, name) {
		return nil
	}
	if c.asked[id] && !c.denied[id] {
		return nil
	}
	why := "ran without the peer asking permission"
	if c.denied[id] {
		why = "ran after permission was denied"
	}
	return c.recordBreachLocked("call:"+id,
		fmt.Sprintf("a %s tool call (%s) %s", class, describeCall(call, name), why))
}

func describeCall(call acpToolCall, name string) string {
	if name != "" {
		return name
	}
	if call.Title != "" {
		return SafeText(call.Title)
	}
	return "unnamed"
}

// closeSegmentLocked pushes cur into segments when non-empty and resets cur.
// Caller must hold c.mu.
func (c *acpClient) closeSegmentLocked() {
	if c.cur.Len() == 0 {
		return
	}
	c.segments = append(c.segments, c.cur.String())
	c.cur.Reset()
}

// capturedLocked returns the text for req.Capture after flushing the open run.
// Caller must hold c.mu. answer falls back to raw when segmentation yields
// nothing but raw is non-empty (never return empty when full is non-empty).
func (c *acpClient) capturedLocked() string {
	c.closeSegmentLocked()
	full := c.raw.String()
	if c.capture == CaptureFull {
		return full
	}
	// CaptureAnswer (default): last non-empty segment.
	for i := len(c.segments) - 1; i >= 0; i-- {
		if s := c.segments[i]; s != "" {
			return s
		}
	}
	// Safety: never drop a non-empty stream if segmentation produced nothing.
	return full
}

// noUserAnswer is the fixed reply to an interactive question: no human is
// attached to an isolated or relay dispatch (sty_32795645).
const noUserAnswer = "no user is available; decide from the payload and state your assumption"

// isInteractiveAskTool reports whether a tool's title or kind names an
// interactive ask-the-user tool ("Ask", "Ask: which story?", "AskUserQuestion",
// "ask_user"). Matching is by name shape, never by provider binary.
func isInteractiveAskTool(title, kind string) bool {
	head, _, _ := strings.Cut(title, ":")
	for _, s := range []string{head, kind} {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
		switch b.String() {
		case "ask", "askuser", "askuserquestion", "askquestion", "question", "userquestion":
			return true
		}
	}
	return false
}

// isInteractiveAskMethod reports whether a peer-initiated JSON-RPC method is an
// elicitation or ask-the-user request ("session/elicitation", "_x.ai/ask_user").
func isInteractiveAskMethod(method string) bool {
	m := strings.ToLower(method)
	if strings.Contains(m, "elicit") || strings.Contains(m, "question") {
		return true
	}
	for _, seg := range strings.FieldsFunc(m, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if seg == "ask" || seg == "askuser" || seg == "input" {
			return true
		}
	}
	return false
}

// questionText pulls the question an ask carried from its raw input or params,
// falling back to fallback.
func questionText(raw json.RawMessage, fallback string) string {
	var in struct {
		Question string `json:"question"`
		Prompt   string `json:"prompt"`
		Message  string `json:"message"`
		// Name and Overview title a plan request (cursor/create_plan).
		Name     string `json:"name"`
		Overview string `json:"overview"`
		// Questions is the AskUserQuestion shape: questions[].question.
		Questions []struct {
			Question string `json:"question"`
			// Prompt is the same field in Cursor's cursor/ask_question.
			Prompt string `json:"prompt"`
		} `json:"questions"`
	}
	_ = json.Unmarshal(raw, &in)
	var first string
	if len(in.Questions) > 0 {
		first = in.Questions[0].Question
		if first == "" {
			first = in.Questions[0].Prompt
		}
	}
	for _, s := range []string{in.Question, in.Prompt, in.Message, first, in.Name, in.Overview, fallback} {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// acpTraceEnv names a file that, when set, receives one JSONL line per
// peer-initiated ACP request (method, id, params) so an agent's real request
// shapes can be captured for fixtures. Off by default; params are redacted.
const acpTraceEnv = "SATELLE_ACP_TRACE"

func traceACPRequest(method string, id int64, params json.RawMessage) {
	path := os.Getenv(acpTraceEnv)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(map[string]any{"method": method, "id": id, "params": params})
	if err != nil {
		return
	}
	_, _ = f.Write(append([]byte(RedactSecrets(string(line))), '\n'))
}

// interactiveDeniedEvent builds the EventInteractiveDenied both transports emit.
func interactiveDeniedEvent(tool, question, response string) Event {
	ev := newEvent(EventInteractiveDenied)
	ev.Tool = tool
	ev.Text = SafeText(question)
	ev.Meta = map[string]string{EventMetaResponse: response}
	return ev
}

func (c *acpClient) emitInteractiveDenied(tool, question, response string) {
	emitEvent(c.onEvent, interactiveDeniedEvent(tool, question, response))
}

// askReplyBuilders maps an ask method to the reply shape that method defines.
// A method absent here gets the generic reply.
var askReplyBuilders = map[string]func(params json.RawMessage) any{
	// Grok: the result is tagged by outcome; answers maps each question's text
	// to its answer and must be a JSON object.
	"_x.ai/ask_user_question": func(params json.RawMessage) any {
		var in struct {
			Questions []struct {
				Question string `json:"question"`
			} `json:"questions"`
		}
		_ = json.Unmarshal(params, &in)
		answers := map[string]string{}
		for _, q := range in.Questions {
			answers[q.Question] = noUserAnswer
		}
		return map[string]any{"outcome": "accepted", "answers": answers}
	},
	// Cursor (cursor.com/docs/cli/acp, "cursor/ask_question"): the result is
	// {outcome: {outcome: answered|skipped|cancelled}}. No user is present, so
	// the question is skipped with the reason, never answered on anyone's behalf.
	"cursor/ask_question": func(json.RawMessage) any {
		return map[string]any{"outcome": map[string]any{"outcome": "skipped", "reason": noUserAnswer}}
	},
	// Cursor (cursor.com/docs/cli/acp, "cursor/create_plan"): the result is
	// {outcome: {outcome: accepted|rejected|cancelled}}. No user is present to
	// approve a plan, so it is rejected with the reason.
	"cursor/create_plan": func(json.RawMessage) any {
		return map[string]any{"outcome": map[string]any{"outcome": "rejected", "reason": "no user is available to approve the plan; proceed without plan approval and state your assumptions"}}
	},
}

// askReply builds the auto-answer for an ask method. The generic fallback
// carries both common shapes: an elicitation reads "action", an ask-style
// request reads "answer"/"text".
func askReply(method string, params json.RawMessage) any {
	if build, ok := askReplyBuilders[method]; ok {
		return build(params)
	}
	return map[string]any{"action": "decline", "answer": noUserAnswer, "text": noUserAnswer}
}

// handleUnknownRequest answers a peer-initiated request the client has no
// handler for. An ask/elicitation is auto-answered (declined, with the fixed
// no-user text); anything else gets JSON-RPC "method not found". Either way the
// peer is unblocked. A method with a defined reply in askReplyBuilders is an ask
// whatever its name.
func (c *acpClient) handleUnknownRequest(id int64, method string, params json.RawMessage) {
	if _, defined := askReplyBuilders[method]; !defined && !isInteractiveAskMethod(method) {
		_ = c.respondError(id, -32601, "Method not found: "+method)
		return
	}
	_ = c.respond(id, askReply(method, params))
	c.emitInteractiveDenied(method, questionText(params, ""), "auto-answered")
}

func (c *acpClient) handlePermission(id int64, params json.RawMessage) {
	var p struct {
		ToolCall struct {
			ToolCallID string          `json:"toolCallId"`
			Kind       string          `json:"kind"`
			Title      string          `json:"title"`
			RawInput   json.RawMessage `json:"rawInput"`
			ToolName   string          `json:"toolName"`
			Name       string          `json:"name"`
			Meta       struct {
				ToolName string `json:"toolName"`
				Name     string `json:"name"`
			} `json:"_meta"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(params, &p)

	c.mu.Lock()
	pol := c.pol
	c.mu.Unlock()
	var deny bool
	ask := isInteractiveAskTool(p.ToolCall.Title, p.ToolCall.Kind)
	if ask {
		// No human is attached to any satelle ACP session (the rework relay
		// is agent-to-agent): an interactive question is always denied.
		deny = true
		_, after, _ := strings.Cut(p.ToolCall.Title, ":")
		c.emitInteractiveDenied(p.ToolCall.Title, questionText(p.ToolCall.RawInput, strings.TrimSpace(after)), "denied")
	} else if c.isReviewer() {
		// A reviewer tool is allowed only when a signal on the call POSITIVELY
		// puts it inside the grant. The kind is not trusted alone (a shell tool
		// labelled kind=read), and an unidentified or empty-kind call is denied.
		call := acpToolCall{
			Kind: p.ToolCall.Kind, Title: p.ToolCall.Title, RawInput: p.ToolCall.RawInput,
			Names: []string{p.ToolCall.ToolName, p.ToolCall.Name, p.ToolCall.Meta.ToolName, p.ToolCall.Meta.Name},
		}
		class, name := classifyACPCall(call)
		c.mu.Lock()
		allowed := c.admits.allows(class, name)
		if id := p.ToolCall.ToolCallID; id != "" {
			c.asked[id] = true
			if !allowed {
				c.denied[id] = true
			}
		}
		c.mu.Unlock()
		deny = !allowed
	} else if pol != nil {
		deny = !pol(PermissionRequest{ToolName: p.ToolCall.Kind, Kind: p.ToolCall.Kind}).Allow
	} else {
		deny = isMutatorToolKind(p.ToolCall.Kind) && !c.allowMutators()
	}
	// A deny answers only with a reject option; with none offered it cancels.
	// It never falls back to another option, which may be an allow.
	optionID := ""
	for _, o := range p.Options {
		if deny && (o.Kind == "reject_once" || o.Kind == "reject_always") {
			optionID = o.OptionID
			break
		}
		if !deny && (o.Kind == "allow_once" || o.Kind == "allow_always") {
			optionID = o.OptionID
			break
		}
	}
	if optionID == "" && !deny {
		// No recognised allow kind: take the first option that is not a reject.
		for _, o := range p.Options {
			if o.Kind != "reject_once" && o.Kind != "reject_always" {
				optionID = o.OptionID
				break
			}
		}
	}
	var result any
	if optionID == "" {
		result = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	} else {
		result = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}}
	}
	_ = c.respond(id, result)
}

func (c *acpClient) writeMsg(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = c.w.Write(append(b, '\n'))
	return err
}

func (c *acpClient) respond(id int64, result any) error {
	return c.writeMsg(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (c *acpClient) respondError(id int64, code int, message string) error {
	return c.writeMsg(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

func (c *acpClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan acpRPC, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		msg["params"] = params
	}
	if err := c.writeMsg(msg); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.readerDone:
		return nil, fmt.Errorf("agent closed stdout during %s", method)
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *acpClient) tryCancel() error {
	c.mu.Lock()
	sid := c.session
	c.mu.Unlock()
	if sid == "" {
		return nil
	}
	return c.writeMsg(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params":  map[string]any{"sessionId": sid},
	})
}
