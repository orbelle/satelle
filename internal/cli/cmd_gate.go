package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func gateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Dry-run one configured gate, or compare judges from the ledger",
	}
	cmd.AddCommand(gateRunCmd(), gateReportCmd())
	return cmd
}

func gateRunCmd() *cobra.Command {
	var itemID, from, to string
	var showInput bool
	cmd := &cobra.Command{
		Use:   "run <workflow>/<gate> --item <id>",
		Short: "Run one gate against an item as its transition would, recording nothing",
		Long: `Run one gate of a workflow against an item exactly as presenting its
transition would: the same check env and stdin, or the same judge prompt and
record folder. It prints the gate's result, each panel member's verdict and
the full notes, and writes nothing to the ledger: no gate.run, no hold round.
A judged gate really dispatches its judges.

--from defaults to the item's state and --to to the one transition from it
that runs the gate. --show-input prints what the gate would receive and starts
nothing.

Exit codes: 0 the gate accepted, skipped or carried; 2 it rejected or could
not judge; 1 the command failed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wf, name, ok := strings.Cut(args[0], "/")
			if !ok || wf == "" || name == "" {
				return fmt.Errorf("name the gate as <workflow>/<gate>, not %q", args[0])
			}
			if itemID == "" {
				return fmt.Errorf("name the item with --item <id>")
			}
			r, err := findRepo("")
			if err != nil {
				return err
			}
			k, err := r.openWith("satelle gate run records nothing: it may read the ledger, not write it")
			if err != nil {
				return err
			}
			defer k.Close()
			res, err := k.GateDry(cmd.Context(), kernel.GateDryRequest{
				Workflow: wf, Gate: name, Item: itemID, From: from, To: to, ShowInput: showInput,
			})
			if err != nil {
				return err
			}
			if res.Input != nil {
				printGateInput(cmd.OutOrStdout(), res, *res.Input)
				if res.Input.Kind == gate.InputJudge {
					fmt.Fprintln(cmd.ErrOrStderr(), "the record folder this prompt names existed for this command only and is removed")
				}
				return nil
			}
			return printGateResult(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().StringVar(&itemID, "item", "", "the item to run the gate against")
	cmd.Flags().StringVar(&from, "from", "", "the state the move leaves (default the item's state)")
	cmd.Flags().StringVar(&to, "to", "", "the state the move enters (default the one transition that runs the gate)")
	cmd.Flags().BoolVar(&showInput, "show-input", false, "print what the gate would receive, without running it")
	return cmd
}

func gateReportCmd() *cobra.Command {
	var since, until string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Compare judges per gate and model from the ledger's gate runs, writing nothing",
		Long: `Fold every judged gate.run on the ledger into one row per gate and model:
runs, accepts, rejects, errors, confirmed catches, overturned rejects, the runs
measured, tokens in and out, and the median duration. A panel member counts
under its own model. A run recorded without a model counts under unknown; the
model is never read from agents.toml.

A reject is a confirmed catch when the next accept of the same gate on the same
item judged a different change (a different coverage), and an overturned
reject when it judged the same one; with no later accept it is neither. A panel
member's reject in a run the gate accepted is overturned. Measured runs are the
ones behind the tokens and the median: a panel run recorded before members
carried their own tokens and duration counts in runs and verdicts only.

--since and --until bound the window by the gate.run's time (since inclusive,
until exclusive). Each takes an RFC 3339 time, a YYYY-MM-DD date (local
midnight), or a Go duration back from now (168h). With neither, the whole
ledger is read. The command only reads the ledger.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			now := time.Now()
			from, err := parseWhen(since, now)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			to, err := parseWhen(until, now)
			if err != nil {
				return fmt.Errorf("--until: %w", err)
			}
			r, err := findRepo("")
			if err != nil {
				return err
			}
			k, err := r.openWith("satelle gate report reads the ledger: it writes nothing")
			if err != nil {
				return err
			}
			defer k.Close()
			rows, err := k.JudgeReport(from, to)
			if err != nil {
				return err
			}
			if *asJSON {
				doc := struct {
					Since string            `json:"since,omitempty"`
					Until string            `json:"until,omitempty"`
					Rows  []kernel.JudgeRow `json:"rows"`
				}{Rows: rows}
				if !from.IsZero() {
					doc.Since = from.Format(time.RFC3339)
				}
				if !to.IsZero() {
					doc.Until = to.Format(time.RFC3339)
				}
				return writeJSON(cmd.OutOrStdout(), doc)
			}
			printJudgeRows(cmd.OutOrStdout(), from, to, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "count gate runs at or after this time (RFC 3339, YYYY-MM-DD or a duration back from now)")
	cmd.Flags().StringVar(&until, "until", "", "count gate runs before this time (RFC 3339, YYYY-MM-DD or a duration back from now)")
	asJSON = jsonFlag(cmd)
	return cmd
}

