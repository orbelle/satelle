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

// Request is one run of a binding.
type Request struct {
	Binding Binding
	System  string   // the skill: the agent's system prompt
	Prompt  string   // the work
	Deny    []string // denied beyond the binding's own deny list
	Dir     string   // working directory
	Env     []string // extra KEY=VALUE entries
}

func (r Request) denies() []string { return append(append([]string{}, r.Binding.Deny...), r.Deny...) }

// Result is what a run produced and what it cost.
type Result struct {
	Text      string
	TokensIn  int
	TokensOut int
	Model     string
	Duration  time.Duration
}

// Adapter runs a binding through one harness.
type Adapter interface {
	Name() string
	// EnforcesGrants reports whether the harness holds the agent to its grant.
	EnforcesGrants() bool
	// Argv returns the command line a run starts; an error when the binding's
	// grant or denies cannot be expressed to the harness.
	Argv(req Request) ([]string, error)
	Run(ctx context.Context, req Request) (Result, error)
}

// Adapter presets.
const (
	PresetClaude  = "claude"
	PresetPi      = "pi"
	PresetGrok    = "grok"
	PresetSession = "session"
)

// AdapterFor chooses the adapter from the basename of the command's first token.
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
	case PresetSession:
		return session{}, nil
	}
	return nil, fmt.Errorf("command %q names no adapter (%s, %s, %s or %s)", b.Command, PresetClaude, PresetPi, PresetGrok, PresetSession)
}

// IsSession reports whether the binding is performed by the interactive session.
func IsSession(b Binding) bool {
	bin, _, err := commandFields(b)
	return err == nil && preset(bin) == PresetSession
}

// Role is why a binding is dispatched.
type Role string

const (
	Perform Role = "perform" // do a state's work
	Judge   Role = "judge"   // judge a gate
)

// PerformerDenies are denied to every dispatched performer: a performer never
// moves an item itself.
var PerformerDenies = []string{"Bash(satelle story set:*)", "Bash(satelle task set:*)", "Bash(satelle execution set:*)"}

// Dispatcher runs bindings. Runner is the real one; tests inject fakes.
type Dispatcher interface {
	Dispatch(ctx context.Context, role Role, req Request) (Result, error)
}

// CheckJudge refuses a binding that may not judge: its grant must be
// read-only and its adapter must enforce grants.
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
	return nil
}

// Runner dispatches bindings through their adapters.
type Runner struct{}

// Dispatch runs one binding in role. A judge that is not read-only on a
// grant-enforcing adapter is refused; a performer always carries
// PerformerDenies. Only local placements are dispatched here.
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
	start := time.Now()
	res, err := a.Run(ctx, req)
	res.Duration = time.Since(start)
	if res.Model == "" {
		res.Model = b.Model
	}
	return res, err
}

// session is the interactive session: it performs in place and is never
// dispatched.
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

// runOnce starts argv, feeds stdin, and returns stdout. The child runs in its
// own process group, killed whole when ctx ends.
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
	err := cmd.Run()
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
