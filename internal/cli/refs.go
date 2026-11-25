package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/substrate"
)

func commandRefs(root, cmd *cobra.Command, dir string) []substrate.Reference {
	g, err := resolveGovernance(dir, os.Environ(), true)
	if err != nil || (g.node != nil && g.node.matches(g.substrate, g.tree) != nil) {
		return nil
	}
	refs, err := substrate.References(g.substrate)
	if err != nil {
		return nil
	}
	var out []substrate.Reference
	for _, ref := range refs {
		for _, entry := range ref.Commands {
			words := strings.Fields(entry)
			if len(words) == 0 {
				continue
			}
			if c, rest, err := root.Find(words); err == nil && len(rest) == 0 && c == cmd {
				out = append(out, ref)
				break
			}
		}
	}
	return out
}

func printHelpRefs(w io.Writer, refs []substrate.Reference) {
	if len(refs) == 0 {
		return
	}
	fmt.Fprintln(w, "\nPrinciples and skills keyed to this command:")
	for _, ref := range refs {
		fmt.Fprintf(w, "  %s  %s\n", ref.Name, ref.Path)
		if ref.Description != "" {
			fmt.Fprintf(w, "      %s\n", ref.Description)
		}
	}
}

func printRunRefs(w io.Writer, refs []substrate.Reference) {
	for _, ref := range refs {
		fmt.Fprintf(w, "see %s: %s\n", ref.Name, ref.Path)
	}
}
