// `satelle agents install|remove` provisions satelle-owned launchers under
// $SATELLE_HOME/agents/bin/ and repo harness compliance scaffolds
// (.claude / .grok / .cursor blocking hooks, .pi extension) (sty_aa726901,
// sty_9e86f407, sty_b3c7b37d, sty_7d098d50).
// Distinct from `satelle agent` (singular: select/validate the headless CLI).
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	agents := &cobra.Command{
		Use:   "agents",
		Short: "Install or remove satelle-owned agent launchers and harness compliance hooks (claude | grok | pi | cursor | all)",
		Long: `agents install and remove manage two satelle-owned surfaces per target:

  1. Launcher scripts under $SATELLE_HOME/agents/bin/ (claude and grok only).
  2. Repo harness compliance scaffolds with blocking PreToolUse hooks:
     .claude/settings.json, .grok/hooks/satelle.json, .cursor/hooks.json, and
     for pi the extension .pi/extensions/satelle.ts — so governed
     code-changing actions are denied unless a satelle story is engaged (same
     policy as satelle hook gate / commitgate).

Compliance guarantee: each scaffolded harness's hook path denies governed
mutations when no story is engaged, and applies the normal engaged-story policy
when one is. pi and cursor cannot veto a stop, so a refused stop returns as a
message the agent must answer.

Unavailable on cursor, recorded rather than approximated:
  cursor — stop: unavailable in print mode (no stop event dispatched; sty_383ff068 capture 4)
  cursor — prompt reminder (beforeSubmitPrompt): unavailable — print mode does not dispatch it; interactive context delivery unproven

Ownership boundary: only satelle-owned artifacts (launchers and hook entries
whose command references satelle-hook.sh / satelle hook) are created, updated,
or removed. User-authored harness keys and non-satelle hooks are preserved.
Install and remove are idempotent.

Statusline: satelle installs NONE (an operator preference; the repo's
.claude/settings.json is shared). Put "satelle status --line" in your own
~/.claude/settings.json as statusLine.command; install prints the snippet.

They install no third-party packages, change no ~/.satelle/config.toml [agent]
cli, and edit no agents.toml.

For selecting or validating the headless agent CLI, use satelle agent (singular):
  satelle agent show | set | detect | validate`,
	}

	install := &cobra.Command{
		Use:   "install <claude|grok|pi|all>",
		Short: "Install harness compliance hooks (idempotent)",
		Long: `Install the satelle-owned compliance hooks for a harness, or for all of them.

"all" covers every harness satelle scaffolds, pi included, so it creates
.pi/extensions/ in a repo that does not use it. Name the harness to avoid that.

Idempotent: re-running converges rather than duplicating. It writes only the
files satelle marks as its own, so a hook you authored by hand is never
overwritten — which is also why remove leaves yours in place.

Install also adds each scaffold directory to the managed .gitignore block,
unless git already tracks files under it or .gitignore already has a rule for it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot := initRepoRoot("")
			out := cmd.OutOrStdout()
			// Harness compliance scaffolds (repo-local).
			targets, err := expandAgentTargets(args[0])
			if err != nil {
				return err
			}
			if err := writeHookScripts(repoRoot); err != nil {
				return err
			}
			for _, name := range targets {
				switch name {
				case "claude":
					added, updated, incomplete, err := ensureClaudeHooks(repoRoot)
					if err != nil {
						return err
					}
					printScaffoldOutcome(out, "claude", ".claude/settings.json", added, updated, incomplete)
				case "grok":
					added, updated, incomplete, err := ensureGrokHooks(repoRoot)
					if err != nil {
						return err
					}
					printScaffoldOutcome(out, "grok", grokHooksRel, added, updated, incomplete)
				case "pi":
					added, updated, incomplete, err := scaffoldPiHooks(repoRoot)
					if err != nil {
						return err
					}
					printScaffoldOutcome(out, "pi", piExtensionRel, added, updated, incomplete)
				}
			}
			// After the scaffolds, so a harness dir created on this run is seen.
			if added, gerr := ensureGitignore(repoRoot); gerr != nil {
				return gerr
			} else if len(added) > 0 {
				fmt.Fprintf(out, "updated .gitignore (added: %s)\n", strings.Join(added, ", "))
			}
			fmt.Fprintln(out, "No default reviewer or [agent] cli was changed.")
			fmt.Fprintln(out, "Compliance: governed code edits require an engaged satelle story (hook gate).")
			return nil
		},
	}

	remove := &cobra.Command{
		Use:   "remove <claude|grok|pi|all>",
		Short: "Remove satelle-owned hook scaffolds (idempotent; unmarked left in place)",
		Long: `Remove the hook scaffolds satelle installed for a harness.

Only satelle-MARKED files go; anything you authored yourself stays, deliberately.
Afterwards that harness no longer enforces the edit gate, so reach for it when
you mean to stop satelle governing the harness — not as tidying.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot := initRepoRoot("")
			out := cmd.OutOrStdout()
			targets, err := expandAgentTargets(args[0])
			if err != nil {
				return err
			}
			for _, name := range targets {
				var action, path, note string
				var rerr error
				switch name {
				case "claude":
					action, path, note, rerr = removeClaudeHooks(repoRoot)
				case "grok":
					action, path, note, rerr = removeGrokHooks(repoRoot)
				case "pi":
					action, path, note, rerr = removePiHooks(repoRoot)
				}
				if rerr != nil {
					return rerr
				}
				fmt.Fprintf(out, "%s %s scaffold → %s", action, name, path)
				if note != "" {
					fmt.Fprintf(out, " (%s)", note)
				}
				fmt.Fprintln(out)
			}
			// Shared wrapper: remove only when no remaining scaffold references it.
			if action, path, note, err := maybeRemoveSharedHookScript(repoRoot); err != nil {
				return err
			} else if action != "" {
				fmt.Fprintf(out, "%s shared-hook → %s", action, path)
				if note != "" {
					fmt.Fprintf(out, " (%s)", note)
				}
				fmt.Fprintln(out)
			}
			return nil
		},
	}

	agents.AddCommand(install, remove)
	register(agents)
}

func expandAgentTargets(name string) ([]string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "all":
		return []string{"claude", "grok", "pi"}, nil
	case "claude", "grok", "pi":
		return []string{n}, nil
	default:
		return nil, fmt.Errorf("agents: unknown agent %q (want claude, grok, pi, or all)", name)
	}
}

func printScaffoldOutcome(out io.Writer, name, rel string, added bool, updated, incomplete []string) {
	if len(updated) > 0 {
		fmt.Fprintf(out, "updated %s scaffold → %s (%s)\n", name, rel, strings.Join(updated, "; "))
	} else if added {
		fmt.Fprintf(out, "created %s scaffold → %s\n", name, rel)
	} else {
		fmt.Fprintf(out, "unchanged %s scaffold → %s\n", name, rel)
	}
	if len(incomplete) > 0 {
		fmt.Fprintf(out, "WARN  %s — incomplete satelle hooks after heal: missing %s\n",
			rel, strings.Join(incomplete, ", "))
	}
}
