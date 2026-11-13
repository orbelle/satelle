package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/substrate"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

func initCmd() *cobra.Command {
	var hooks []string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create .satelle/ here with a two-state workflow, then validate it",
		Long: `init writes the starter configuration into ./.satelle (satelle.toml,
agents.toml and workflows/default.toml: backlog → done, no gates), leaving any
file that already exists alone, and validates the result. In a repo that
already has a satelle.toml it writes no starter workflow: the repo keeps its own
routes. The configuration is yours to edit and replace. 'satelle doctor' lists
what init would add.

--hooks installs harness hook files as well (claude when given bare; or name
them: --hooks claude,pi,grok). Without it no harness file is touched; see
'satelle agents install'. init ends by naming each harness it left unwired and
the command that wires one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			dataDir := filepath.Join(cwd, config.DefaultDataDir)
			if config.IsGlobalDir(dataDir) {
				return fmt.Errorf("%s is satelle's machine-wide home, not a repo", dataDir)
			}
			out := cmd.OutOrStdout()
			if err := kernel.Init(dataDir); err != nil {
				return problemsError(dataDir, err)
			}
			fmt.Fprintf(out, "initialised %s (configuration valid)\n", dataDir)
			if err := installHooks(out, cwd, hooks, staleHook(substrateTree(NewRootCmd()))); err != nil {
				return err
			}
			// Name what is left unwired, at the root doctor and status read.
			if r, err := findRepo(""); err == nil {
				var unwired []string
				for _, w := range hook.WiringOf(r.Tree) {
					if !w.Wired() {
						unwired = append(unwired, w.Harness)
					}
				}
				if len(unwired) > 0 {
					fmt.Fprintf(out, "harness hooks not installed: %s\n", strings.Join(unwired, ", "))
					fmt.Fprintf(out, "  %s   # gives that harness's sessions satelle's context and the edit gate; commit the file it writes\n", wireFix("<harness>"))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&hooks, "hooks", nil, "install harness hooks ("+strings.Join(hook.Harnesses, ", ")+")")
	cmd.Flags().Lookup("hooks").NoOptDefVal = hook.Claude
	return cmd
}

func problemsError(dataDir string, err error) error {
	return fmt.Errorf("configuration in %s is invalid:\n%w", dataDir, err)
}

func validateCmd() *cobra.Command {
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate this repo's .satelle configuration and its substrate",
		Long: `validate checks this repo's .satelle configuration (the problems that stop
the kernel opening) and its substrate against this binary: every satelle
command, flag and help topic its skills, principles, tasks, workflows and
queues name in code, and every [[link]], must exist. A substrate finding is
printed as <file>:<line>: <kind>: <subject> — <why> (listed under the
substrate key with --json) and fails the check, but never stops the kernel opening.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := findRepo("")
			if err != nil {
				return err
			}
			problems := kernel.Validate(r.DataDir)
			drift, err := substrate.Check(r.DataDir, substrateTree(NewRootCmd()))
			if err != nil {
				return fmt.Errorf("checking the substrate in %s: %w", r.DataDir, err)
			}
			if drift == nil {
				drift = []substrate.Finding{}
			}
			valid := len(problems) == 0 && len(drift) == 0
			if *asJSON {
				msgs := []string{}
				for _, p := range problems {
					msgs = append(msgs, p.Error())
				}
				if err := writeJSON(cmd.OutOrStdout(), map[string]any{"root": r.DataDir, "valid": valid, "problems": msgs, substrate.Field: drift}); err != nil {
					return err
				}
			} else if valid {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: configuration valid\n", r.DataDir)
			}
			if valid {
				return nil
			}
			var errs []error
			if len(problems) > 0 {
				errs = append(errs, problemsError(r.DataDir, errors.Join(problems...)))
			}
			if len(drift) > 0 {
				lines := make([]string, len(drift))
				for i, f := range drift {
					lines[i] = f.String()
				}
				errs = append(errs, fmt.Errorf("substrate in %s names what this binary lacks:\n%s", r.DataDir, strings.Join(lines, "\n")))
			}
			return errors.Join(errs...)
		},
	}
	asJSON = jsonFlag(cmd)
	return cmd
}

