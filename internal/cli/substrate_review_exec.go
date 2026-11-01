package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/substrate"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// substrateReviewTask is the task `satelle substrate review` runs: an embedded
// default, overridden by a same-id file in the repo's tasks dir like any task.
const substrateReviewTask = "tsk_substrate-review"

// substrateReviewStageKind is the ledger telemetry kind each stage of a review
// run is recorded under on its execution.
const substrateReviewStageKind = "substrate-review-stage"

// substrateReviewRun is one review's execution of substrateReviewTask. It is
// in_progress — so in the Activity tab — only while the review runs: it starts
// through the execution route's entry gate and ends through its close gate, or
// at the route's cancel state with the reason recorded (sty_c356ed4f).
type substrateReviewRun struct {
	id     string
	errOut io.Writer
}

// startSubstrateReviewRun creates the execution and moves it to in_progress.
// A run that cannot start is cancelled with the reason and no review runs.
func startSubstrateReviewRun(ctx context.Context, a *app.App, errOut io.Writer) (*substrateReviewRun, error) {
	tasks, err := substrate.ListTasks(filepath.Join(a.ProcessDataDir, "tasks"))
	if err != nil {
		return nil, err
	}
	source := ""
	for _, t := range tasks {
		if t.Name == substrateReviewTask {
			source = t.Source
		}
	}
	if source == "" {
		return nil, fmt.Errorf("substrate review: task %s resolves to no file and no embedded default", substrateReviewTask)
	}
	var exe workitem.Item
	if err := dispatchInto(ctx, "execution-create", map[string]any{
		"title":     "satelle substrate review",
		"body":      "A run of `satelle substrate review`, from task " + source + ".",
		"parent_id": substrateReviewTask,
	}, &exe); err != nil {
		return nil, fmt.Errorf("substrate review: create the run's execution: %w", err)
	}
	r := &substrateReviewRun{id: exe.ID, errOut: errOut}
	fmt.Fprintf(errOut, "execution: %s (task %s)\n", r.id, source)
	if err := r.set(ctx, workitem.StatusInProgress); err != nil {
		return nil, r.fail(ctx, fmt.Errorf("substrate review: the run could not start: %w", err))
	}
	return r, nil
}

// stage records one stage of the run on the execution's ledger. A nil run (the
// judging path is always given one) records nothing.
func (r *substrateReviewRun) stage(ctx context.Context, stage string, data map[string]any) error {
	if r == nil {
		return nil
	}
	d := map[string]any{"stage": stage}
	for k, v := range data {
		d[k] = v
	}
	return dispatchInto(ctx, "story-log", map[string]any{"id": r.id, "kind": substrateReviewStageKind, "data": d}, nil)
}

// reportDoc is the name of the run's report attachment. Every execution of a
// task shares the task's folder, so the name carries the execution id.
func (r *substrateReviewRun) reportDoc() string { return "substrate-review-report-" + r.id }

// finish attaches the report at path and moves the run to done through the
// route's close gate; any failure cancels the run with the reason.
func (r *substrateReviewRun) finish(ctx context.Context, path string) error {
	body, err := os.ReadFile(path)
	if err == nil {
		err = dispatchInto(ctx, "story-doc-attach", map[string]any{
			"story_id": r.id, "name": r.reportDoc(), "type": "output", "body": string(body),
		}, nil)
	}
	if err != nil {
		return r.fail(ctx, fmt.Errorf("substrate review: attach the report to %s: %w", r.id, err))
	}
	if err := r.set(ctx, workitem.StatusDone); err != nil {
		return r.fail(ctx, fmt.Errorf("substrate review: the run's close was refused: %w", err))
	}
	fmt.Fprintf(r.errOut, "execution: %s done\n", r.id)
	return nil
}

// fail records cause as the run's cancel reason, moves it to the route's cancel
// state, and returns cause (joined with any error the cancel itself met).
func (r *substrateReviewRun) fail(ctx context.Context, cause error) error {
	errs := []error{cause}
	if err := r.stage(ctx, workitem.StatusCancelled, map[string]any{"reason": cause.Error()}); err != nil {
		errs = append(errs, fmt.Errorf("record the cancel reason on %s: %w", r.id, err))
	}
	if err := r.set(ctx, workitem.StatusCancelled); err != nil {
		errs = append(errs, fmt.Errorf("cancel %s: %w", r.id, err))
	} else {
		fmt.Fprintf(r.errOut, "execution: %s cancelled — %s\n", r.id, cause)
	}
	return errors.Join(errs...)
}

// set presents a status move through the story-set verb, so the execution
// route's gates judge it as configured.
func (r *substrateReviewRun) set(ctx context.Context, status string) error {
	return dispatchInto(ctx, "story-set", map[string]any{"id": r.id, "status": status}, nil)
}

// dispatchInto runs verb name in-process with req and decodes its answer into
// out (nil discards it).
func dispatchInto(ctx context.Context, name string, req map[string]any, out any) error {
	raw, _ := json.Marshal(req)
	ans, err := verb.Dispatch(ctx, name, raw)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(ans, out)
}
