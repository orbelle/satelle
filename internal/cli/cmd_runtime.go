package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
)

// runtimeEntry is one home-keyed runtime dir.
type runtimeEntry struct {
	Dir, Repo, Status string // status: linked | stale | unknown
}

func runtimeEntries() ([]runtimeEntry, error) {
	home := config.GlobalDir()
	ents, err := os.ReadDir(home)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []runtimeEntry
	for _, e := range ents {
		if !e.IsDir() || !config.IsRuntimeKeyDir(e.Name()) {
			continue
		}
		dir := filepath.Join(home, e.Name())
		ent := runtimeEntry{Dir: dir, Repo: config.ReadRepoPathMarker(dir), Status: "unknown"}
		if ent.Repo != "" {
			ent.Status = "stale"
			if st, err := os.Stat(ent.Repo); err == nil && st.IsDir() {
				ent.Status = "linked"
			}
		}
		out = append(out, ent)
	}
	return out, nil
}

func runtimeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runtime",
		Short: "Inspect the home-keyed runtime dirs (ledger, blobs, cache, logs)",
		Long: `A repo's runtime state (its ledger, blobs, sqlite cache and logs) lives under
~/.satelle/<repo-key>/, keyed by the repository so its worktrees share one
ledger. Its configuration stays under <repo>/.satelle.`,
	}
	cmd.AddCommand(&cobra.Command{
		Use: "path", Short: "Print this repo's runtime dir", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := findRepo("")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), r.Runtime)
			return nil
		},
	}, &cobra.Command{
		Use: "list", Short: "List runtime dirs and whether their repo still exists", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ents, err := runtimeEntries()
			if err != nil {
				return err
			}
			for _, e := range ents {
				fmt.Fprintf(cmd.OutOrStdout(), "%-8s %s  %s\n", e.Status, e.Dir, e.Repo)
			}
			return nil
		},
	})
	var yes, unknown bool
	reap := &cobra.Command{
		Use:   "reap",
		Short: "Report, and with --yes remove, runtime dirs whose repo is gone",
		Long: `reap lists runtime dirs whose recorded repo no longer exists (and, with
--include-unknown, those with no record) and removes exactly those only with
--yes. A dir whose repo exists is never removed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ents, err := runtimeEntries()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			n := 0
			for _, e := range ents {
				if e.Status != "stale" && !(unknown && e.Status == "unknown") {
					continue
				}
				n++
				if !yes {
					fmt.Fprintf(out, "would remove %s (%s %s)\n", e.Dir, e.Status, e.Repo)
					continue
				}
				if err := os.RemoveAll(e.Dir); err != nil {
					return err
				}
				fmt.Fprintf(out, "removed %s\n", e.Dir)
			}
			if n == 0 {
				fmt.Fprintln(out, "nothing to reap")
			} else if !yes {
				fmt.Fprintln(out, "nothing removed; re-run with --yes to remove what is listed")
			}
			return nil
		},
	}
	reap.Flags().BoolVar(&yes, "yes", false, "remove what is reported")
	reap.Flags().BoolVar(&unknown, "include-unknown", false, "also remove dirs with no recorded repo")
	cmd.AddCommand(reap)
	return cmd
}
