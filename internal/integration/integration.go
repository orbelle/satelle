package integration

import (
	"slices"

	"github.com/bobmcallan/satelle/internal/integration/claude"
	"github.com/bobmcallan/satelle/internal/integration/common"
	"github.com/bobmcallan/satelle/internal/integration/cursor"
	"github.com/bobmcallan/satelle/internal/integration/grok"
	"github.com/bobmcallan/satelle/internal/integration/pi"
)

const (
	Claude = claude.Name
	Pi     = pi.Name
	Grok   = grok.Name
	Cursor = cursor.Name
)

type provider struct {
	hook     common.HookAdapter
	dispatch common.DispatchAdapter
}

var providers = []provider{
	{claude.Hook{}, claude.Dispatch{}},
	{pi.Hook{}, pi.Dispatch{}},
	{grok.Hook{}, grok.Dispatch{}},
	{cursor.Hook{}, cursor.Dispatch{}},
}

func Names() []string {
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.hook.Name())
	}
	return out
}

func Hooks() []common.HookAdapter {
	out := make([]common.HookAdapter, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.hook)
	}
	return out
}

func Hook(name string) (common.HookAdapter, bool) {
	for _, p := range providers {
		if p.hook.Name() == name {
			return p.hook, true
		}
	}
	return nil, false
}

func Fallback() common.HookAdapter {
	for _, p := range providers {
		if p.hook.Fallback() {
			return p.hook
		}
	}
	return providers[0].hook
}

func IsShellTool(tool string) bool {
	for _, p := range providers {
		if slices.Contains(p.hook.ShellTools(), tool) {
			return true
		}
	}
	return false
}

func Dispatches() []string {
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.dispatch.Name())
	}
	return out
}

func Dispatch(preset string) (common.DispatchAdapter, bool) {
	for _, p := range providers {
		if p.dispatch.Name() == preset {
			return p.dispatch, true
		}
	}
	return nil, false
}
