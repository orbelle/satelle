// Package cli is satelle's command line: a thin cobra layer over the kernel.
// Commands parse flags, call one kernel operation and print its result; no
// command names or decides anything only one workflow has.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/buildinfo"
)

// Exit codes.
const (
	ExitError   = 1 // the command failed
	ExitBlocked = 2 // a transition was presented and a gate rejected it
)

// exitError carries an exit code; its message is printed unless quiet.
type exitError struct {
	code  int
	msg   string
	quiet bool
}

func (e *exitError) Error() string { return e.msg }

// ExitCodeOf maps an Execute error to the process exit code.
func ExitCodeOf(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return ExitError
}

// NewRootCmd returns the satelle command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "satelle",
		Short: "satelle runs your workflow's configuration: items, gates, performers and a ledger",
		Long: `satelle is a stateless, generic CLI. Your workflows, gates, performers and
agents live as configuration under .satelle/; satelle runs them and records
every event on an append-only ledger. Start with 'satelle init' and
'satelle help'.`,
		Version:       buildinfo.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("satelle {{.Version}}\n")
	root.AddCommand(
		initCmd(), validateCmd(), statusCmd(), versionCmd(),
		itemCmd("story", true), itemCmd("task", true), itemCmd("execution", false),
		ledgerCmd(), workflowCmd(), hookCmd(), agentsCmd(),
		loginCmd(), logoutCmd(), whoamiCmd(), projectCmd(), syncCmd(),
		serviceCmd(), workspaceCmd(), runtimeCmd(), logsCmd(),
	)
	root.SetHelpCommand(helpCmd(root))
	return root
}

// Execute runs the command line.
func Execute() error {
	root := NewRootCmd()
	err := root.Execute()
	if err != nil {
		var ee *exitError
		if !errors.As(err, &ee) || !ee.quiet {
			fmt.Fprintln(root.ErrOrStderr(), "satelle:", strings.TrimPrefix(err.Error(), "satelle: "))
		}
		if strings.HasPrefix(err.Error(), "unknown command") {
			fmt.Fprintln(root.ErrOrStderr(), "\n"+root.UsageString())
		}
	}
	return err
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the satelle version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "satelle %s\n", buildinfo.Version)
			return nil
		},
	}
}

// writeJSON prints v as indented JSON.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// jsonFlag adds --json to cmd and returns its value.
func jsonFlag(cmd *cobra.Command) *bool {
	return cmd.Flags().Bool("json", false, "print JSON")
}

// actor is who an event is recorded as: the session when one is named, else
// the OS user.
func actor() string {
	if s := sessionFromEnv(); s != "" {
		return s
	}
	for _, k := range []string{"USER", "USERNAME"} {
		if u := strings.TrimSpace(os.Getenv(k)); u != "" {
			return u
		}
	}
	return "cli"
}
