package cli

import (
	"context"
	"strings"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/verb"
)

// fakeSess mirrors a transport: every event goes through onEvent SYNCHRONOUSLY
// (the Request.OnEvent path the transcript sink hangs on) before it is queued
// on the buffered Events() channel the render loop drains.
type fakeSess struct {
	turns    []agentcli.Turn
	events   chan agentcli.Event
	onEvent  agentcli.EventHandler
	captured []byte
}

func newFakeSess() *fakeSess {
	return &fakeSess{events: make(chan agentcli.Event, 16)}
}

func (f *fakeSess) emit(ev agentcli.Event) {
	if f.onEvent != nil {
		f.onEvent(ev)
	}
	f.events <- ev
}

func (f *fakeSess) Send(_ context.Context, turn agentcli.Turn) error {
	f.turns = append(f.turns, turn)
	reply := "reply:" + strings.TrimSpace(turn.Text)
	if i := strings.LastIndex(reply, "\n"); i >= 0 {
		reply = "reply:" + strings.TrimSpace(reply[i+1:])
	}
	if strings.Contains(turn.Text, "hello") {
		f.emit(agentcli.Event{Kind: agentcli.EventToolStart, Tool: "read_file", Status: "running"})
		f.emit(agentcli.Event{Kind: agentcli.EventToolEnd, Tool: "read_file", Status: "done"})
	}
	f.emit(agentcli.Event{Kind: agentcli.EventMessage, Text: reply})
	f.emit(agentcli.Event{Kind: agentcli.EventCompleted})
	return nil
}
func (f *fakeSess) Events() <-chan agentcli.Event { return f.events }
func (f *fakeSess) Cancel() error                 { return nil }
func (f *fakeSess) Close() error                  { return nil }
func (f *fakeSess) Captured() []byte              { return f.captured }

type memLedger struct {
	msgs []verb.AgentMessage
	inv  []map[string]string
}

func (m *memLedger) WriteInvocation(tool, kind, decision, decidedBy string) error {
	m.inv = append(m.inv, map[string]string{"tool": tool, "kind": kind, "decision": decision, "by": decidedBy})
	return nil
}

func (m *memLedger) invocationKeys() map[string]bool {
	got := map[string]bool{}
	for _, inv := range m.inv {
		got[inv["tool"]+"|"+inv["decision"]+"|"+inv["by"]] = true
	}
	return got
}
