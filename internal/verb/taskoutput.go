package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func init() {
	Register(&Verb{
		Name:        "execution-record",
		Description: "Record a task execution's run output as an OKF document under its task folder",
		Invoke:      executionRecord,
	})
}

// okfRunOutputType is the OKF `type` carried by a run-output document.
const okfRunOutputType = "task-execution-output"

// The outcomes a run's performer or validator may record on its run-output doc:
// the same vocabulary satelle-server accepts for a step verdict. The binary
// computes none of them; "no-op" is how the task's own substrate says there was
// nothing to do.
const (
	OutcomeAccept    = "accept"
	OutcomeReject    = "reject"
	OutcomePerformed = "performed"
	OutcomeNoOp      = "no-op"
)

// ValidOutcome reports whether o is one of the recordable outcomes.
func ValidOutcome(o string) bool {
	switch o {
	case OutcomeAccept, OutcomeReject, OutcomePerformed, OutcomeNoOp:
		return true
	}
	return false
}

// runOutputPath is where an execution's run-output doc lives.
func runOutputPath(exec workitem.Item) string {
	return filepath.Join(execDir(exec.ParentID), runOutputFileName(exec.ID))
}

// RunOutputDoc is exec's run-output doc: its file name and its text. ok is false
// when no doc was written.
func RunOutputDoc(exec workitem.Item) (name, body string, ok bool) {
	if taskDir == "" || exec.ParentID == "" {
		return "", "", false
	}
	raw, err := os.ReadFile(runOutputPath(exec))
	if err != nil {
		return "", "", false
	}
	return runOutputFileName(exec.ID), string(raw), true
}

// RunOutcome reads the outcome recorded on exec's run-output doc: the one record
// a run's result is read from. It is "" when no doc was written or the doc
// records no outcome.
func RunOutcome(exec workitem.Item) string {
	front, _ := readRunOutput(exec)
	const key = "outcome: "
	for _, ln := range strings.Split(front, "\n") {
		if strings.HasPrefix(ln, key) {
			return strings.TrimSpace(strings.TrimPrefix(ln, key))
		}
	}
	return ""
}

// RunFindings reads what a run recorded on exec's run-output doc: the text below
// its front matter and its generated heading, which is what the performer wrote.
// It is "" when no doc was written. It is read from the same record RunOutcome
// reads, so a remote reports the outcome and the findings behind it together.
func RunFindings(exec workitem.Item) string {
	_, body := readRunOutput(exec)
	body = strings.TrimSpace(body)
	if rest, ok := strings.CutPrefix(body, "# Run output"); ok {
		_, body, _ = strings.Cut(rest, "\n")
	}
	return strings.TrimSpace(body)
}

// readRunOutput splits exec's run-output doc into its front matter and its body.
// Both are "" when there is no doc, or it has no front matter.
func readRunOutput(exec workitem.Item) (front, body string) {
	if taskDir == "" || exec.ParentID == "" {
		return "", ""
	}
	raw, err := os.ReadFile(runOutputPath(exec))
	if err != nil {
		return "", ""
	}
	rest, ok := strings.CutPrefix(string(raw), "---\n")
	if !ok {
		return "", ""
	}
	front, body, ok = strings.Cut(rest, "\n---\n")
	if !ok {
		return "", ""
	}
	return front, body
}

// runOutputFileName is the per-run output doc name under the parent task folder.
// The execution id is unique per run, so a new run never overwrites a prior run's
// output (sty_890b86cb). The `output-` prefix keeps it out of the exe_*.md
// execution ingest walked by SyncTasks.
func runOutputFileName(execID string) string { return "output-" + execID + ".md" }

// recordRunOutput writes a task execution's run OUTPUT as an OKF document under
// its parent task's folder (.satelle/tasks/<tsk_id>/output-<exe_id>.md), carrying
// OKF frontmatter (required `type` + the `generated: satelle` marker) so a run's
// evidence is discoverable per task rather than only in the central
// executor.log (sty_890b86cb). This is the SINGLE write seam both capture paths
// use — the DISPATCHED path (the named agent's stdout written through
// mechanically from the dispatch site) and the IN-LOOP path (the
// execution-record verb the orchestrator calls). No-op when taskDir is unset;
// idempotent for the same run. A recorded outcome is written into the doc's
// frontmatter; a record that carries none keeps the outcome already on the doc, so
// a performer that records its output after its outcome does not lose it.
func recordRunOutput(exec workitem.Item, output, outcome string, now time.Time) (string, error) {
	if taskDir == "" {
		return "", nil
	}
	if exec.Kind != workitem.KindExecution || exec.ParentID == "" {
		return "", fmt.Errorf("record: %s is not an execution with a parent task", exec.ID)
	}
	dir := execDir(exec.ParentID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("record: run-output dir: %w", err)
	}
	if outcome == "" {
		outcome = RunOutcome(exec)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntype: %s\ngenerated: satelle\nexecution: %s\ntask: %s\ntimestamp: %s\n",
		okfRunOutputType, exec.ID, exec.ParentID, now.UTC().Format(time.RFC3339))
	if outcome != "" {
		fmt.Fprintf(&b, "outcome: %s\n", outcome)
	}
	fmt.Fprintf(&b, "---\n\n# Run output — %s\n\n%s\n", exec.ID, strings.TrimRight(output, "\n"))
	path := filepath.Join(dir, runOutputFileName(exec.ID))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("record: write run output: %w", err)
	}
	return path, nil
}

// executionRecordReq carries the execution id and the run output to record. The
// in-loop orchestrator calls this as its final act of the run (the task
// workflow's exit-gate rubric instructs it to).
type executionRecordReq struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	// Outcome is optional: one of the Outcome* values, which the run's result is
	// read as (a remote node reports it).
	Outcome string `json:"outcome,omitempty"`
}

func executionRecord(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	var req executionRecordReq
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, fmt.Errorf("verb: id required")
	}
	if req.Outcome != "" && !ValidOutcome(req.Outcome) {
		return nil, fmt.Errorf("verb: outcome %q must be accept, reject, performed or no-op", req.Outcome)
	}
	exec, err := store.Get(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	path, werr := recordRunOutput(exec, req.Output, req.Outcome, now)
	if werr != nil {
		return nil, werr
	}
	// The ledger row mirrors the outcome for history; the run-output doc is the
	// record it is read from.
	note := "recorded run output"
	if req.Outcome != "" {
		note += " (outcome " + req.Outcome + ")"
	}
	appendLedger(ctx, req.ID, ledger.KindTaskUpdated, note, now)
	notifyChange(TopicTasks)
	return json.Marshal(map[string]string{"execution": req.ID, "path": path})
}
