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

const (
	ExitError    = 1
	ExitBlocked  = 2
	ExitBlobGaps = 3
)

type exitError struct {
	code  int
	msg   string
	quiet bool
}

func (e *exitError) Error() string { return e.msg }

func ExitCodeOf(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return ExitError
}

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
	root.PersistentFlags().String(sessionFlag, "", "the session this command acts as (default $"+config.SessionEnv+")")
	root.PersistentFlags().StringP(directoryFlag, "C", "", "run as if satelle was started in this directory, like git -C")
	root.PersistentPreRunE = rootPreRun
	root.AddCommand(
		initCmd(), validateCmd(), statusCmd(), versionCmd(), doctorCmd(),
		itemCmd("story", true), itemCmd("task", true), itemCmd("execution", false), itemCmd("queue", true),
		ledgerCmd(), workflowCmd(), hookCmd(), agentsCmd(),
		loginCmd(), logoutCmd(), whoamiCmd(), projectCmd(), remoteCmd(), nodeCmd(),
		serviceCmd(), workspaceCmd(), runtimeCmd(), logsCmd(), updateCmd(), settingsCmd(), upgradeCmd(), githubCmd(),
		packCmd(), gateCmd(),
	)
	root.SetHelpCommand(helpCmd(root))
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		defaultHelp(cmd, args)
		printHelpRefs(cmd.OutOrStdout(), commandRefs(root, cmd, flagString(root.PersistentFlags(), directoryFlag)))
	})
	root.PersistentPostRunE = func(cmd *cobra.Command, _ []string) error {
		printRunRefs(cmd.ErrOrStderr(), commandRefs(root, cmd, ""))
		return nil
	}
	return root
}

const (
	sessionFlag   = "session"
	directoryFlag = "directory"
)

func rootPreRun(cmd *cobra.Command, _ []string) error {
	flags := cmd.Root().PersistentFlags()
	if dir, _ := flags.GetString(directoryFlag); dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("-C %s: %w", dir, err)
		}
		if err := flags.Set(directoryFlag, ""); err != nil {
			return err
		}
	}
	if s := strings.TrimSpace(flagString(flags, sessionFlag)); s != "" {
		if err := os.Setenv(config.SessionEnv, s); err != nil {
			return err
		}
	}
	return nil
}

func flagString(fs *pflag.FlagSet, name string) string {
	v, _ := fs.GetString(name)
	return v
}

func Execute() error {
	_ = buildinfo.RecordSelf(config.GlobalDir())
	root := NewRootCmd()
	err := root.Execute()
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

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func jsonFlag(cmd *cobra.Command) *bool {
	return cmd.Flags().Bool("json", false, "print JSON")
}

func actor() string {
	if s := sessionFromEnv(); s != "" {
		return s
	}
	if u := osUser(); u != "" {
		return u
	}
	return "cli"
}

func osUser() string {
	for _, k := range []string{"USER", "USERNAME"} {
		if u := strings.TrimSpace(os.Getenv(k)); u != "" {
			return u
		}
	}
	return ""
}
