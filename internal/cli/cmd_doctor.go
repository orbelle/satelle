package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func doctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report what is wrong here, each with the command that fixes it (read-only)",
		Long: `doctor reports what is wrong with this repo and this machine's satelle, each
issue with the command that fixes it. It changes nothing: no file, ledger,
runtime or git state is written, and it never contacts the ledger store.

Its checks:
  config     the configuration is invalid (satelle validate)
  init       files satelle init would add (satelle init)
  versions   the installed satelle CLI, the running satelled and the installed
             satelled disagree (satelle service restart, satelle update)
  sync       the sync timer is missing or not active, or the last automatic
             sync pass failed; the last pass's pushed, pulled and forked items
             are a note (read from the recorded pass, never the store)
  seats      a seat on an item in a terminal state, or at a worktree that no
             longer exists (satelle <kind> release <id> --session <session>)
  check:<s>  each check skill .satelle/doctor.toml names
  open:<n>   each open item a .satelle/doctor.toml [[items]] entry selects

Outside a satelle repo only the machine checks (versions, sync) run. The
session-start hook runs the same checks within ` + doctorHookBudget.String() + ` and prints a short
summary; a check still running then is listed as not checked. doctor exits 0
when it finds nothing and 1 when it finds an issue.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep := runDoctor(cmd.Context(), loadDoctorInput(""))
			out := cmd.OutOrStdout()
			if asJSON {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(out, string(b))
			} else {
				for _, is := range rep.Issues {
					fmt.Fprintf(out, "%s: %s\n  fix: %s\n", is.Check, is.Problem, is.Fix)
				}
				for _, n := range rep.Notes {
					fmt.Fprintf(out, "note: %s\n", n)
				}
				for _, u := range rep.Unchecked {
					fmt.Fprintf(out, "not checked: %s\n", u)
				}
				if len(rep.Issues) == 0 {
					fmt.Fprintln(out, "no issues")
				} else {
					fmt.Fprintf(out, "%d issue(s)\n", len(rep.Issues))
				}
			}
			if len(rep.Issues) > 0 {
				return &exitError{code: ExitError, msg: fmt.Sprintf("%d issue(s)", len(rep.Issues)), quiet: true}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}
