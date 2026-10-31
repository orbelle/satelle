// `satelle agent` selects the headless agent CLI the quality-management spine
// shells out to for isolated reviews/summaries (the install-time choice, sty_b6973a7b).
// The selection persists in the global config (~/.satelle/config.toml [agent] cli)
// so every repo's reviewer resolves the same agent; the reviewer/summariser never
// name a binary directly.
//
// `satelle agent validate` (sty_93eec36d) is the store-backed check of this repo's
// .satelle/workflows/agents.toml + workflow agent= bindings — the same authority init and
// story engagement use. The top-level `satelle validate` runs the per-kind
// substrate checks and the machine catalog load check.
package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/doctor"
)

func init() {
	agent := &cobra.Command{
		Use:   "agent",
		Short: "Select the agent CLI the reviewer/summariser use (claude | grok); validate agents.toml",
		Long: `agent manages which headless agent CLI satelle's quality-management spine
shells out to for isolated reviews and summaries. The choice persists in the
global config (~/.satelle/config.toml) so it is set once per machine.

agent validate checks this repo's .satelle/workflows/agents.toml (every binding's command,
timeout, env) and each workflow's agent= node bindings, and surfaces each agent's
resolved grant. Structural workflow checks (rubrics, unresolved gate skills) stay
on satelle workflow validate — this command reuses them alongside the agent layer.

To install satelle-owned launcher scripts, use
satelle agents install (plural) — that path never changes this [agent] cli default.`,
	}

	show := &cobra.Command{
		Use:   "show",
		Short: "Show the selected agent CLI and whether it is installed",
		Long: `Print the selected agent CLI and whether its binary is actually on PATH.

Reach for it when dispatch fails: a selection naming a CLI this machine does not
have looks identical to a misconfigured workflow until you ask.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			gc, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			cli := gc.Agent.ResolveCLI()
			source := "config"
			if gc.Agent.CLI == "" {
				source = "default"
			}
			onPath := "not found on PATH"
			if agentcli.Available(cli) {
				onPath = "on PATH"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "agent cli: %s (%s) — %s\n", cli, source, onPath)
			return nil
		},
	}

	set := &cobra.Command{
		Use:   "set <claude|grok>",
		Short: "Select and persist the agent CLI",
		Long: `Select the agent CLI and persist it machine-wide.

It records a preference; it installs nothing. Naming a CLI this machine does not
have leaves dispatch broken until that binary exists, and satelle agent show is
what tells you which case you are in.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate the name against the known runners before persisting.
			if _, err := agentcli.NewRunner(args[0]); err != nil {
				return err
			}
			return persistAgentCLI(cmd, args[0])
		},
	}

	detect := &cobra.Command{
		Use:   "detect",
		Short: "Auto-detect an installed agent CLI (claude preferred) and persist it",
		Long: `Look for an installed agent CLI and persist the one it finds, preferring claude.

The convenience path on a new machine, where set would mean naming what you have
not checked for. It only ever selects something present, so it cannot leave the
selection pointing at a missing binary.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			found := agentcli.Detect()
			if found == "" {
				return fmt.Errorf("no supported agent CLI found on PATH (looked for %q, %q) — install one, then `satelle agent set <cli>`",
					agentcli.CLIClaude, agentcli.CLIGrok)
			}
			return persistAgentCLI(cmd, found)
		},
	}

	agent.AddCommand(show, set, detect, agentValidateCmd(), agentRecheckCmd(), agentProfilesCmd(), agentMigrateCmd())
	register(agent)
}

// agentRecheckCmd is `satelle agent recheck <harness>` (sty_32c61e68): the way to
// find out which captured contracts a CLI update actually changed.
func agentRecheckCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "recheck <harness>",
		Short: "Re-check a harness's captured contracts against the installed CLI (cursor)",
		Long: `Some adapter behavior (for cursor: stop cap, hook timeout handling, envelope
shapes, ACP methods, usage fields) was measured on one CLI build. When satelle
says the installed build differs, recheck prints both versions, then one line per
contract: holds, changed, or unverifiable. unverifiable names the fixture to
re-capture; those need a live session, which recheck never opens. It reads
--version and --help, plus one ACP initialize. Exits 1 if any contract changed.
A harness with no captured version says so and exits 0.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			harness := strings.ToLower(strings.TrimSpace(args[0]))
			switch harness {
			case agentcli.HarnessCursor, agentcli.HarnessClaude, agentcli.HarnessGrok, agentcli.HarnessPi:
			default:
				return fmt.Errorf("unknown harness %q — one of cursor, claude, grok, pi", args[0])
			}
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			res := doctor.Recheck(cmd.Context(), doctor.Opts{
				RepoRoot:    a.RepoRoot,
				DataDir:     a.PlaneDir(),
				LiveTimeout: timeout,
				Process:     &a.ProcessConfig,
				ProcessRoot: a.ProcessRoot,
			}, harness)
			out := cmd.OutOrStdout()
			res.Render(out)
			if res.Changed() {
				return &ExitError{Code: 1, Quiet: true, Err: fmt.Errorf("a captured %s contract no longer holds against the installed CLI", harness)}
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", doctor.DefaultLiveTimeout, "bound on each spawn of the CLI")
	return cmd
}

func agentMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Seed ~/.satelle/agents.toml with a starter profile from the selected agent CLI",
		Long: `Seed the machine-wide agent profile from the currently selected CLI, so every
repo does not have to author its own bindings from nothing.

Run it once per machine, after the selection is right. What it writes is a
STARTER profile: a repo's own .satelle/workflows/agents.toml still decides what
runs each step.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			path := config.GlobalAgentsPath()
			if _, err := os.Stat(path); err == nil {
				fmt.Fprintf(out, "%s already exists — edit it by hand; migrate never overwrites a catalog\n", path)
				return nil
			}
			gc, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			cli := gc.Agent.ResolveCLI()
			body, err := config.StarterGlobalAgents(cli)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(out, "wrote %s — one %q profile derived from [agent] cli\n", path, cli)
			fmt.Fprintln(out, "reference it from a repo with `profile = \"<name>\"` in .satelle/workflows/agents.toml; nothing changes until you do")
			return nil
		},
	}
}

