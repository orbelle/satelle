package agentstep

import (
	"bytes"
	"context"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// One isolation path for every role=reviewer dispatch (sty_ef3efb51): the step
// summariser and the rework consultant's live session are refused, trimmed and
// recorded exactly like a gate verdict.

const summaryRO = "Read,Grep,Glob"

type liveReviewer struct {
	opens  int
	req    agentcli.Request
	pol    agentcli.PermissionPolicy
	rows   []map[string]any
	events *[]telemetryRec
	warn   *bytes.Buffer
}

// openConsult opens a role=reviewer consult session over a fake opener and
// reports what the opener saw.
func openConsult(t *testing.T, iface, command, tools string, caller agentcli.PermissionPolicy) (*liveReviewer, error) {
	t.Helper()
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Role: config.RoleReviewer, Interface: iface, Command: command, Tools: tools}, true
	})
	lr := &liveReviewer{events: captureTelemetry(g), warn: &bytes.Buffer{}}
	g.SetWarnWriter(lr.warn)
	g.SetInvocationRecorder(func(_ context.Context, _ string, p map[string]any) error {
		lr.rows = append(lr.rows, p)
		return nil
	})
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, pol agentcli.PermissionPolicy) (agentcli.Session, error) {
			lr.opens++
			lr.req, lr.pol = req, pol
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAsWithModel(context.Background(), "reviewer-consult", SessionRoleConsult,
		workitem.Item{ID: "sty_live", Status: "in_progress"}, caller, nil, "")
	if err == nil {
		_ = sess.Close()
	}
	return lr, err
}

// A stock claude stream consult still opens: the request is read-only, the open
// row records the offered tools, and the caller's own policy is narrowed by the
// grant, never widened.
func TestOpenSession_StockClaudeConsultRunsReadOnlyAndRecords(t *testing.T) {
	var callerSaw int
	caller := func(agentcli.PermissionRequest) agentcli.PermissionDecision {
		callerSaw++
		return agentcli.PermissionDecision{Allow: true}
	}
	lr, err := openConsult(t, "stream", agentcli.DefaultClaudeStreamCommand, summaryRO, caller)
	if err != nil {
		t.Fatalf("stock claude consult refused: %v", err)
	}
	if lr.opens != 1 || !lr.req.ReadOnly {
		t.Fatalf("opens = %d, ReadOnly = %v; want one read-only open", lr.opens, lr.req.ReadOnly)
	}
	var open map[string]any
	for _, r := range lr.rows {
		if r["phase"] == "open" {
			open = r
		}
	}
	if open == nil || open["offered_tool_count"] != 3 || open["offered_tools_source"] != agentcli.OfferedSourceFlag {
		t.Fatalf("open row = %+v, want offered_tool_count 3 from the flag", open)
	}
	if lr.pol(agentcli.PermissionRequest{ToolName: "Read", Kind: "read"}).Allow != true {
		t.Error("a granted read tool must be allowed")
	}
	if callerSaw == 0 {
		t.Error("the caller's own policy must still be consulted")
	}
	for _, tool := range []string{"Bash", "Write", "Edit", "WebFetch", "mcp__x__y"} {
		if lr.pol(agentcli.PermissionRequest{ToolName: tool}).Allow {
			t.Errorf("tool %q outside the grant was allowed although the caller's policy allows everything", tool)
		}
	}
}

// A role=agent live session is not a reviewer dispatch: no preflight, no trim.
func TestOpenSession_PerformerSessionIsNotIsolated(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Role: config.RoleAgent, Interface: "acp", Command: "grok agent stdio", Tools: "Read,Edit"}, true
	})
	var req agentcli.Request
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, r agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			req = r
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAs(context.Background(), "coder", SessionRoleDriving,
		workitem.Item{ID: "sty_live", Status: "in_progress"}, nil, nil)
	if err != nil {
		t.Fatalf("performer session refused: %v", err)
	}
	_ = sess.Close()
	if req.ReadOnly {
		t.Error("a performer session must not be marked read-only")
	}
}
