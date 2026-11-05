// Package cli wires the satelle command-line interface.
//
// Subcommands self-register via init() calling register(), so adding a new
// verb is one new file under internal/cli/ — no edits to a central switch.
// Ported from satellites' internal/cli/root.go, stripped of the engagement
// ticker (the local OSS tier has no leased session to heartbeat).
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/wfgovern"
)

var registered []*cobra.Command

// register attaches a subcommand to the root. Called from each subcommand
// file's init().
func register(c *cobra.Command) {
	registered = append(registered, c)
}

// NewRootCmd returns the root cobra command with every registered
// subcommand attached.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "satelle",
		Short: "Satelle — local-first authored-process substrate CLI",
		Long: `satelle governs the authored process for agent-driven work, backed by a
local per-repo database. The OSS tier runs 100% locally with no server
dependency. See https://github.com/bobmcallan/satelle for docs.`,
		Version:       versionLine(),
		SilenceUsage:  true,
		SilenceErrors: true,
		// Bootstrap seam: open the local store (config + db) only for commands
		// that carry the storeAnnotation, and close it after. `version` and
		// `--help` are unannotated, so they never create a database.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Annotations[storeAnnotation] == "1" {
				err := openAppForCmd(cmd)
				// A store-optional command reports on something other than the
				// current repo, so an ungoverned cwd must not stop it. It runs with
				// no app and is responsible for handling that (sty_0f471251).
				//
				// An unreadable authored process is the other case it must see: the
				// bootstrap refuses every store command then, and doctor's job is to
				// report that as a finding rather than stop on it (sty_d6e209aa).
				if err != nil && cmd.Annotations[storeOptionalAnnotation] == "1" &&
					(errors.Is(err, app.ErrNotInitialised) || errors.Is(err, wfgovern.ErrAuthoredProcessUnreadable)) {
					return nil
				}
				return err
			}
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			closeAppForCmd(cmd)
			return nil
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	// Compact-rendering overrides (sty_75b76691 AC4), global so any command's
	// dispatch() call can read them: --json always forces plain indented JSON;
	// --compact forces compact mode for a configured compact_commands verb
	// even when [output].compact_for_agents is off or the caller is not
	// detected as an agent. See internal/cli/render.go.
	root.PersistentFlags().Bool("json", false, "force plain indented JSON, skipping compact rendering")
	for _, c := range registered {
		root.AddCommand(c)
	}
	return root
}

// retiredNames maps a removed full CLI path (space-separated) to the
// replacement to name when the removed spelling is invoked. Keys match
// os.Args[1:] joined — so `service install` is never confused with top-level
// `install`.
var retiredNames = map[string]string{
	"install":          "satelle init",
	"workspace rm":     "satelle workspace remove",
	"sync config pull": "satelle sync config deploy",
	// sty_805bee9c: ui push folded into workspace add (register + seed).
	"ui push": "satelle workspace add",
	"ui":      "satelle workspace add",
}

// retiredNameMessage returns a fail-closed error for a removed spelling, or "".
func retiredNameMessage(args []string) string {
	// Try longest path first (3 tokens, then 2, then 1).
	for n := 3; n >= 1; n-- {
		if len(args) < n {
			continue
		}
		key := strings.Join(args[:n], " ")
		if repl, ok := retiredNames[key]; ok {
			return fmt.Sprintf("satelle: %q was removed — use %s instead (breaking CLI surface; see CHANGELOG.md ### Breaking)", key, repl)
		}
	}
	return ""
}

// Execute runs the root command and, on an unknown subcommand, follows the
// bare cobra error with the command's usage so a mistyped command guides the
// user instead of dead-ending. Removed spellings fail closed naming their
// replacement. Returns the command's error so the caller sets a non-zero exit.
func Execute() error {
	// Fail closed on removed spellings before cobra's generic unknown-command
	// path, so the replacement is named explicitly.
	if len(os.Args) > 1 {
		if msg := retiredNameMessage(os.Args[1:]); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
			return fmt.Errorf("%s", msg)
		}
	}
	root := NewRootCmd()
	// ExecuteC returns the leaf command so we can drain/close even when RunE
	// failed — Cobra skips PersistentPostRunE on error (sty_9ba3d709).
	c, err := root.ExecuteC()
	if c != nil {
		closeAppForCmd(c)
	}
	err, show := finishExecute(err)
	if err != nil && show {
		fmt.Fprintln(root.ErrOrStderr(), err)
		if isUnknownCommandErr(err) {
			printUnknownCommandHelp(root.ErrOrStderr(), root)
		}
	}
	return err
}

// isUnknownCommandErr reports whether err is cobra's "unknown command"
// error. Cobra exposes no typed error for this, but the message prefix
// (`unknown command %q for %q`) is stable across releases.
func isUnknownCommandErr(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "unknown command")
}

// printUnknownCommandHelp writes the root usage block so an unknown command
// response names the available commands.
func printUnknownCommandHelp(w io.Writer, root *cobra.Command) {
	fmt.Fprintln(w)
	fmt.Fprint(w, root.UsageString())
}
