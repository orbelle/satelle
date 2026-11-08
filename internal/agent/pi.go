package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

// pi runs `pi --mode rpc`, started cold (no session, extensions, skills,
// prompt templates, context files or MCP): satelle sends one prompt command,
// reads events until agent_settled, then asks for the last assistant text and
// the session stats, and ends pi's process group. pi has no deny flag, so a
// plain deny is taken out of --tools and a scoped deny on a shell grant is
// refused. The dispatch log gets argv, every RPC line written ("> ") and read
// ("< ") in order, then pi's stderr: the full exchange.
type pi struct{}

func (pi) Name() string         { return PresetPi }
func (pi) EnforcesGrants() bool { return true }

var piColdFlags = []string{"--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--no-mcp", "--no-themes"}

var piTools = map[string][]string{
	ToolRead:  {"read"},
	ToolGrep:  {"grep"},
	ToolGlob:  {"find", "ls"},
	ToolEdit:  {"edit"},
	ToolWrite: {"write"},
	ToolBash:  {"bash"},
}

// piGrace bounds each wait after pi settles.
const piGrace = 2 * time.Second

func (pi) Argv(req Request) ([]string, error) {
	b := req.Binding
	bin, extra, err := commandFields(b)
	if err != nil {
		return nil, err
	}
	plain, scoped := splitDenies(b.Tools, req.denies())
	if len(scoped) > 0 {
		return nil, fmt.Errorf("pi adapter cannot express the scoped deny %s on a grant that runs a shell", strings.Join(scoped, ", "))
	}
	var grant []string
	for _, t := range b.Tools {
		if !slices.Contains(plain, t) {
			grant = append(grant, t)
		}
	}
	tools, err := translate(PresetPi, piTools, grant)
	if err != nil {
		return nil, err
	}
	argv := append([]string{bin}, extra...)
	argv = append(argv, "--mode", "rpc")
	argv = append(argv, piColdFlags...)
	argv = append(argv, "--system-prompt", req.System, "--tools", strings.Join(tools, ","))
	if b.Model != "" {
		argv = append(argv, "--model", b.Model)
	}
	if b.Effort != "" {
		argv = append(argv, "--thinking", b.Effort)
	}
	return argv, nil
}

// piRecord is the union of the pi RPC records read here.
type piRecord struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Success *bool           `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
	Message json.RawMessage `json:"message"`
	Method  string          `json:"method"`
}

type piMessage struct {
	Role          string          `json:"role"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	ResponseModel string          `json:"responseModel"`
	Content       json.RawMessage `json:"content"`
}

func (m piMessage) text() string {
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(m.Content, &blocks) != nil {
		var s string
		_ = json.Unmarshal(m.Content, &s)
		return s
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

func (m piMessage) model() string {
	model := m.ResponseModel
	if model == "" {
		model = m.Model
	}
	if model != "" && m.Provider != "" && !strings.HasPrefix(model, m.Provider+"/") {
		return m.Provider + "/" + model
	}
	return model
}

const (
	piPromptID = "satelle-prompt"
	piTextID   = "satelle-text"
	piStatsID  = "satelle-stats"
)

func (p pi) Run(ctx context.Context, req Request) (Result, error) {
	argv, err := p.Argv(req)
	if err != nil {
		return Result{}, err
	}
	req.log.argv(argv)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = append(os.Environ(), req.Env...)
	procgroup.Set(cmd)
	cmd.WaitDelay = piGrace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("pi: start: %w", err)
	}
	lines := make(chan []byte)
	go func() {
		defer close(lines)
		r := bufio.NewReader(stdout)
		for {
			line, err := r.ReadBytes('\n')
			if t := bytes.TrimSpace(line); len(t) > 0 {
				req.log.line("< ", t)
				lines <- t
			}
			if err != nil {
				return
			}
		}
	}()
	end := func() {
		go func() {
			for range lines {
			}
		}()
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(piGrace):
			_ = procgroup.Kill(cmd)
			<-done
		}
		req.log.output("stderr: ", stderr.Bytes())
	}
	send := func(v map[string]any) error {
		b, _ := json.Marshal(v)
		req.log.line("> ", b)
		_, err := stdin.Write(append(b, '\n'))
		return err
	}

	var res Result
	var lastText string
	var text *string
	settled, gotText, gotStats := false, false, false
	handle := func(line []byte) error {
		var rec piRecord
		if json.Unmarshal(line, &rec) != nil {
			return nil
		}
		ok := rec.Success != nil && *rec.Success
		switch rec.Type {
		case "agent_settled":
			settled = true
		case "message_end":
			var m piMessage
			if json.Unmarshal(rec.Message, &m) == nil && m.Role == "assistant" {
				lastText = m.text()
				if mod := m.model(); mod != "" {
					res.Model = mod
				}
			}
		case "extension_ui_request":
			switch rec.Method {
			case "select", "confirm", "input", "editor":
				_ = send(map[string]any{"type": "extension_ui_response", "id": rec.ID, "cancelled": true})
			}
		case "response":
			switch rec.ID {
			case piPromptID:
				if !ok {
					return fmt.Errorf("pi refused the prompt: %s", rec.Error)
				}
				var d struct{ Disposition string }
				if json.Unmarshal(rec.Data, &d) == nil && d.Disposition == "handled" {
					settled = true
				}
			case piTextID:
				gotText = true
				var d struct{ Text *string }
				if ok && json.Unmarshal(rec.Data, &d) == nil {
					text = d.Text
				}
			case piStatsID:
				gotStats = true
				var d struct {
					Tokens *struct{ Input, Output, CacheRead, CacheWrite int }
				}
				if ok && json.Unmarshal(rec.Data, &d) == nil && d.Tokens != nil {
					res.TokensIn = d.Tokens.Input + d.Tokens.CacheRead + d.Tokens.CacheWrite
					res.TokensOut = d.Tokens.Output
				}
			}
		}
		return nil
	}

	if err := send(map[string]any{"id": piPromptID, "type": "prompt", "message": req.Prompt}); err != nil {
		end()
		return res, fmt.Errorf("pi: send prompt: %w", err)
	}
	for !settled {
		select {
		case <-ctx.Done():
			_ = procgroup.Kill(cmd)
			end()
			return res, fmt.Errorf("pi: %w", ctx.Err())
		case line, open := <-lines:
			if !open {
				end()
				return res, fmt.Errorf("pi exited before it settled: %s", tail(stderr.Bytes(), 2000))
			}
			if err := handle(line); err != nil {
				end()
				return res, err
			}
		}
	}
	_ = send(map[string]any{"id": piTextID, "type": "get_last_assistant_text"})
	_ = send(map[string]any{"id": piStatsID, "type": "get_session_stats"})
	wait := time.After(piGrace)
replies:
	for !gotText || !gotStats {
		select {
		case <-ctx.Done():
			_ = procgroup.Kill(cmd)
			end()
			return res, fmt.Errorf("pi: %w", ctx.Err())
		case <-wait:
			break replies
		case line, open := <-lines:
			if !open {
				break replies
			}
			_ = handle(line)
		}
	}
	end()
	res.Text = lastText
	if text != nil && strings.TrimSpace(*text) != "" {
		res.Text = *text
	}
	if strings.TrimSpace(res.Text) == "" {
		return res, fmt.Errorf("pi settled without an answer: %s", tail(stderr.Bytes(), 2000))
	}
	return res, nil
}
