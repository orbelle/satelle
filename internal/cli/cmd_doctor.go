package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func doctorCmd() *cobra.Command {
	var asJSON, resolve bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report what is wrong here, each with the command that fixes it; --resolve fixes what it can prove",
		Long: `doctor reports what is wrong with this repo and this machine's satelle, each
issue with the command that fixes it. Without --resolve it changes nothing: no
file, ledger, runtime or git state is written, and it never contacts the
ledger store.

Its checks:
  config     the configuration is invalid (satelle validate)
  substrate  a satelle command, flag, help topic or [[link]] the repo's
             .satelle skills, principles, tasks, workflows or queues name that
             this binary lacks (satelle validate)
  init       files satelle init would add (satelle init)
  versions   the installed satelle CLI, the running satelled and the installed
             satelled disagree (satelle service restart, satelle update)
  sync       the sync timer is missing or not active, or the last automatic
             sync pass failed; the last pass's pushed, pulled and forked items
             are a note (read from the recorded pass, never the store)
  seats      a seat whose session has ended on this machine, on an item in a
             terminal state, or at a worktree of this machine that no longer
             exists (satelle <kind> release <id> --session <session>); a note
             classes every seat as here (this machine, session running),
             elsewhere (another machine), gone or unverified
  check:<s>  each check skill .satelle/doctor.toml names
  open:<n>   each open item a .satelle/doctor.toml [[items]] entry selects

Outside a satelle repo only the machine checks (versions, sync) run. The
session-start hook runs the same checks within ` + doctorHookBudget.String() + ` and prints a short
summary; a check still running then is listed as not checked. doctor exits 0
when it finds nothing and 1 when it finds an issue.

--resolve fixes what doctor can prove and asks about the rest:
  - a missing satelle.service or sync timer, or a timer not active: it runs
    the satelle service install action; satelled not answering or running
    another build than the installed one: satelle service restart;
  - a seat bound on this machine whose session's process has exited: it
    releases it, without asking;
  - every other orphaned seat: it shows the evidence and asks on the
    controlling terminal, release or keep (keep is the default); with no
    terminal it only lists it;
  - config, substrate, init and .satelle/doctor.toml check findings: it
    opens one item listing them all, for the agent to fix in one change
    through the item's workflow (a story tagged substrate-fix, or what
    doctor.toml [resolve] names), and opens no second one while that is open. With an invalid
    configuration no item can be recorded: it lists them, satelle validate
    first.
It never edits or commits files and runs no git command. Everything else is
listed with its fix. --resolve exits 0 when nothing is left and no action
failed, else 1.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if resolve {
				rep := resolveDoctor(cmd.Context(), loadDoctorInput(""))
				if asJSON {
					b, err := json.MarshalIndent(rep, "", "  ")
					if err != nil {
						return err
					}
					fmt.Fprintln(out, string(b))
				} else {
					printResolve(out, rep)
				}
				if len(rep.Left) > 0 || rep.failed() {
					return &exitError{code: ExitError, msg: fmt.Sprintf("%d issue(s) left", len(rep.Left)), quiet: true}
				}
				return nil
			}
			rep := runDoctor(cmd.Context(), loadDoctorInput(""))
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
				if n := resolvable(rep.Issues); n > 0 {
					fmt.Fprintf(out, "satelle doctor --resolve acts on %d of them\n", n)
				}
			}
			if len(rep.Issues) > 0 {
				return &exitError{code: ExitError, msg: fmt.Sprintf("%d issue(s)", len(rep.Issues)), quiet: true}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	cmd.Flags().BoolVar(&resolve, "resolve", false, "fix what doctor can prove, ask about orphaned seats, open one item for the substrate findings")
	return cmd
}

// resolvable counts the issues satelle doctor --resolve acts on.
func resolvable(issues []doctorIssue) int {
	n := 0
	for _, is := range issues {
		if is.Resolve != "" {
			n++
		}
	}
	return n
}
