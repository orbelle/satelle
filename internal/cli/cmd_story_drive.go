package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/drive"
	"github.com/bobmcallan/satelle/internal/verb"
)

// exitParked is the exit code of a drive that stopped because a person must
// decide; 0 is a terminal story and 1 a refusal or failure.
const exitParked = 3

func storyDriveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "drive <id>",
		Short: "Drive a story along its route with no one at a prompt, parking as blocked when a person must decide",
		Long: `Present each next edge through the verb 'story set --status' uses, letting every
step's headless performer run and its gates judge, until the story is terminal.
Refuses (exit 1) a route needing the driving session: an in-loop [executor] on
an executor step, or a named agent bound in-loop. A rejection is relayed to the
performer and rework runs within its budget; when a person must decide, the
story parks at blocked with the objection recorded. Any other end after the first
presentation parks it too, or writes a drive-stopped row.

Exit: 0 terminal, 3 parked, 1 refused or stopped. See 'satelle help agent-dispatch'.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE:        runStoryDrive,
	}
}

func runStoryDrive(cmd *cobra.Command, args []string) error {
	id := strings.TrimSpace(args[0])
	eng, a, err := engineForCmd(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	res, err := drive.Story(cmd.Context(), id, driveOptions(cmd, eng, a, out))
	var stopped *drive.Stopped
	if errors.As(err, &stopped) {
		// The drive ended short of its exit and could not park the story: the stop
		// is on its ledger.
		line := stopped.Error()
		fmt.Fprintln(out, line)
		verb.RecordVerdict(line)
		return &ExitError{Code: 1, Err: errors.New(line), Quiet: true}
	}
	if err != nil {
		return err
	}
	summary := driveSummary(res)
	fmt.Fprintln(out, summary)
	verb.RecordVerdict(summary)
	if res.Parked {
		return &ExitError{Code: exitParked, Err: fmt.Errorf("%s", summary), Quiet: true}
	}
	return nil
}

// driveOptions is what a drive in this process runs with: the engine's performers,
// this machine's identity (so the server can tell this drive's park from a
// person's), and the declared rework relay. `story drive` and a task-kind node's
// wake share it.
func driveOptions(cmd *cobra.Command, eng drive.Performers, a *app.App, out io.Writer) drive.Options {
	location, label := thisLocation(a)
	return drive.Options{
		Performers:    eng,
		Location:      location,
		LocationLabel: label,
		Out:           out,
		Runs:          &lazyRunSource{cfg: a.Config},
	}
}

// driveSummary is the one line a drive ends on.
func driveSummary(res drive.Result) string {
	switch {
	case res.Waiting:
		return fmt.Sprintf("%s waiting at %s: %s", res.ID, res.Status, res.Reason)
	case res.Parked && res.Reason != "":
		return fmt.Sprintf("%s parked at %s: %s", res.ID, res.Status, res.Reason)
	case res.Parked:
		return fmt.Sprintf("%s parked at %s", res.ID, res.Status)
	}
	return fmt.Sprintf("%s %s (terminal)", res.ID, res.Status)
}