func parseWhen(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, s, time.Local); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("%q is not an RFC 3339 time, a YYYY-MM-DD date or a duration back from now (such as 168h)", s)
}

func printJudgeRows(w io.Writer, since, until time.Time, rows []kernel.JudgeRow) {
	if !since.IsZero() || !until.IsZero() {
		bound := func(t time.Time, none string) string {
			if t.IsZero() {
				return none
			}
			return t.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "window %s to %s\n", bound(since, "the first run"), bound(until, "now"))
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "GATE\tMODEL\tRUNS\tACCEPT\tREJECT\tERROR\tCATCH\tOVERTURNED\tMEASURED\tTOKENS-IN\tTOKENS-OUT\tMEDIAN")
	for _, r := range rows {
		median := "-"
		if r.Measured > 0 {
			median = (time.Duration(r.MedianDurationMS) * time.Millisecond).String()
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n", r.Gate, r.Model, r.Runs, r.Accepts, r.Rejects, r.Errors,
			r.ConfirmedCatches, r.OverturnedRejects, r.Measured, r.TokensIn, r.TokensOut, median)
	}
	tw.Flush()
}

func printGateResult(w io.Writer, res kernel.GateDryResult) error {
	r := *res.Result
	fmt.Fprintf(w, "gate %s  %s  (%s -> %s; dry run, nothing recorded)\n", r.Gate, r.Result, res.From, res.To)
	if r.Tier != "" {
		fmt.Fprintf(w, "  tier %s\n", r.Tier)
	}
	if r.Binding != "" && len(r.Members) == 0 {
		fmt.Fprintf(w, "  judge %s\n", r.Binding)
	}
	for _, m := range r.Members {
		line := fmt.Sprintf("  %s  %s", m.Binding, m.Result)
		if m.Class != "" {
			line += "  " + m.Class
		}
		fmt.Fprintln(w, line)
	}
	if r.Warning != "" {
		fmt.Fprintf(w, "warning: %s\n", r.Warning)
	}
	if r.Notes != "" {
		fmt.Fprintf(w, "\n%s\n", r.Notes)
	}
	if gate.Refuses(r.Result) {
		return &exitError{code: ExitBlocked, msg: fmt.Sprintf("gate %s %s on %s (%s -> %s); nothing recorded", r.Gate, r.Result, res.Item, res.From, res.To), quiet: true}
	}
	return nil
}

func printGateInput(w io.Writer, res kernel.GateDryResult, in gate.Input) {
	fmt.Fprintf(w, "gate %s  %s  (%s -> %s on %s; nothing run)\n", in.Gate, in.Kind, res.From, res.To, res.Item)
	fmt.Fprintf(w, "skill: %s (%s)\n", in.Skill, in.SkillPath)
	fmt.Fprintf(w, "dir: %s\n", in.Dir)
	fmt.Fprintf(w, "timeout: %v (%s)\n", in.Timeout, in.TimeoutSource)
	if in.DiffMaxBytes > 0 {
		fmt.Fprintf(w, "diff_max_bytes: %d (%s)\n", in.DiffMaxBytes, in.DiffBoundSource)
	}
	if in.Placement != "" {
		fmt.Fprintf(w, "placement: %s\n", in.Placement)
	}
	if len(in.Paths) > 0 {
		fmt.Fprintf(w, "paths: %s\n", strings.Join(in.Paths, ", "))
	}
	if in.Carry {
		fmt.Fprintln(w, "carry: an earlier accept over the same change is carried")
	}
	if in.Trigger != nil {
		fmt.Fprintf(w, "\ntrigger %s:\n", in.Trigger.Skill)
		printCheckInput(w, *in.Trigger)
	}
	if in.Check != nil {
		fmt.Fprintln(w)
		printCheckInput(w, *in.Check)
		if in.Head.Commit != "" {
			fmt.Fprintf(w, "head: %s (%s)\n", in.Head.Commit, in.Head.Ref)
		}
		if in.HeadErr != "" {
			fmt.Fprintf(w, "head: none: %s\n", in.HeadErr)
		}
	}
	if in.Kind == gate.InputJudge {
		if in.Tier != "" {
			fmt.Fprintf(w, "tier: %s\n", in.Tier)
		}
		fmt.Fprintf(w, "judges: %s\n", strings.Join(in.Judges, ", "))
		fmt.Fprintf(w, "system: %s\n", in.SkillPath)
		fmt.Fprintf(w, "prompt:\n%s", in.Prompt)
	}
	if len(in.After) > 0 {
		fmt.Fprintf(w, "after: %s (their input adds the gate's verdict, which exists once it has run)\n", strings.Join(in.After, ", "))
	}
}

func printCheckInput(w io.Writer, c gate.CheckInput) {
	fmt.Fprintln(w, "env:")
	for _, v := range c.Env {
		fmt.Fprintf(w, "  %s\n", v)
	}
	fmt.Fprintf(w, "stdin: %s\n", c.Stdin)
}
