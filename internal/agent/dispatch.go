package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

type Request struct {
	Binding Binding
	System  string
	Prompt  string
	Deny    []string
	Dir     string
	Env     []string
	Log     string

	promptFile string
	log        *dispatchLog
}

func (r Request) denies() []string { return append(append([]string{}, r.Binding.Deny...), r.Deny...) }

type Result struct {
	Text      string
	TokensIn  int
	TokensOut int
	Model     string
	Command   string
	Duration  time.Duration
}

type Adapter interface {
	Name() string
	EnforcesGrants() bool
	Argv(req Request) ([]string, error)
	Run(ctx context.Context, req Request) (Result, error)
}

const (
	PresetClaude  = "claude"
	PresetPi      = "pi"
	PresetGrok    = "grok"
	PresetCursor  = "cursor-agent"
	PresetSession = "session"
)

func AdapterFor(b Binding) (Adapter, error) {
	bin, _, err := commandFields(b)
	if err != nil {
		return nil, err
	}
	switch preset(bin) {
	case PresetClaude:
		return claude{}, nil
	case PresetPi:
		return pi{}, nil
	case PresetGrok:
		return grok{}, nil
	case PresetCursor:
		return cursor{}, nil
	case PresetSession:
		return session{}, nil
	}
	return nil, fmt.Errorf("command %q names no adapter (%s, %s, %s, %s or %s)", b.Command, PresetClaude, PresetPi, PresetGrok, PresetCursor, PresetSession)
}

func IsSession(b Binding) bool {
	bin, _, err := commandFields(b)
	return err == nil && preset(bin) == PresetSession
}

type Role string

const (
	Perform Role = "perform"
	Judge   Role = "judge"
)

var PerformerDenies = []string{"Bash(satelle story set:*)", "Bash(satelle task set:*)", "Bash(satelle execution set:*)"}

type Dispatcher interface {
	Dispatch(ctx context.Context, role Role, req Request) (Result, error)
}

func CheckJudge(b Binding) error {
	a, err := AdapterFor(b)
	if err != nil {
		return err
	}
	if !IsReadOnly(b.Tools) {
		return fmt.Errorf("agent %q may not judge: its grant %v is not read-only (%s)", b.Name, b.Tools, strings.Join(readTools, " "))
	}
	if !a.EnforcesGrants() {
		return fmt.Errorf("agent %q may not judge: the %s adapter does not enforce tool grants", b.Name, a.Name())
	}
	if _, err := a.Argv(Request{Binding: b}); err != nil {
		return fmt.Errorf("agent %q may not judge: %w", b.Name, err)
	}
	return nil
}

type Runner struct{}

func (Runner) Dispatch(ctx context.Context, role Role, req Request) (Result, error) {
	b := req.Binding
	a, err := AdapterFor(b)
	if err != nil {
		return Result{}, err
	}
	if p := b.Placement; p != "" && p != PlaceLocal {
		return Result{}, fmt.Errorf("agent %q is placed %q, which is not dispatched locally", b.Name, p)
	}
	switch role {
	case Judge:
		if err := CheckJudge(b); err != nil {
			return Result{}, err
		}
	case Perform:
		req.Deny = append(append([]string{}, req.Deny...), PerformerDenies...)
	default:
		return Result{}, fmt.Errorf("unknown role %q", role)
	}
	req.log = openLog(req.Log)
	defer closeLog(req.log, req.Log)
	start := time.Now()
	res, err := a.Run(ctx, req)
	res.Duration = time.Since(start)
	if res.Model == "" {
		res.Model = b.Model
	}
	if res.Command == "" {
		res.Command = b.Command
	}
	return res, err
}

type session struct{}

func (session) Name() string         { return PresetSession }
func (session) EnforcesGrants() bool { return false }
func (session) Argv(Request) ([]string, error) {
	return nil, errors.New("a session binding is performed by the interactive session, not dispatched")
}
func (s session) Run(context.Context, Request) (Result, error) {
	_, err := s.Argv(Request{})
	return Result{}, err
}

func runOnce(ctx context.Context, argv []string, stdin string, req Request) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = append(os.Environ(), req.Env...)
	procgroup.Set(cmd)
	cmd.Cancel = func() error { return procgroup.Kill(cmd) }
	cmd.WaitDelay = 2 * time.Second
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	req.log.argv(argv)
	err := cmd.Run()
	req.log.output("< ", out.Bytes())
	req.log.output("stderr: ", errb.Bytes())
	if ctx.Err() != nil {
		return out.Bytes(), fmt.Errorf("%s: %w", argv[0], ctx.Err())
	}
	if err != nil {
		return out.Bytes(), fmt.Errorf("%s: %w: %s", argv[0], err, tail(errb.Bytes(), 2000))
	}
	return out.Bytes(), nil
}

func tail(b []byte, n int) string {
	b = bytes.TrimSpace(b)
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}
