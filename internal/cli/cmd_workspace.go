package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/uipush"
)

func workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage the machine's registry of connected repos and satelled's partitions",
		Long: `The workspace registry (~/.satelle/config.toml [workspace] repos) lists the
repos satelled shows. Partitions are satelled's per-repo stores in the mirror
database.`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "add [path]", Short: "Register a repo (default: this one)", Args: cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				path, err := repoArg(args)
				if err != nil {
					return err
				}
				gc, err := config.LoadGlobal()
				if err != nil {
					return err
				}
				if !gc.Workspace.AddRepo(path) {
					fmt.Fprintf(cmd.OutOrStdout(), "%s is already registered\n", path)
				} else {
					if err := config.SaveGlobal(gc); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "registered %s (%d repos)\n", path, len(gc.Workspace.Repos))
				}
				seedUI(cmd, path)
				return nil
			},
		},
		&cobra.Command{
			Use: "list", Short: "List registered repos", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				gc, err := config.LoadGlobal()
				if err != nil {
					return err
				}
				for _, r := range gc.Workspace.Repos {
					state := "ok"
					if st, err := os.Stat(r); err != nil || !st.IsDir() {
						state = "missing"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%-8s %-20s %s\n", state, config.RepoName(r), r)
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "remove [path]", Short: "Unregister a repo and drop its partition", Args: cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				path, err := repoArg(args)
				if err != nil {
					return err
				}
				gc, err := config.LoadGlobal()
				if err != nil {
					return err
				}
				if gc.Workspace.RemoveRepo(path) {
					if err := config.SaveGlobal(gc); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "removed %s from the registry\n", path)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "%s was not registered\n", path)
				}
				return dropPartition(cmd, config.RepoKey(path))
			},
		},
		&cobra.Command{
			Use: "partitions", Short: "List satelled's partitions", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ms, err := mirror.Open(mirror.DefaultPath(config.GlobalDir()))
				if err != nil {
					return err
				}
				defer ms.Close()
				details, err := ms.ListPartitionDetails(cmd.Context())
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "%-40s %-20s %6s %5s  %s\n", "REPO_KEY", "SLUG", "STORY", "TASK", "PATH")
				for _, d := range details {
					fmt.Fprintf(out, "%-40s %-20s %6d %5d  %s\n", d.RepoKey, d.Slug, d.Stories, d.Tasks, d.Path)
				}
				return nil
			},
		},
	)
	var force bool
	prune := &cobra.Command{
		Use: "prune <repo_key>", Short: "Drop a partition whose repo is gone", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ms, err := mirror.Open(mirror.DefaultPath(config.GlobalDir()))
			if err != nil {
				return err
			}
			details, err := ms.ListPartitionDetails(cmd.Context())
			ms.Close()
			if err != nil {
				return err
			}
			for _, d := range details {
				if d.RepoKey != args[0] {
					continue
				}
				if st, err := os.Stat(d.Path); err == nil && st.IsDir() && !force {
					return fmt.Errorf("%s still exists; pass --force to drop its partition anyway", d.Path)
				}
				return dropPartition(cmd, d.RepoKey)
			}
			return fmt.Errorf("no partition %q; see `satelle workspace partitions`", args[0])
		},
	}
	prune.Flags().BoolVar(&force, "force", false, "drop the partition even though its repo still exists")
	cmd.AddCommand(prune)
	return cmd
}

// seedUI pushes a full snapshot of the repo at path to satelled.
func seedUI(cmd *cobra.Command, path string) {
	r, err := findRepo(path)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "not seeded: %v\n", err)
		return
	}
	pushed := false
	err = withUISource(r, func(c uipush.Client, src uipush.Source) error {
		pushed = true
		if err := c.Seed(cmd.Context(), src); err != nil {
			return fmt.Errorf("satelled at %s: %w", c.Endpoint, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "seeded satelled at %s\n", c.Endpoint)
		return nil
	})
	switch {
	case err != nil:
		fmt.Fprintf(cmd.OutOrStdout(), "not seeded: %v (the next change pushes it once satelled runs)\n", err)
	case !pushed:
		fmt.Fprintln(cmd.OutOrStdout(), "not seeded: the push to satelled is disabled")
	}
}

// dropPartition removes repoKey's partition through satelled's ingest, else
// (satelled not running) from the mirror database directly.
func dropPartition(cmd *cobra.Command, repoKey string) error {
	if c, ok := uiClient(); ok && c.Remove(cmd.Context(), repoKey) == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "dropped partition %s\n", repoKey)
		return nil
	}
	ms, err := mirror.Open(mirror.DefaultPath(config.GlobalDir()))
	if err != nil {
		return err
	}
	defer ms.Close()
	if err := ms.DeletePartition(cmd.Context(), repoKey); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "dropped partition %s\n", repoKey)
	return nil
}

// repoArg is the repo a workspace command names: the argument, else the repo
// at the working directory, as its canonical root.
func repoArg(args []string) (string, error) {
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return "", err
		}
		return filepath.Clean(abs), nil
	}
	r, err := findRepo("")
	if err != nil {
		return "", errors.New("not in a satelle repo; name the repo path")
	}
	return filepath.Abs(config.CanonicalRepoRoot(filepath.Dir(r.DataDir)))
}
