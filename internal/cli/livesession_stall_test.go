package cli

import (
	"context"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// heartbeatOnlySess is a live session whose first turn emits ONE real event
// (a tool start), then goes silent except for heartbeats until cancelled — the
// live-session analogue of agentstep's heartbeatOnlyRunner (sty_752c4ef2 AC4):
// a fake peer that is stuck, not merely slow.
type heartbeatOnlySess struct {
	events  chan agentcli.Event
	onEvent agentcli.EventHandler
	done    chan struct{}
}

func newHeartbeatOnlySess() *heartbeatOnlySess {
	return &heartbeatOnlySess{events: make(chan agentcli.Event, 32), done: make(chan struct{})}
}

func (s *heartbeatOnlySess) emit(ev agentcli.Event) {
	if s.onEvent != nil {
		s.onEvent(ev)
	}
	select {
	case s.events <- ev:
	default:
	}
}

func (s *heartbeatOnlySess) Send(context.Context, agentcli.Turn) error {
	s.emit(agentcli.Event{Kind: agentcli.EventToolStart, Tool: "Bash"})
	go func() {
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-t.C:
				s.emit(agentcli.Event{Kind: agentcli.EventHeartbeat})
			}
		}
	}()
	return nil
}

func (s *heartbeatOnlySess) Events() <-chan agentcli.Event { return s.events }
func (s *heartbeatOnlySess) Cancel() error                 { close(s.done); return nil }
func (s *heartbeatOnlySess) Close() error                  { return nil }
func (s *heartbeatOnlySess) Captured() []byte              { return nil }
