package remotenode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/drive"
	"github.com/bobmcallan/satelle/internal/runstatus"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_4bf6adbe AC2/AC3: a wake whose executor CLI fails before the performer
// records any findings concludes with findings naming the CLI's own error (the
// JSON result claude -p prints on stdout), and a secret the CLI echoes is
// [REDACTED] in them — on a route that parks and on one that stops.
func TestRunTaskConcludesWithTheExecutorsFailureAsFindings(t *testing.T) {
	const secret = "node-secret-value-9f8e7d6c"
	bin := filepath.Join(t.TempDir(), "agent")
	script := "#!/bin/sh\ncat >/dev/null\n" +
		`echo "{\"type\":\"result\",\"is_error\":true,\"result\":\"Invalid API key (got $NODE_CRED)\"}"` + "\n" +
		`echo "auth failed with $NODE_CRED" >&2` + "\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := agentcli.RunnerFromCommand(bin + " -p --output-format json")
	if err != nil {
		t.Fatal(err)
	}
	failing := executorOnly{step: func(workitem.Item) error {
		_, err := runner.Run(context.Background(), agentcli.Request{
			Payload: "P",
			Env:     map[string]string{"NODE_CRED": secret},
			Redact:  map[string]string{"NODE_CRED": secret},
		})
		return err
	}}

	for _, parkable := range []bool{true, false} {
		e := newTaskEnv(t, parkable)
		log := &statusLog{}
		res, _ := RunTask(context.Background(), taskRun(e, failing, log))
		sent := log.all()
		last := sent[len(sent)-1]
		t.Logf("parkable=%v findings: %s", parkable, last.Findings)
		if last.RunState != runstatus.Concluded || last.Outcome != verb.OutcomeReject {
			t.Fatalf("parkable=%v: last heartbeat = %+v, want concluded/reject", parkable, last)
		}
		for _, want := range []string{"Invalid API key", "exit status 1", "[REDACTED]"} {
			if !strings.Contains(last.Findings, want) {
				t.Errorf("parkable=%v: findings %q lack %q", parkable, last.Findings, want)
			}
		}
		if strings.Contains(last.Findings, secret) || strings.Contains(res.Findings, secret) {
			t.Errorf("parkable=%v: findings carry the secret: %q", parkable, last.Findings)
		}
	}
}

// A park or stop with no error reports the drive's reason, pattern-scrubbed; an
// error wins over the reason.
func TestStopFindingsReadsTheErrorElseTheReason(t *testing.T) {
	if got := stopFindings(nil, drive.Result{Reason: "parked: token=abc"}); got != "parked: token=[REDACTED]" {
		t.Errorf("stopFindings(reason) = %q, want the reason pattern-scrubbed", got)
	}
	if got := stopFindings(errors.New("boom"), drive.Result{Reason: "r"}); got != "boom" {
		t.Errorf("stopFindings(err) = %q, want the error", got)
	}
}
