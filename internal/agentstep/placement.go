package agentstep

import (
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/wfdot"
)

// placementRemote is the value recorded as a cloud dispatch's placement.
const placementRemote = "remote"

// placementDecision is where a step's performer runs, read from its binding's
// declared placement (agents.toml placement = "remote" | "team:<path>").
type placementDecision struct {
	Agent   string
	Remote  bool // placed off this machine (Channel)
	Channel bool // an ephemeral remote paired with this session
	Team    string
	Note    string
	Pinned  bool
	InLoop  bool
	PinErr  error
}

// placementBindings resolves a named agents.toml binding.
type placementBindings func(name string) (config.AgentBinding, bool)

// decidePlacement places a step by its own binding's declared placement. An agent
// with no resolvable binding is the plain local decision.
func decidePlacement(step wfdot.State, bindings placementBindings) placementDecision {
	d := placementDecision{Agent: step.Agent}
	if step.Agent == "" || bindings == nil {
		return d
	}
	b, ok := bindings(step.Agent)
	if !ok {
		return d
	}
	switch kind, path := b.ResolvedPlacement(); kind {
	case config.PlacementRemote:
		d.Remote, d.Channel = true, true
	case config.PlacementTeam:
		d.Team = path
	}
	return d
}
