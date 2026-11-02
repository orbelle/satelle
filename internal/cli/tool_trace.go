// tool_trace.go — the in-loop run trace the skill-evidence check reads
// (sty_23ceeca2). A dispatched performer's tool calls arrive on its event stream;
// the driving session's arrive only at the PreToolUse hooks, which see exactly
// the tools their per-harness matcher lists (agentcli.HarnessHooks). While the
// engaged step's skills declare commands or tools as evidence, each hook call
// appends one tool_trace row to the engaged story; the check reads the rows since
// the story entered its current step, and reports an item no hook could see as
// unverifiable rather than missing.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
)

// toolTraceCommandMax bounds the command text a trace row keeps: the head is
// what a prefix match needs, and a long heredoc is not worth a ledger row.
const toolTraceCommandMax = 240

// toolTracePayload is a tool_trace row's payload.
type toolTracePayload = verb.ToolTracePayload

// traceSeat is the seat a hook already resolved for its call, so recording does
// not resolve it again. resolved false makes recordToolTrace resolve it on the
// store the call holds.
type traceSeat struct {
	info     seatInfo
	engaged  bool
	resolved bool
}

// recordToolTrace appends the tool call a gate hook ALLOWED to the engaged
// story's ledger (a denied call never ran, so a hook calls this only when it let
// the call through). Best-effort and silent: it never changes a hook's decision,
// writes nothing for a dispatched process (its calls are on its event stream),
// when no story is engaged, or while the engaged step's skills declare no
// evidence a trace could show; a store error is dropped.
//
// gs is the store the hook call already holds and seat the seat it already
// resolved: recording opens no second store and starts no second git lookup.
func recordToolTrace(gs *gateStore, raw []byte, seat traceSeat) {
	if gs == nil || isDispatchedProcess() {
		return
	}
	tool := toolNameFromEvent(raw)
	if tool == "" {
		return
	}
	info, engaged := seat.info, seat.engaged
	if !seat.resolved {
		var err error
		if info, engaged, err = gs.seat(bindSessionID(raw)); err != nil {
			return
		}
	}
	if !engaged || info.ItemID == "" {
		return
	}
	a, err := gs.open()
	if err != nil {
		return
	}
	// A hook makes no network call, so it cannot fetch a story whose history is on
	// the server; a trace appended there would root a chain of its own that no push
	// can publish (sty_00212637).
	if remote, err := a.Store.Ledger.HasMark(context.Background(), info.ItemID, ledger.MarkHistoryRemote); err != nil || remote {
		return
	}
	command := agentcli.CommandFromHookEvent(raw)
	if !stepWantsTrace(context.Background(), a.Store.Ledger, info, command != "") {
		return
	}
	if len(command) > toolTraceCommandMax {
		command = command[:toolTraceCommandMax]
	}
	payload, err := json.Marshal(toolTracePayload{Harness: invokingHarness(raw), Tool: tool, Command: command})
	if err != nil {
		return
	}
	_, _ = a.Store.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: info.ItemID, Kind: ledger.KindToolTrace, Actor: "hook",
		Body:    fmt.Sprintf("%s tool call %s while %s was at %s", invokingHarness(raw), tool, info.ItemID, info.StoryStatus),
		Payload: payload,
	}, time.Now().UTC())
}

// stepWantsTrace reports whether the step the story is performing declares
// evidence a recorded call could show. The step's entry row (skill_delivery) says
// whether its skills declare commands or tools: a shell call is wanted for
// either, any other tool only for a declared tool. A step with no entry row
// declares nothing.
func stepWantsTrace(ctx context.Context, l *ledger.Store, info seatInfo, isShell bool) bool {
	if l == nil {
		return false
	}
	rows, err := l.ListByStory(ctx, info.ItemID, ledger.KindSkillDelivery)
	if err != nil {
		return false
	}
	var latest time.Time
	var commands, tools bool
	for _, e := range rows {
		var p struct {
			To            string `json:"to"`
			TraceCommands bool   `json:"trace_commands"`
			TraceTools    bool   `json:"trace_tools"`
		}
		if json.Unmarshal(e.Payload, &p) != nil || p.To != info.StoryStatus || e.CreatedAt.Before(latest) {
			continue
		}
		latest, commands, tools = e.CreatedAt, p.TraceCommands, p.TraceTools
	}
	if isShell {
		return commands || tools
	}
	return tools
}

// toolTraceResolver reads the in-loop trace for the story's current performing
// step: the tool_trace rows written, and the documents attached, since the story
// last entered status.
func toolTraceResolver() agentstep.ToolTraceFunc { return toolTraceFrom(verb.StoryLedgerKind) }

// toolTraceFrom is toolTraceResolver over any ledger reader.
func toolTraceFrom(rows func(ctx context.Context, itemID, kind string) []ledger.Entry) agentstep.ToolTraceFunc {
	return func(ctx context.Context, itemID, status string) agentstep.ToolTrace {
		var since time.Time
		for _, e := range rows(ctx, itemID, ledger.KindStatusTransition) {
			var p struct {
				To string `json:"to"`
			}
			if json.Unmarshal(e.Payload, &p) == nil && p.To == status && e.CreatedAt.After(since) {
				since = e.CreatedAt
			}
		}
		tr := agentstep.ToolTrace{DocsScoped: true}
		for _, e := range rows(ctx, itemID, ledger.KindStoryDocAttached) {
			var p struct {
				Name string `json:"name"`
			}
			if e.CreatedAt.After(since) && json.Unmarshal(e.Payload, &p) == nil && p.Name != "" {
				tr.Docs = append(tr.Docs, p.Name)
			}
		}
		for _, e := range rows(ctx, itemID, ledger.KindToolTrace) {
			if !e.CreatedAt.After(since) {
				continue
			}
			var p toolTracePayload
			if json.Unmarshal(e.Payload, &p) != nil {
				continue
			}
			if p.Source == verb.ToolTraceStream {
				// A headless run's own stream, with the coverage it had.
				tr.Stream, tr.Harness = true, p.Harness
				tr.StreamCommands, tr.StreamTools, tr.StreamWhy = p.CommandsCovered, p.ToolsCovered, p.Why
				for _, c := range p.Calls {
					tr.Calls = append(tr.Calls, agentstep.ToolCall{Tool: c.Tool, Command: c.Command})
				}
				continue
			}
			if strings.TrimSpace(p.Tool) == "" {
				continue
			}
			if tr.Harness == "" {
				tr.Harness = p.Harness
			}
			tr.Calls = append(tr.Calls, agentstep.ToolCall{Tool: p.Tool, Command: p.Command})
		}
		return tr
	}
}
