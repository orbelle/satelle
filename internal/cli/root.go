// Package cli is satelle's command line: a thin cobra layer over the kernel.
// Commands parse flags, call one kernel operation and print its result; no
// command names or decides anything only one workflow has.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

// Exit codes.
const (
	ExitError    = 1 // the command failed
	ExitBlocked  = 2 // a gate rejected a presented transition, or the kernel refused a move for the evidence on the item's chain
	ExitBlobGaps = 3 // satelle ledger verify: every chain intact, a referenced blob missing or corrupt
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
		Version:       buildinfo.Resolve().String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	// --session and -C are root flags, so every command takes them, before or
	// after its name. Neither is bound to a variable: a tree built inside a
	// running command (doctor, validate) writes nothing (sto_2a41b9e4).
	root.PersistentFlags().String(sessionFlag, "", "the session this command acts as (default $"+config.SessionEnv+")")
	root.PersistentFlags().StringP(directoryFlag, "C", "", "run as if satelle was started in this directory, like git -C")
	root.PersistentPreRunE = rootPreRun
	root.AddCommand(
		initCmd(), validateCmd(), statusCmd(), versionCmd(), doctorCmd(),
		itemCmd("story", true), itemCmd("task", true), itemCmd("execution", false), itemCmd("queue", true),
		ledgerCmd(), workflowCmd(), hookCmd(), agentsCmd(),
		loginCmd(), logoutCmd(), whoamiCmd(), projectCmd(), remoteCmd(), nodeCmd(),
		serviceCmd(), workspaceCmd(), runtimeCmd(), logsCmd(), updateCmd(), settingsCmd(), upgradeCmd(), githubCmd(),
	)
	root.SetHelpCommand(helpCmd(root))
	return root
}

// The root flags' names.
const (
	sessionFlag   = "session"
	directoryFlag = "directory"
)

// rootPreRun runs once, on the executed command, after its flags are parsed
// and before it runs: it changes into -C's directory, then makes a non-empty
// --session the call's SATELLE_SESSION, the one carrier actor(), the hooks, a
// gate's check script and a dispatched performer already read. An empty
// --session leaves the environment as it is, so the flag never makes a call
// look like the operator's.
func rootPreRun(cmd *cobra.Command, _ []string) error {
	flags := cmd.Root().PersistentFlags()
	if dir, _ := flags.GetString(directoryFlag); dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("-C %s: %w", dir, err)
		}
	}
	if s := strings.TrimSpace(flagString(flags, sessionFlag)); s != "" {
		if err := os.Setenv(config.SessionEnv, s); err != nil {
			return err
		}
	}
	return nil
}

// flagString is a string flag's value, "" when it is not defined.
func flagString(fs *pflag.FlagSet, name string) string {
	v, _ := fs.GetString(name)
	return v
}

// Execute runs the command line.
func Execute() error {
	// This binary records its own build for the file on disk before the command
	// runs, so satelled names the installed CLI without running it; an update
	// that replaces this file mid-run records the new file itself. Best effort.
	_ = buildinfo.RecordSelf(config.GlobalDir())
	root := NewRootCmd()
	err := root.Execute()
	// satelled hears of every ledger write, whether the command succeeded or
	// not (a refused transition still records its verdicts).
	flushUI(context.Background())
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
			fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Resolve().String())
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
	if u := osUser(); u != "" {
		return u
	}
	return "cli"
}

// osUser is the OS user from USER or USERNAME, or "" when neither is set.
func osUser() string {
	for _, k := range []string{"USER", "USERNAME"} {
		if u := strings.TrimSpace(os.Getenv(k)); u != "" {
			return u
		}
	}
	return ""
}
