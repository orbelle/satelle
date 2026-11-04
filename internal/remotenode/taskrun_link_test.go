package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A wake that names a subject leaves one execution_link row on it, naming the
// execution, task, remote and concluded outcome, and a repeat run for the same
// node adds none.
func TestRunTaskWritesOneExecutionLinkOnTheSubject(t *testing.T) {
	e := newTaskEnv(t, false)
	ctx := context.Background()
	perf := executorOnly{step: func(item workitem.Item) error {
		req, _ := json.Marshal(map[string]any{"id": item.ID, "output": "nothing to do", "outcome": "no-op"})
		_, err := verb.Dispatch(ctx, "execution-record", req)
		return err
	}}
	res, err := RunTask(ctx, taskRun(e, perf, &statusLog{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunTask(ctx, taskRun(e, perf, &statusLog{})); err != nil {
		t.Fatal(err)
	}
	rows, err := e.db.Ledger.ListByStory(ctx, "sty_subject", ledger.KindExecutionLink)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("execution_link rows = %d, want exactly 1", len(rows))
	}
	var p ledger.ExecutionLinkPayload
	if err := json.Unmarshal(rows[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	want := ledger.ExecutionLinkPayload{Execution: res.Execution, Task: "tsk_nightly-sweep", Remote: "nod_1", Outcome: verb.OutcomeNoOp}
	if p != want {
		t.Errorf("payload = %+v, want %+v", p, want)
	}
}

// A wake with no subject writes no link row.
func TestRunTaskWithNoSubjectWritesNoExecutionLink(t *testing.T) {
	e := newTaskEnv(t, false)
	ctx := context.Background()
	tr := taskRun(e, executorOnly{}, &statusLog{})
	tr.Desc.StoryID = ""
	if err := tr.linkSubject(ctx, workitem.Item{ID: "exe_x"}, verb.OutcomePerformed); err != nil {
		t.Fatal(err)
	}
	var rows []ledger.Entry
	if err := dispatchJSON(ctx, "ledger-list", map[string]any{"kind": ledger.KindExecutionLink}, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("execution_link rows = %d, want none", len(rows))
	}
}

// The run is published once, after its link row is written, and a refused
// publish does not change the run's outcome.
func TestRunTaskPushesAfterTheLinkRowAndSurvivesARefusedPush(t *testing.T) {
	e := newTaskEnv(t, false)
	ctx := context.Background()
	published := 0
	tr := taskRun(e, executorOnly{}, &statusLog{})
	tr.Publish = func(ctx context.Context, exec workitem.Item, subject string) (PublishReport, error) {
		published++
		rows, _ := e.db.Ledger.ListByStory(ctx, subject, ledger.KindExecutionLink)
		if len(rows) != 1 || subject != "sty_subject" {
			t.Errorf("at publish: link rows = %d, subject = %q, want the link row written first", len(rows), subject)
		}
		return PublishReport{}, context.DeadlineExceeded
	}
	res, err := RunTask(ctx, tr)
	if err != nil || published != 1 || res.Outcome != verb.OutcomePerformed {
		t.Fatalf("result = %+v, err = %v, publishes = %d, want performed after exactly one publish", res, err, published)
	}
}

// A fetch satelle.dev refused (ErrRunRefused) stops the run before its execution
// is made: nothing is written, nothing published, and the run concludes rejected.
func TestRunTaskStopsOnARefusedFetch(t *testing.T) {
	e := newTaskEnv(t, false)
	ctx := context.Background()
	var fetched []string
	published := 0
	tr := taskRun(e, executorOnly{}, &statusLog{})
	tr.Fetch = func(_ context.Context, ids ...string) error {
		fetched = append(fetched, ids...)
		return fmt.Errorf("%w: node token refused", ErrRunRefused)
	}
	tr.Publish = func(context.Context, workitem.Item, string) (PublishReport, error) {
		published++
		return PublishReport{}, nil
	}
	res, err := RunTask(ctx, tr)
	if !errors.Is(err, ErrRunRefused) || res.Execution != "" || res.Outcome != verb.OutcomeReject {
		t.Fatalf("result = %+v, err = %v, want a rejected run with no execution and the refusal", res, err)
	}
	if len(fetched) != 1 || fetched[0] != "sty_subject" || published != 0 {
		t.Errorf("fetched %v, published %d times, want only the subject's fetch and no publish", fetched, published)
	}
	if rows, _ := e.db.Ledger.ListByStory(ctx, "sty_subject", ledger.KindExecutionLink); len(rows) != 0 {
		t.Errorf("link rows = %d after a refused fetch, want none", len(rows))
	}
}
