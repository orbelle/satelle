package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func gateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Dry-run one configured gate against an item",
	}
	cmd.AddCommand(gateRunCmd())
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
			k, _, err := openKernel()
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

// printGateResult prints a dry run's result; a refusing result exits 2.
func printGateResult(w io.Writer, res kernel.GateDryResult) error {
	r := *res.Result
	fmt.Fprintf(w, "gate %s  %s  (%s -> %s; dry run, nothing recorded)\n", r.Gate, r.Result, res.From, res.To)
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

// printGateInput prints what a gate would receive.
func printGateInput(w io.Writer, res kernel.GateDryResult, in gate.Input) {
	fmt.Fprintf(w, "gate %s  %s  (%s -> %s on %s; nothing run)\n", in.Gate, in.Kind, res.From, res.To, res.Item)
	fmt.Fprintf(w, "skill: %s (%s)\n", in.Skill, in.SkillPath)
	fmt.Fprintf(w, "dir: %s\n", in.Dir)
	fmt.Fprintf(w, "timeout: %v (%s)\n", in.Timeout, in.TimeoutSource)
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
