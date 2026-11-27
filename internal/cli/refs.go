package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/substrate"
)

const ownSubstrateAnnotation = "satelle.own-substrate"

func ownSubstrateOnly(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[ownSubstrateAnnotation] == "true" {
			return true
		}
	}
	return false
}

func commandRefs(root, cmd *cobra.Command, dir string) []substrate.Reference {
	dataDir, _, err := substrateFor(dir, os.Environ(), !ownSubstrateOnly(cmd))
	if err != nil {
		return nil
	}
	refs, err := substrate.References(dataDir)
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
