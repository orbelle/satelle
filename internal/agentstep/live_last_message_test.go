package agentstep

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// liveRows opens a live session as role over a fake opener that emits msgs as
// EventMessages, closes it, and returns the rows it ledgered (open, close).
func liveRows(t *testing.T, role SessionRole, msgs ...string) []map[string]any {
	t.Helper()
	return liveRowsWithEnv(t, role, nil, msgs...)
}

// liveRowsWithEnv is liveRows over a binding carrying env.
func liveRowsWithEnv(t *testing.T, role SessionRole, env map[string]string, msgs ...string) []map[string]any {
	t.Helper()
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "orchestrator" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Interface: "stream", Tools: "Read,Grep,Glob,Bash(satelle:*)", Command: "claude -p {tools}", Env: env}, true
	})
	var rows []map[string]any
	g.SetInvocationRecorder(func(_ context.Context, _ string, payload map[string]any) error {
		rows = append(rows, payload)
		return nil
	})
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			for _, m := range msgs {
				req.OnEvent(agentcli.Event{Kind: agentcli.EventMessage, Text: m})
			}
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAsWithModel(context.Background(), "orchestrator", role,
		workitem.Item{ID: "sty_live", Status: "in_progress"}, nil, nil, "y")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (open, close): %#v", len(rows), rows)
	}
	return rows
}

// A driving (coder) session's close row carries the tail of its last message, so a
// drive that parks over a failed rework relay quotes what the coder last said; the
// open row, written before it spoke, carries none.
func TestLiveSessionCloseCarriesLastMessage(t *testing.T) {
	rows := liveRows(t, SessionRoleDriving, "first thought", strings.Repeat("z", 3000)+"relay says Z")
	if _, ok := rows[0]["last_message"]; ok {
		t.Errorf("the open row carries last_message: %#v", rows[0])
	}
	got, ok := rows[1]["last_message"].(string)
	if !ok || len(got) != verb.LastMessageMax || !strings.HasSuffix(got, "relay says Z") {
		t.Errorf("close row last_message = %d bytes (present %v), want the 2000-byte tail of the second message", len(got), ok)
	}
}

// A session that said nothing still marks its close row a performer's.
func TestLiveSessionCloseWithNoMessageCarriesAnEmptyLastMessage(t *testing.T) {
	rows := liveRows(t, SessionRoleDriving)
	if got, ok := rows[1]["last_message"]; !ok || got != "" {
		t.Errorf("close row last_message = %#v (present %v), want the key present and empty", got, ok)
	}
}

// The consulting reviewer's close row is a reviewer's: no last_message, or a drive
// would quote the consultant's verdict as the performer's last words.
func TestConsultSessionCloseCarriesNoLastMessage(t *testing.T) {
	rows := liveRows(t, SessionRoleConsult, "READY")
	if _, ok := rows[1]["last_message"]; ok {
		t.Errorf("a consult session's close row carries last_message: %#v", rows[1])
	}
}

// A value satelle resolved into the session's environment (a binding env value)
// never reaches the close row: the marker names the key.
func TestLiveSessionCloseScrubsResolvedEnvValues(t *testing.T) {
	rows := liveRowsWithEnv(t, SessionRoleDriving,
		map[string]string{"TOKEN": "LOCAL-token", "DEPLOY_KEY": "run-secret-1"},
		"token is LOCAL-token and key is run-secret-1, flag on")
	got, _ := rows[1]["last_message"].(string)
	if want := "token is [redacted:TOKEN] and key is [redacted:DEPLOY_KEY], flag on"; got != want {
		t.Errorf("close row last_message = %q, want %q", got, want)
	}
}

// The dispatch result keeps the raw capture for the run-output doc but its
// ledger tail is scrubbed of the binding's env.
func TestPerformanceResultLastMessageIsScrubbedButOutputIsRaw(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	binding := config.AgentBinding{Env: map[string]string{"TOKEN": "LOCAL-token", "DEPLOY_KEY": "run-secret-1"}}
	out := "OBSERVED-TOKEN=LOCAL-token key=run-secret-1 ok"
	res := g.performanceResult("coder", "skill", binding, ModelChoice{}, InvokeResult{Stdout: []byte(out)})
	if res.Output != out {
		t.Errorf("Output = %q, want the raw capture %q", res.Output, out)
	}
	if want := "OBSERVED-TOKEN=[redacted:TOKEN] key=[redacted:DEPLOY_KEY] ok"; res.LastMessage != want {
		t.Errorf("LastMessage = %q, want %q", res.LastMessage, want)
	}
}
