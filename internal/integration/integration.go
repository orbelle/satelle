package integration

import (
	"slices"

	"github.com/bobmcallan/satelle/internal/integration/agy"
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
	Agy    = agy.Name
)

type provider struct {
	hook     common.HookAdapter
	dispatch common.DispatchAdapter
}

var providers = []provider{
	{claude.Hook{}, claude.Dispatch{}},
	{pi.Hook{}, pi.Dispatch{}},
	{agy.Hook{}, agy.Dispatch{}},
	{grok.Hook{}, grok.Dispatch{}},
	{cursor.Hook{}, cursor.Dispatch{}},
}

func dispatches() []common.DispatchAdapter {
	out := make([]common.DispatchAdapter, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.dispatch)
	}
	return out
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
	var out []string
	for _, d := range dispatches() {
		out = append(out, d.Name())
	}
	return out
}

func Dispatch(preset string) (common.DispatchAdapter, bool) {
	for _, d := range dispatches() {
		if d.Name() == preset {
			return d, true
		}
	}
	return nil, false
}
