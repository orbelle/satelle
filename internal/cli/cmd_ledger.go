package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
)

// omissionNotice is the one stderr line `ledger list` prints when its limit left
// older rows out. Every number comes from the store's Window; when the matched
// total exceeds what one read can return, raising --limit cannot reach the rest,
// so the hint is to narrow with --kind.
func omissionNotice(w ledger.Window) string {
	hint := fmt.Sprintf("use --limit %d to see them", w.Total)
	if w.Total > w.Max {
		hint = fmt.Sprintf("use --limit %d for more, or narrow with --kind", w.Max)
	}
	return fmt.Sprintf("ledger: showing the newest %d of %d rows; %d older rows omitted — %s",
		w.Limit, w.Total, w.Omitted, hint)
}

func init() {
	ledgerCmd := &cobra.Command{Use: "ledger", Short: "Append to and read the evidence ledger",
		Long: `The evidence ledger: what happened, appended in order and never rewritten.

Gate verdicts, agent invocations and their cost, change records, suite runs. Use
list to read it and append to add an entry; record-run and cite-run carry one
verification run across stories so a sibling need not re-run the suite.`}

	var aStory, aProject, aKind, aActor, aBody string
	appendCmd := &cobra.Command{
		Use:   "append",
		Short: "Append an entry to the ledger",
		Long: `Append one entry to the evidence ledger.

Append-only: there is no edit and no delete, because a record its writer can
revise is not evidence. For a story-scoped telemetry event prefer satelle story
log, which types the data and attaches it to the story.`,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"kind": aKind}
			putIf(req, "story_id", aStory)
			putIf(req, "project_id", aProject)
			putIf(req, "actor", aActor)
			putIf(req, "body", aBody)
			return dispatch(cmd, "ledger-append", req)
		},
	}
	appendCmd.Flags().StringVar(&aStory, "story", "", "story id correlation")
	appendCmd.Flags().StringVar(&aProject, "project", "", "project id correlation")
	appendCmd.Flags().StringVar(&aKind, "kind", "", "entry kind (required)")
	appendCmd.Flags().StringVar(&aActor, "actor", "", "actor")
	appendCmd.Flags().StringVar(&aBody, "body", "", "entry body")
	_ = appendCmd.MarkFlagRequired("kind")

	var lStory, lProject, lKind string
	var lLimit int
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List ledger entries (filter by story, project, or kind)",
		Long: `Read the ledger, filtered by story, project or kind.

This is where a gate's FULL output lives: the route document shows the verdict
and its reasoning and points here for everything the reviewer actually wrote.
Reach for it when the summary is not enough.`,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{}
			putIf(req, "story_id", lStory)
			putIf(req, "project_id", lProject)
			putIf(req, "kind", lKind)
			if lLimit > 0 {
				req["limit"] = lLimit
			}
			req["window"] = true
			body, err := json.Marshal(req)
			if err != nil {
				return fmt.Errorf("encode request: %w", err)
			}
			resp, err := verb.Dispatch(cmd.Context(), "ledger-list", body)
			if err != nil {
				return err
			}
			var w struct {
				Entries json.RawMessage `json:"entries"`
				ledger.Window
			}
			if err := json.Unmarshal(resp, &w); err != nil {
				return fmt.Errorf("ledger list: decode: %w", err)
			}
			if err := renderResponse(cmd, "ledger-list", lStory, w.Entries); err != nil {
				return err
			}
			if w.Omitted > 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), omissionNotice(w.Window))
			}
			return nil
		},
	}
	listCmd.Flags().StringVar(&lStory, "story", "", "filter by story id")
	listCmd.Flags().StringVar(&lProject, "project", "", "filter by project id")
	listCmd.Flags().StringVar(&lKind, "kind", "", "filter by kind")
	listCmd.Flags().IntVar(&lLimit, "limit", 0, fmt.Sprintf(
		"max rows, newest window, oldest first (default %d, max %d)", ledger.DefaultListLimit, ledger.MaxListLimit))

	var vStory string
	verifyCmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify a story's event chain (every story with no --story)",
		Long: `Check that each story's ledger events form one chain: every event's base_id
names an earlier event of the same story, the story has exactly one root, and no
two events share a base unless a chain_fork entry names them.

It also replays each story's transitions, each against the workflow version its
own event recorded (the story's workflow_version events archive them), and
reports a transition whose edge that version does not declare.

Prints one line per break (<story> <event> <kind>: <detail>) and exits non-zero
when any is found; otherwise prints "chain ok" with the counts and exits 0.
Read-only: a broken chain is reported, never repaired.`,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{}
			putIf(req, "story_id", vStory)
			body, err := json.Marshal(req)
			if err != nil {
				return fmt.Errorf("encode request: %w", err)
			}
			resp, err := verb.Dispatch(cmd.Context(), "ledger-verify", body)
			if err != nil {
				return err
			}
			var rep verb.LedgerVerifyReport
			if err := json.Unmarshal(resp, &rep); err != nil {
				return fmt.Errorf("ledger verify: decode: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(rep.Breaks) == 0 && len(rep.ReplayBreaks) == 0 {
				fmt.Fprintf(out, "chain ok (%d stories, %d events)\n", rep.Stories, rep.Events)
				return nil
			}
			for _, b := range rep.Breaks {
				fmt.Fprintf(out, "%s %s %s: %s\n", b.StoryID, b.EventID, b.Kind, b.Detail)
			}
			for _, b := range rep.ReplayBreaks {
				fmt.Fprintf(out, "%s %s replay_%s: %s → %s under workflow %s: %s\n", b.StoryID, b.EventID, b.Reason, b.From, b.To, b.WorkflowHash, b.Detail)
			}
			return &ExitError{Code: 1, Quiet: true,
				Err: fmt.Errorf("ledger verify: %d chain break(s), %d replay break(s) in %d stories, %d events",
					len(rep.Breaks), len(rep.ReplayBreaks), rep.Stories, rep.Events)}
		},
	}
	verifyCmd.Flags().StringVar(&vStory, "story", "", "verify one story's chain (default: every story)")

	ledgerCmd.AddCommand(appendCmd, listCmd, verifyCmd)
	register(ledgerCmd)
}
