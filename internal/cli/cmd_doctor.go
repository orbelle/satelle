// `satelle doctor` is the one diagnostic surface: is satelle ready to govern
// this repository, and optionally every registered one (sty_e9da28e2).
//
// It composes the validators that already exist rather than adding rules of its
// own, so init, engagement, and doctor cannot drift into three different
// opinions about the same repo.
package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/health"
)

func init() {
	var (
		all     bool
		asJSON  bool
		verbose bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose whether satelle can govern this repository (add --all for every registered repo)",
		Long: `doctor reports whether satelle is ready to govern a repository.

It COMPOSES the checks that already own their rules — the agents layer and its
machine-wide profile references, workflow structure, cross-workflow consistency,
lifecycle-hook allocations, skills, permission ceilings, required binaries, and
harness hook scaffolding — and prints each binding's effective value with the
source that supplied it. It adds no rule of its own, so it can never disagree
with what init validates or what engagement refuses.

TWO CONFIGURATION LAYERS, deliberately kept apart:

  repo workflow POLICY        .satelle/ — workflows, gates, lifecycle hooks, and
                              which logical agent runs each step. What THIS repo
                              enforces. Never machine-wide.
  machine-wide EXECUTION      ~/.satelle/agents.toml — reusable provider profiles
                              (command, transport, model, effort, tools). HOW a
                              command runs on this machine, shared by every repo.

SEVERITY
  FAIL  an error: the repo is not ready; engagement will refuse for the same reason
  WARN  advisory: printed, never blocking (the underlying test is a heuristic)
  INFO  context you asked for (a live probe that answered)

EXIT CODES
  0  healthy — warnings are allowed
  1  one or more error findings, in any checked repository
  2  doctor could not run (bad flag, unreadable global config)

LIVE PROBES (--live) SPAWN PROVIDER PROCESSES. Ordinary doctor performs no paid
and no network model call at all. --live starts each isolated binding's CLI: a
command-transport binding is asked for its version, and an ACP binding is taken
through a single initialize handshake. Neither opens a session or sends a prompt,
so neither is a paid model call — but both may consume provider authentication
and rate budget. Every probe is bounded by --timeout and its process group is
killed and reaped on the deadline or on cancellation.`,
		Args:        cobra.NoArgs,
		Annotations: needsStoreOptional(),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			// --all reports on the REGISTRY, not on this directory, so it must run
			// from anywhere — including a non-satelle cwd, which is exactly where an
			// operator lands after upgrading the binary (sty_0f471251). CheckAll
			// resolves each registered repo itself — its process of record
			// included — and never reads this invoking repo's repo/process opts,
			// so running with no app costs nothing.
			//
			// The single-repo path still requires the app: without a repo there is
			// nothing for it to diagnose, and the refusal names `satelle init`.
			a, err := appFrom(cmd)
			if err != nil && !all {
				oa, oerr := app.Open()
				if oerr != nil {
					return oerr // the actionable "not a satelle repo — run satelle init"
				}
				// A governed repo whose bootstrap stood aside because its authored
				// process is unreadable (the only refusal a store-optional command
				// gets past): doctor reports it as a finding instead of stopping.
				defer func() { _ = oa.Close() }()
				a, err = oa, nil
			}
			opts := doctor.Opts{}
			if a != nil {
				opts.RepoRoot = a.RepoRoot
				// The process read-plane: in a linked worktree the main tree's
				// process is the one in force, so that is what doctor judges and
				// what the divergence report compares the worktree against.
				opts.DataDir = a.PlaneDir()
				opts.Process = &a.ProcessConfig
				opts.ProcessRoot = a.ProcessRoot
			}
			ctx := context.Background()

			var reports []doctor.Report
			if all {
				reports = doctor.CheckAll(ctx, opts)
				if len(reports) == 0 {
					fmt.Fprintln(out, "no registered repositories — add one with `satelle workspace add <path>`")
					return nil
				}
			} else {
				reports = []doctor.Report{doctor.Check(ctx, opts)}
			}

			if asJSON {
				if err := doctor.RenderJSON(out, reports); err != nil {
					return err
				}
			} else {
				doctor.RenderText(out, reports, !all || verbose, verbose)
			}
			if doctor.ExitCode(reports) != doctor.ExitHealthy {
				_, unhealthy := doctor.Summarise(reports)
				return fmt.Errorf("doctor: %d repositor(y|ies) unhealthy — fix the FAIL findings above", unhealthy)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "check every registered workspace repository, each independently")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "also list every workflow node/edge allocation (and force detail under --all)")
	register(cmd)
}

// statusDriftFix is the drift finding's fix: a row that disagrees with its chain
// is most often a story whose chain forked from its hosted one, which rechain
// merges; reconcile repairs the row of a story only this checkout holds.
const statusDriftFix = health.RechainRemediation + "; for a story only this checkout holds, run `satelle story reconcile --repair`"
