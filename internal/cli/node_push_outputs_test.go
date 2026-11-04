package cli

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/verb"
)

// sty_ce11bbdb AC2: a chunked push sends each run output in the Apply that
// carries its execution, once, and in no other.
func TestChunkedPushSendsEachOutputWithItsExecution(t *testing.T) {
	fx := newHoldFixture(t, appendOnlyToml)
	run := seedNodeRun(t)
	prev := workstateItemChunk
	workstateItemChunk = 1
	t.Cleanup(func() { workstateItemChunk = prev })

	srv := serveNodeSync(t, fx.f)
	if out, err := runRoot(t, "sync", "workstate", "push", "--server", fx.ts.URL, run.exec, run.subject); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	applies := srv.applies()
	if len(applies) < 2 {
		t.Fatalf("applies = %d, want the items chunked over several", len(applies))
	}
	sent := 0
	for i, req := range applies {
		if len(req.GetOutputs()) == 0 {
			continue
		}
		sent += len(req.GetOutputs())
		with := false
		for _, it := range req.GetItems() {
			with = with || it.GetId() == run.exec
		}
		if !with {
			t.Errorf("apply %d carries the output of %s without the execution", i, run.exec)
		}
	}
	if sent != 1 {
		t.Errorf("outputs sent = %d over %d applies, want the one run output once", sent, len(applies))
	}
}

// sty_ce11bbdb AC2: a run output over what Sync/Apply accepts fails the push in
// the collector, before any RPC.
func TestPushRefusesAnOverlargeRunOutputBeforeAnyRPC(t *testing.T) {
	fx := newHoldFixture(t, appendOnlyToml)
	run := seedNodeRun(t)
	big := strings.Repeat("x", hosted.MaxRunOutputBytes+1)
	if out, err := runRoot(t, "execution", "record", run.exec, "--output", big, "--outcome", verb.OutcomeAccept); err != nil {
		t.Fatalf("record: %v\n%.300s", err, out)
	}

	srv := serveNodeSync(t, fx.f)
	out, err := runRoot(t, "sync", "workstate", "push", "--server", fx.ts.URL, run.exec, run.subject)
	if !errors.Is(err, errRunOutputTooLarge) {
		t.Fatalf("push = %v, want errRunOutputTooLarge\n%.500s", err, out)
	}
	if calls := srv.rec.all(); len(calls) != 0 {
		t.Errorf("RPCs before the refusal = %+v, want none", calls)
	}
}

// sty_ce11bbdb AC3: nodePublish stops at the first Chain/Read, Chain/Append or
// Sync/Apply the server refuses as the node token's or as another project's: the
// named error comes back, not the per-story "not published" count, and nothing
// is sent after the refused call.
func TestNodePublishStopsAtTheFirstRefusal(t *testing.T) {
	for _, method := range []string{"Read", "Append", "Apply"} {
		for _, tc := range []struct {
			code codes.Code
			want error
		}{
			{codes.Unauthenticated, hosted.ErrNodeTokenRefused},
			{codes.NotFound, hosted.ErrNodeProjectNotFound},
		} {
			t.Run(method+"/"+tc.code.String(), func(t *testing.T) {
				newHoldFixture(t, appendOnlyToml)
				run := seedNodeRun(t)
				srv := newNodeSync(t)
				srv.rec.refuse = func(c rpcCall) error {
					if c.Method == method {
						return status.Error(tc.code, "refused")
					}
					return nil
				}
				log, err := runNodePublish(t, nodeClient("http://node.example", "node-tok"), "http://node.example", "probe", run)
				if !errors.Is(err, tc.want) {
					t.Fatalf("publish = %v, want %v\n%s", err, tc.want, log)
				}
				if strings.Contains(err.Error(), "not published") {
					t.Errorf("publish = %v, the per-story count instead of the refusal", err)
				}
				calls := srv.rec.all()
				if len(calls) == 0 || calls[len(calls)-1].Method != method {
					t.Fatalf("calls = %+v, want them to end at the refused %s", calls, method)
				}
				for _, c := range calls[:len(calls)-1] {
					if c.Method == method {
						t.Errorf("calls = %+v, want exactly one %s", calls, method)
					}
				}
				if method != "Apply" && len(srv.applies()) != 0 {
					t.Errorf("Sync/Apply was called after a refused %s", method)
				}
			})
		}
	}
}
