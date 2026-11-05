package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/help"
)

// helpCmd is `satelle help [topic|command]`: a help topic by name, else the
// named command's usage, else the topic list and the command tree.
func helpCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "help [topic|command]",
		Short: "Show a help topic or a command's usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				if t, ok := help.Get(args[0]); ok {
					fmt.Fprint(out, t.Body)
					return nil
				}
			}
			if len(args) > 0 {
				c, _, err := root.Find(args)
				if err != nil || c == root {
					return fmt.Errorf("no help topic or command %q; run `satelle help`", strings.Join(args, " "))
				}
				return c.Help()
			}
			if err := root.Help(); err != nil {
				return err
			}
			fmt.Fprintln(out, "\nHelp topics (satelle help <topic>):")
			for _, t := range help.List() {
				fmt.Fprintf(out, "  %-14s %s\n", t.Name, t.Title)
			}
			return nil
		},
	}
}