// printProfileCatalog renders the machine-wide catalog. Secret containment: env
// and settings VALUES are never printed — only their key names, the same rule
// the grant display already follows.
func printProfileCatalog(out io.Writer, g config.GlobalAgentsConfig) {
	if len(g.Profiles) == 0 && len(g.Roles) == 0 && len(g.Seats) == 0 {
		return
	}
	fmt.Fprintf(out, "Global agent profiles (%s — execution configuration; workflow policy stays repo substrate):\n",
		config.GlobalAgentsLabel)
	for _, name := range sortedBindingNames(g.Profiles) {
		b := g.Profiles[name]
		fmt.Fprintf(out, "  PROFILE [%s] role=%q interface=%s model=%q effort=%q tools=%q timeout=%q\n",
			name, b.Role, b.ResolvedInterface(), b.Model, b.Effort, b.Tools, b.Timeout)
		if b.Profile != "" {
			fmt.Fprintf(out, "          extends: %s\n", b.Profile)
		}
		if b.Command != "" {
			fmt.Fprintf(out, "          command: %s\n", b.Command)
		}
		if len(b.Env) > 0 {
			fmt.Fprintf(out, "          env keys: %s\n", strings.Join(sortedMapKeys(b.Env), ","))
		}
	}
	for _, role := range sortedMapKeys(g.Roles) {
		fmt.Fprintf(out, "  ROLE DEFAULT %s -> %s (applies only to a repo with [defaults] use_global_roles = true)\n",
			role, g.Roles[role])
	}
	for _, seat := range sortedMapKeys(g.Seats) {
		fmt.Fprintf(out, "  SEAT [%s] -> %s (every repo on this machine; above agents.toml, below agents.local.toml)\n",
			seat, g.Seats[seat])
	}
}

// printGrantSources renders each effective field with the tier that supplied it
// (AC4): repo inline, an explicitly referenced profile, an opt-in role default,
// or the binary's embedded fallback. It DELEGATES to doctor's renderer so this
// display and `satelle doctor`'s cannot drift (sty_e9da28e2); env/settings
// values may be secrets and are never printed by either.
func printGrantSources(out io.Writer, g agentvalidate.Grant) {
	doctor.RenderGrantSources(out, "         ", g)
}

