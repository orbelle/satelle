package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/integration/common"
	"github.com/bobmcallan/satelle/internal/ledger"
)

type (
	Request = common.Request
	Result  = common.Result
	Adapter = common.DispatchAdapter
)

const PresetSession = "session"

func AdapterFor(b Binding) (Adapter, error) {
	bin, _, err := common.CommandFields(b)
	if err != nil {
		return nil, err
	}
	p := common.Preset(bin)
	if p == PresetSession {
		return session{}, nil
	}
	if a, ok := integration.Dispatch(p); ok {
		return a, nil
	}
	names := append(integration.Dispatches(), PresetSession)
	return nil, fmt.Errorf("command %q names no adapter (%s or %s)", b.Command, strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}

func IsSession(b Binding) bool {
	bin, _, err := common.CommandFields(b)
	return err == nil && common.Preset(bin) == PresetSession
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
		return fmt.Errorf("agent %q may not judge: its grant %v is not read-only (%s)", b.Name, b.Tools, strings.Join(common.ReadTools, " "))
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
	var worker string
	switch role {
	case Judge:
		if err := CheckJudge(b); err != nil {
			return Result{}, err
		}
		worker = ledger.RoleJudge
	case Perform:
		req.Deny = append(append([]string{}, req.Deny...), PerformerDenies...)
		worker = ledger.RolePerformer
	default:
		return Result{}, fmt.Errorf("unknown role %q", role)
	}
	req.Env = append(append([]string{}, req.Env...), config.WorkerEnv+"="+worker)
	req.Trace = common.OpenLog(req.Log)
	defer closeLog(req.Trace, req.Log)
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
