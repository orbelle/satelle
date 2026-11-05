// `satelle help` renders the embedded help topics to the terminal — the
// CLI contract (workflows, gates, agents). With no argument it
// lists the topics; with a topic name it prints that guide. The same topics back
// the web `/help` page (internal/help is the shared source). This is distinct
// from cobra's per-command `--help`: it documents the process, not the flags.
package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/help"
)

func init() {
	register(newHelpCmd())
}

// newHelpCmd builds the `satelle help` command (extracted so routing is testable).
func newHelpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "help [topic]",
		Short: "Read satelle's guides (workflows, gates, agents)",
		Long: `help renders satelle's process guides. Run with no argument to list the
topics, or pass a topic name to print that guide. These document the process
(how a story flows and what each reviewer checks); for command flags use
` + "`satelle <command> --help`" + `.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "Topics — `satelle help <topic>`:")
				fmt.Fprintln(tw)
				for _, t := range help.List() {
					fmt.Fprintf(tw, "  %s\t%s\n", t.Name, t.Title)
				}
				return tw.Flush()
			}
			if t, ok := help.Get(args[0]); ok {
				body := t.Body
				if args[0] == "agent-dispatch" {
					body = sessionContextHelpBody(body)
				}
				fmt.Fprintln(out, body)
				return nil
			}
			// Not a process topic — if it names a command, route to that command's
			// help (so `satelle help story` works) rather than a flat error.
			if c, _, ferr := cmd.Root().Find(args); ferr == nil && c != nil && c != cmd.Root() {
				return c.Help()
			}
			return fmt.Errorf("unknown help topic %q — run `satelle help` to list process topics, or `satelle <command> --help` for command flags", args[0])
		},
	}
	return cmd
}

// sessionContextHelpBody threads the config-resolved channel and clip into the
// harness-facts table the topic embeds. The embedded table is the un-injected
// rendering; a repo override changes the cell. Missing config leaves the embed.
func sessionContextHelpBody(body string) string {
	plain := agentcli.HarnessFactsTableMarkdown()
	cfg, _, err := config.Load("")
	if err != nil {
		cfg = config.Config{}
	}
	applySessionContextFacts(cfg)
	rendered := agentcli.HarnessFactsTableMarkdown()
	if plain == rendered || !strings.Contains(body, plain) {
		return body
	}
	return strings.Replace(body, plain, rendered, 1)
}

// applySessionContextFacts records each harness's resolved session-context
// channel and clip on the adapter row. agentcli does not read config.
func applySessionContextFacts(cfg config.Config) {
	for _, name := range cfg.BudgetHarnesses() {
		agentcli.SetSessionContext(name, cfg.SessionContextEvent(name), cfg.ToolContextLimitChars(name))
	}
}
