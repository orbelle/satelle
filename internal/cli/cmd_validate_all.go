package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
)

func init() {
	register(validateAllCmd())
}

// validateAllCmd is the top-level `satelle validate` — runs every per-kind
// deterministic check plus the cross-substrate wikilink audit.
func validateAllCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate all authored substrate (per-kind checks + wikilink resolution)",
		Long: `Run every per-kind structure check across this repo's authored substrate, then
resolve wikilinks between the files.

The cross-file half is what a per-kind validate cannot do: a skill that cites a
principle by name is only correct if that name resolves, and a rename breaks it
silently. Read-only, deterministic, no agent — run it after any substrate edit.`,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var failed int
			for _, kind := range []string{"workflows", "skills", "principles", "tasks"} {
				fmt.Fprintf(out, "# %s\n", kind)
				if err := validateKind(cmd, a, kind, ""); err != nil {
					failed++
				}
				fmt.Fprintln(out)
			}
			// The machine catalog must load: a retired table or an unknown key is
			// a load error, the same one `agent validate` reports.
			fmt.Fprintln(out, "# agent catalog")
			if _, gerr := config.LoadGlobalAgents(); gerr != nil {
				failed++
				fmt.Fprintf(out, "FAIL  %v\n", gerr)
			} else {
				fmt.Fprintln(out, "PASS  agent catalog")
			}
			fmt.Fprintln(out)
			if failed > 0 {
				return fmt.Errorf("validate failed")
			}
			return nil
		},
	}
}