// statusReport is `satelle status --json`.
type statusReport struct {
	Root      string   `json:"root"`
	Tree      string   `json:"tree"`
	Runtime   string   `json:"runtime"`
	Workflows []string `json:"workflows"`
	// Harnesses is each harness's wiring in this repo (hook.WiringOf).
	Harnesses []hook.Wiring  `json:"harnesses"`
	Items     map[string]int `json:"items"` // "<kind> <status>" → count
	Session   string         `json:"session,omitempty"`
	Seats     []seatView     `json:"seats,omitempty"`
	// Seed is the last snapshot satelled refused for this repo, until a full
	// snapshot lands.
	Seed *seedFailure `json:"seed_failure,omitempty"`
}

// seedFailure is a snapshot satelled refused: when it last failed, why, and
// how many attempts in a row failed for that reason.
type seedFailure struct {
	FailedAt time.Time `json:"failed_at"`
	Reason   string    `json:"reason"`
	Attempts int       `json:"attempts"`
}

type seatView struct {
	Item       string `json:"item"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Location   string `json:"location"`
	Performing bool   `json:"performing"`
}

func statusCmd() *cobra.Command {
	var asJSON *bool
	var session string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show this repo's configuration, item counts and the session's seats",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, r, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			rep := statusReport{Root: r.DataDir, Tree: r.Tree, Runtime: r.Runtime, Harnesses: hook.WiringOf(r.Tree), Items: map[string]int{}, Session: session}
			for name := range k.Workflows {
				rep.Workflows = append(rep.Workflows, name)
			}
			sort.Strings(rep.Workflows)
			items, err := k.List(cache.Query{})
			if err != nil {
				return err
			}
			for _, it := range items {
				rep.Items[it.Kind+" "+it.Status]++
			}
			if st, _, _ := syncstate.Load(config.GlobalDir(), uiRepoRoot(r)); st.SnapshotReason != "" {
				rep.Seed = &seedFailure{FailedAt: st.SnapshotLastFailure, Reason: st.SnapshotReason, Attempts: st.SnapshotPasses}
			}
			if session != "" {
				if rep.Seats, err = seatsOf(k, session); err != nil {
					return err
				}
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "config    %s\ntree      %s\nruntime   %s\nworkflows %s\n", rep.Root, rep.Tree, rep.Runtime, strings.Join(rep.Workflows, ", "))
			keys := make([]string, 0, len(rep.Items))
			for key := range rep.Items {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			fmt.Fprintln(out, "harness")
			for _, w := range rep.Harnesses {
				line := fmt.Sprintf("  %-8s %-26s %s", w.Harness, w.State, w.File)
				if w.Rule != "" {
					line += " (" + w.Rule + ")"
				}
				fmt.Fprintln(out, line)
			}
			fmt.Fprintln(out, "items")
			for _, key := range keys {
				fmt.Fprintf(out, "  %-30s %d\n", key, rep.Items[key])
			}
			if s := rep.Seed; s != nil {
				fmt.Fprintf(out, "satelled  seed failed %s (%d attempt(s)): %s\n", s.FailedAt.Format(time.RFC3339), s.Attempts, s.Reason)
				fmt.Fprintln(out, "          satelle project add re-seeds once the cause is fixed")
			}
			if session != "" {
				fmt.Fprintf(out, "session   %s\n", session)
				printSeats(out, rep.Seats)
			}
			return nil
		},
	}
	asJSON = jsonFlag(cmd)
	cmd.Flags().StringVar(&session, "session", sessionFromEnv(), "the session whose seats to show (default $SATELLE_SESSION)")
	return cmd
}