func sortedBindingNames(m map[string]config.AgentBinding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// agentValidateCmd is `satelle agent validate` — the standalone, deterministic
// check of agents.toml + workflow agent= bindings (sty_93eec36d). It also runs
// the structural workflow check and WorkflowConsistency so one call covers the
// mechanical agent↔workflow surface without duplicating those checks.
func agentValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate .satelle/workflows/agents.toml and workflow agent= bindings; show resolved grants",
		Long: `Check this repo's agents layer: every agent= and reviewer_agent= the workflow
names resolves to a binding, and each binding's resolved model, effort and tool
grant is printed.

Reach for it after editing agents.toml or a step's agent allocation. It reads
both authored files together, so an orphaned binding and a dangling reference
are both visible here rather than at the next dispatch.

Each field is printed with the layer and file that supplied it. Precedence:
agents.local.toml > agents.toml > agents.workspace.toml > embedded baseline >
profile= / [roles] base. agents.local.toml is this machine's override; it never
syncs, so keep profile= and use_global_roles there, not in shared agents.toml.
A seat named in ~/.satelle/agents.toml [seats] takes that profile on every repo
of this machine, above agents.toml and below agents.local.toml.`,
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			dataDir := a.PlaneDir()

			layers, _ := config.LoadAgentLayers(dataDir)
			if layers.RepoErr != nil {
				return fmt.Errorf("agents.toml: %w", layers.RepoErr)
			}
			// The machine-wide profile catalog is EXECUTION configuration only
			// (sty_c7dfeedf). A broken catalog is reported here as a problem, not a
			// hard error, so one run shows every finding.
			global, gerr := config.LoadGlobalAgents()
			if gerr != nil {
				fmt.Fprintf(out, "FAIL  %v\n", gerr)
			}
			// The synced workspace bindings layer (sty_01949949) sits under the
			// repo's file in the ladder; it is judged here so the grants printed
			// are the ones that will run, each field naming its source.
			if layers.WorkspaceErr != nil {
				fmt.Fprintf(out, "FAIL  %v\n", layers.WorkspaceErr)
			}
			if layers.LocalErr != nil {
				fmt.Fprintf(out, "FAIL  %v\n", layers.LocalErr)
			}
			if _, serr := os.Stat(config.LocalAgentsPath(dataDir)); serr == nil {
				fmt.Fprintf(out, "Machine-local layer: %s (present — top of the ladder; never syncs; source shown per field)\n", config.LocalAgentsRel)
			} else {
				fmt.Fprintf(out, "Machine-local layer: %s (absent — no machine override)\n", config.LocalAgentsRel)
			}
			if _, serr := os.Stat(config.WorkspaceAgentsPath(dataDir)); serr == nil {
				fmt.Fprintf(out, "Workspace bindings layer: %s (present — a repo field wins; a workspace field fills a blank; source shown per field)\n", config.WorkspaceAgentsRel)
			} else {
				fmt.Fprintf(out, "Workspace bindings layer: %s (absent — repo bindings only)\n", config.WorkspaceAgentsRel)
			}
			// Read the AUTHORED FILES, not the doc index — the same source, through
			// the same helpers, that `satelle doctor` uses (sty_540cfcd3). Reading
			// the index made these two commands contradict each other seconds apart
			// on an unchanged tree: editing substrate is exactly when the index is
			// most likely to lag, and validating right after the edit is exactly
			// what an author does.
			//
			// The two sets are deliberately different, as doctor has always had it:
			// ALLOCATION judges the GOVERNING set (authored ∪ unshadowed embedded),
			// because an embedded default really does govern a repo that has not
			// overridden it; CONSISTENCY judges only the AUTHORED set, because an
			// on-disk wildcard workflow legitimately shadows the embedded wildcard
			// baseline and feeding both to the ambiguity check would report every
			// repo as broken.
			governing := doctor.GoverningWorkflows(dataDir)

			// Agent layer + node→binding (shared with init + engage), resolved
			// against the catalog so what is judged is what will actually run.
			// Skill bodies too, from the same resolver doctor uses — otherwise this
			// command and `satelle doctor` would report the reviewer shell grant
			// differently on the same tree (sty_338a53f8).
			report := agentvalidate.ValidateEffectiveLayered(layers, global, a.PlaneConfig().Vars, governing,
				doctor.SkillBodyResolver(dataDir))
			printProfileCatalog(out, global)
			fmt.Fprintln(out, "Agent grants (resolved):")
			for _, g := range report.Grants {
				ro := "read-write"
				if g.ReadOnly {
					ro = "read-only"
				}
				consti := "no"
				if g.InjectsPrinciples {
					consti = "yes" // constitution rides order-zero when principles ≠ none
				}
				if g.Backend == "in-loop" {
					consti = "n/a (in-loop; session injects)"
				}
				roleNote := g.Role
				if g.RoleInferred {
					roleNote += " (inferred)"
				}
				ifaceNote := g.Interface
				if g.InterfaceReason != "" {
					ifaceNote = fmt.Sprintf("%s (%s)", g.Interface, g.InterfaceReason)
				}
				fmt.Fprintf(out, "  GRANT [%s] role=%s principles=%s constitution=%s interface=%s placement=%s backend=%s %s tools=%q model=%q effort=%q timeout=%q idle_timeout=%q inject_principles=%v command=%q\n",
					g.Name, roleNote, g.Principles, consti, ifaceNote, g.Placement, g.Backend, ro, g.Tools, g.Model, g.Effort, g.Timeout, g.IdleTimeout, g.InjectsPrinciples, g.Command)
				if g.Notes != "" {
					fmt.Fprintf(out, "         notes: %s\n", g.Notes)
				}
				printGrantSources(out, g)
				// Local resolution (sty_01949949 AC3): a binding — workspace-supplied
				// or not — names a program; THIS machine decides whether it exists.
				// A warning here, a hard refusal at dispatch: an operator may inspect
				// a machine that intentionally lacks a binding without bricking validate.
				if g.Backend != "in-loop" && g.Backend != "invalid" {
					if tok := config.ExecutableToken(g.Command); tok != "" {
						if _, lerr := exec.LookPath(tok); lerr != nil {
							fmt.Fprintf(out, "WARN  [%s] command %q is not executable on this machine (%v) — dispatch will refuse; install it or rebind the agent\n", g.Name, tok, lerr)
						}
					}
				}
			}
			if len(report.Gates) > 0 {
				fmt.Fprintln(out, "Gate/node effective models (binding that will run the gate):")
				for _, ga := range report.Gates {
					// A lifecycle HOOK fires outside the status graph, so it is labelled
					// as such and carries how it was declared — an allocation that used
					// to be an invisible fallback (sty_ede16f51).
					if ga.Operation != "" {
						fmt.Fprintf(out, "  HOOK [%s] %s gate=%s agent=%s effective_model=%q declared=%s\n",
							ga.Workflow, ga.Node, ga.Skill, ga.Agent, ga.EffectiveModel, ga.Source)
						continue
					}
					fmt.Fprintf(out, "  NODE [%s] %s gate=%s agent=%s effective_model=%q\n",
						ga.Workflow, ga.Node, ga.Skill, ga.Agent, ga.EffectiveModel)
				}
			}
			failed := 0
			// A broken catalog (a retired table, an unknown key) is a failure, not
			// only a printed line; gerr was printed above.
			if gerr != nil {
				failed++
			}
			for _, w := range report.Warnings {
				fmt.Fprintf(out, "WARN  %s\n", w)
			}
			for _, p := range report.Problems {
				failed++
				fmt.Fprintf(out, "FAIL  %s\n", p)
			}
			if report.OK() {
				fmt.Fprintln(out, "PASS  agents.toml + workflow agent= bindings")
			}
			for _, f := range doctor.BindingWiring(a.RepoRoot, a.PlaneConfig(), report.Grants) {
				failed++
				fmt.Fprintf(out, "FAIL  %s — %s\n", f.Detail, f.Remediation)
			}

			// Structural workflow checks + consistency (owned elsewhere; re-run so
			// one verb covers the mechanical agent↔workflow surface). Disk-backed
			// resolver and AUTHORED set, matching doctor (sty_540cfcd3).
			resolve := doctor.SkillResolver(dataDir)
			// An unreadable workflows dir was already reported above, once, as a
			// problem of the governing set; validating it again would print it twice.
			if _, _, unreadable := docindex.UnreadableOf(governing); !unreadable {
				_, f, _ := validateAuthoredDir(out, "workflows", filepath.Join(dataDir, "workflows"), "", resolve)
				failed += f
			}
			for _, p := range agentstep.WorkflowConsistency(doctor.WorkflowDocs(dataDir), resolve) {
				failed++
				fmt.Fprintf(out, "FAIL  workflows (consistency) — %s\n", p)
			}

			if failed > 0 {
				return fmt.Errorf("agent validate: %d problem(s)", failed)
			}
			fmt.Fprintln(out, "PASS  agent validate green")
			return nil
		},
	}
}

// persistAgentCLI saves cli into the global config, preserving other settings.
func persistAgentCLI(cmd *cobra.Command, cli string) error {
	gc, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	gc.Agent.CLI = cli
	if err := config.SaveGlobal(gc); err != nil {
		return err
	}
	note := ""
	if !agentcli.Available(cli) {
		note = " (warning: not currently on PATH)"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "agent cli set to %s%s → %s\n", cli, note, config.GlobalConfigPath())
	return nil
}
