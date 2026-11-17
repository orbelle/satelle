package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	var print string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create .satelle/ here, or list what a configured repo lacks; --print gives a shipped file",
		Long: `In a directory with no .satelle/satelle.toml, init writes the starter
configuration into ./.satelle (satelle.toml, agents.toml, init.toml and
workflows/default.toml: backlog → done, no gates) and validates the result.
The configuration is yours to edit and replace. When it writes volatile.toml it
says so, and how many lines the volatile check then flags.

In a repo that already has a satelle.toml, init writes nothing under .satelle/.
It lists the files this release ships that the repo lacks and prints the
satelle story create command that raises a story for them, in the category
.satelle/init.toml names: adding them changes the substrate, and raising that
story is the user's decision. Where the repo names no category it prints no
command and says which key must name it and which categories the repo's
workflow rules select. removed in .satelle/init.toml records a shipped file the
repo removed on purpose; init and doctor then do not report it.

--print <path> prints one file this release ships, exactly, and nothing else,
for that story's session to write; it writes nothing and needs no repo.

--hooks installs harness hook files as well (claude when given bare; or name
them: --hooks claude,pi,grok). Without it no harness file is touched; see
'satelle agents install'. init ends by naming each harness it left unwired and
the command that wires one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("print") {
				return printShipped(cmd, print)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			dataDir := filepath.Join(cwd, config.DefaultDataDir)
			if config.IsGlobalDir(dataDir) {
				return fmt.Errorf("%s is satelle's machine-wide home, not a repo", dataDir)
			}
			out := cmd.OutOrStdout()
			if kernel.Configured(dataDir) {
				if err := reportConfigured(out, dataDir); err != nil {
					return err
				}
			} else if err := firstInit(out, dataDir); err != nil {
				return err
			}
			if err := installHooks(out, cwd, hooks, staleHook(substrateTree(NewRootCmd()))); err != nil {
				return err
			}
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
	cmd.Flags().StringVar(&print, "print", "", "print one file this release ships (.satelle/<file>) and write nothing")
	return cmd
}

func printShipped(cmd *cobra.Command, path string) error {
	if cmd.Flags().Changed("hooks") {
		return errors.New("--print prints one shipped file and installs nothing")
	}
	rel := strings.TrimPrefix(filepath.ToSlash(path), config.DefaultDataDir+"/")
	b, err := kernel.Shipped(rel)
	if err != nil {
		return fmt.Errorf("%s is not a file this release ships; satelle init lists the ones this repo lacks", path)
	}
	_, err = cmd.OutOrStdout().Write(b)
	return err
}

func reportConfigured(out io.Writer, dataDir string) error {
	plan, err := kernel.InitPlan(dataDir)
	if err != nil {
		return err
	}
	valid := kernel.Validate(dataDir)
	if len(plan) == 0 {
		if len(valid) > 0 {
			return problemsError(dataDir, errors.Join(valid...))
		}
		fmt.Fprintf(out, "%s: nothing to add (configuration valid)\n", dataDir)
		return nil
	}
	pol := kernel.InitPolicy(dataDir)
	fmt.Fprintf(out, "satelle init: nothing written under %s/\n", config.DefaultDataDir)
	fmt.Fprintf(out, "%s lacks %d file(s) this release ships:\n", dataDir, len(plan))
	for _, rel := range plan {
		fmt.Fprintf(out, "  %s/%s\n", config.DefaultDataDir, rel)
	}
	fmt.Fprintln(out, "adding them changes the substrate, which is a substrate story, and raising one is the user's decision:")
	if pol.CategorySet() {
		fmt.Fprintf(out, "  %s\n", substrateStoryCommand(pol.Category, plan))
	} else {
		fmt.Fprintf(out, "%s\n  %s\n", categoryUnsetProblem(len(plan)), categoryUnsetFix(ruleCategories(dataDir)))
	}
	fmt.Fprintln(out, "that story's session writes each file itself, from the content this prints:")
	fmt.Fprintf(out, "  satelle init --print %s/<file> --session <session>\n", config.DefaultDataDir)
	if len(valid) > 0 {
		return problemsError(dataDir, errors.Join(valid...))
	}
	return nil
}

func firstInit(out io.Writer, dataDir string) error {
	plan, _ := kernel.InitPlan(dataDir)
	addsVolatile := slices.Contains(plan, kernel.VolatileFile)
	if err := kernel.Init(dataDir); err != nil {
		return problemsError(dataDir, err)
	}
	fmt.Fprintf(out, "initialised %s (configuration valid)\n", dataDir)
	if addsVolatile {
		rel := filepath.Join(config.DefaultDataDir, kernel.VolatileFile)
		if found, err := volatileFindings(dataDir); err != nil {
			fmt.Fprintf(out, "volatile check enabled: wrote %s; could not count the lines it flags: %v\n", rel, err)
		} else {
			lines := map[string]bool{}
			for _, f := range found {
				lines[fmt.Sprintf("%s:%d", f.File, f.Line)] = true
			}
			fmt.Fprintf(out, "volatile check enabled: wrote %s, which flags %d line(s) here; satelle validate lists them, and the file holds the patterns\n", rel, len(lines))
		}
	}
	return nil
}

func problemsError(dataDir string, err error) error {
	return fmt.Errorf("configuration in %s is invalid:\n%w", dataDir, err)
}

func volatileFindings(dataDir string) ([]substrate.Finding, error) {
	cfg, _ := kernel.LoadConfig(dataDir)
	if cfg == nil || len(cfg.Volatile.Problems()) > 0 {
		return []substrate.Finding{}, nil
	}
	found, err := cfg.VolatileFindings()
	if err != nil || found == nil {
		return []substrate.Finding{}, err
	}
	return found, nil
}

func tagWarnings(r repo, configValid bool) ([]kernel.TagWarning, error) {
	if !configValid {
		return []kernel.TagWarning{}, nil
	}
	k, err := r.openWith("satelle validate")
	if err != nil {
		return []kernel.TagWarning{}, err
	}
	defer k.Close()
	items, err := k.List(cache.Query{})
	if err != nil {
		return []kernel.TagWarning{}, err
	}
	return kernel.TagWarnings(items), nil
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
substrate key with --json) and fails the check, but never stops the kernel opening.

It also lists each line of the files .satelle/volatile.toml names under
paths (shipped: the .satelle Markdown) that matches one of that file's
volatile patterns (a version string, "currently", a model id), as
<file>:<line>: volatile: <pattern> — <why> (under the volatile key with
--json). A line its dated expression matches (shipped: a YYYY-MM-DD date),
or holding the file's exempt marker, is not listed, no file its exclude globs
match is read, and no line of a fenced code block in Markdown is read. A line in a file an [[upstream]] entry owns
names that owner. Volatile lines are listed without failing the check;
satelle doctor reports each as an issue.
With no volatile.toml, or no pattern in it, none is listed.

When the configuration is valid it also lists each item's tag whose key has
conventional values (satelle help satelle) and whose value is none of them, as
<item>: tag "<tag>": conventional values are <values> (under the tag_warnings
key with --json). These are listed without failing the check.`,
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
			volatile, err := volatileFindings(r.DataDir)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", substrate.VolatileField, err))
			}
			tagged, err := tagWarnings(r, len(problems) == 0)
			if err != nil {
				problems = append(problems, fmt.Errorf("tag_warnings: %w", err))
			}
			valid := len(problems) == 0 && len(drift) == 0
			if *asJSON {
				msgs := []string{}
				for _, p := range problems {
					msgs = append(msgs, p.Error())
				}
				if err := writeJSON(cmd.OutOrStdout(), map[string]any{"root": r.DataDir, "valid": valid, "problems": msgs, substrate.Field: drift,
					substrate.VolatileField: volatile, "tag_warnings": tagged}); err != nil {
					return err
				}
			} else {
				if valid {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: configuration valid\n", r.DataDir)
				}
				if len(volatile) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "volatile literals in %s (%d):\n", r.DataDir, len(volatile))
					for _, f := range volatile {
						fmt.Fprintln(cmd.OutOrStdout(), f.String())
					}
				}
				if len(tagged) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "tags outside their conventional values in %s (%d):\n", r.DataDir, len(tagged))
					for _, w := range tagged {
						fmt.Fprintln(cmd.OutOrStdout(), w.String())
					}
				}
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

type statusReport struct {
	Root      string         `json:"root"`
	Tree      string         `json:"tree"`
	Runtime   string         `json:"runtime"`
	Workflows []string       `json:"workflows"`
	Harnesses []hook.Wiring  `json:"harnesses"`
	Items     map[string]int `json:"items"`
	Session   string         `json:"session,omitempty"`
	Seats     []seatView     `json:"seats,omitempty"`
	Seed      *seedFailure   `json:"seed_failure,omitempty"`
}

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
	Edits      string `json:"edits,omitempty"`
}

func statusCmd() *cobra.Command {
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show this repo's configuration, item counts and the session's seats",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			session := sessionFromEnv()
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
	return cmd
}
