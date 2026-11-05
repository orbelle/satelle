package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/hook"
)

func agentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Install or remove harness hook files (" + strings.Join(hook.Harnesses, ", ") + ")",
		Long: `agents install writes a harness's hook file so its sessions call satelle's
hook handlers: claude (.claude/settings.json), grok (.grok/hooks/satelle.json)
and pi (.pi/extensions/satelle.ts). Entries that are not satelle's are kept.
agents remove strips satelle's entries again.`,
	}
	for _, verb := range []string{"install", "remove"} {
		cmd.AddCommand(&cobra.Command{
			Use:       verb + " <harness>...",
			Short:     verb + " harness hook files in this repo",
			Args:      cobra.MinimumNArgs(1),
			ValidArgs: hook.Harnesses,
			RunE: func(cmd *cobra.Command, args []string) error {
				r, err := findRepo("")
				if err != nil {
					return err
				}
				for _, h := range args {
					var path string
					if verb == "install" {
						path, err = hook.Install(r.Tree, h)
					} else {
						path, err = hook.Remove(r.Tree, h)
					}
					if err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s hooks: %s\n", map[string]string{"install": "installed", "remove": "removed"}[verb], h, path)
				}
				return nil
			},
		})
	}
	return cmd
}
