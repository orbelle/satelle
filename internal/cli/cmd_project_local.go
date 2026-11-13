package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/uipush"
)

// projectLocalCmds are the verbs over this machine's projects (repos or
// directories, ~/.satelle/config.toml [projects] paths) and satelled's
// partitions of them.
func projectLocalCmds() []*cobra.Command {
	add := &cobra.Command{
		Use: "add [path]", Short: "Add a project to this machine's projects (default: this repo) and show it in satelled", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := repoArg(args)
			if err != nil {
				return err
			}
			return addLocalProject(cmd, path)
		},
	}
	remove := &cobra.Command{
		Use: "remove [path]", Short: "Remove a project from this machine's projects and drop its satelled partition", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := repoArg(args)
			if err != nil {
				return err
			}
			gc, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			if gc.Projects.Remove(path) {
				if err := config.SaveGlobal(gc); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed project %s from this machine's projects\n", path)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s was not a project on this machine\n", path)
			}
			return dropPartition(cmd, config.RepoKey(path))
		},
	}
	partitions := &cobra.Command{
		Use: "partitions", Short: "List satelled's partitions (one per project it has been sent)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			details, err := homePartitions(cmd.Context())
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
	}
	var force bool
	prune := &cobra.Command{
		Use: "prune <repo_key>", Short: "Drop a partition whose project is gone", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			details, err := homePartitions(cmd.Context())
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
			return fmt.Errorf("no partition %q; see `satelle project partitions`", args[0])
		},
	}
	prune.Flags().BoolVar(&force, "force", false, "drop the partition even though its project still exists")
	return []*cobra.Command{add, remove, partitions, prune}
}

// homePartitions lists this machine's satelled partitions with their recorded
// project path, read from this home's mirror database. With no mirror database
// it returns none and creates nothing. The store is closed before it returns.
func homePartitions(ctx context.Context) ([]mirror.PartitionDetail, error) {
	path := mirror.DefaultPath(config.GlobalDir())
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	ms, err := mirror.Open(path)
	if err != nil {
		return nil, err
	}
	defer ms.Close()
	return ms.ListPartitionDetails(ctx)
}

// localProject is one of this machine's projects, as project list shows it.
type localProject struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	State string `json:"state"`
	Bound string `json:"bound"`
}

// localProjects lists this machine's projects: whether each path is there and
// the satelle.dev project it is bound to ("-" when none).
func localProjects() ([]localProject, error) {
	gc, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	out := make([]localProject, 0, len(gc.Projects.Paths))
	for _, p := range gc.Projects.Paths {
		lp := localProject{Name: config.RepoName(p), Path: p, State: "ok", Bound: "-"}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			lp.State = "missing"
		} else if r, err := findRepo(p); err == nil {
			if b, ok, _ := loadBinding(r); ok && b.Slug != "" {
				lp.Bound = b.Slug
			}
		}
		out = append(out, lp)
	}
	return out, nil
}

// addLocalProject adds path to this machine's projects (~/.satelle/config.toml
// [projects]), which satelled shows and `satelle ledger autosync` replicates,
// and seeds its satelled partition.
func addLocalProject(cmd *cobra.Command, path string) error {
	gc, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	if !gc.Projects.Add(path) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s is already a project on this machine\n", path)
	} else {
		if err := config.SaveGlobal(gc); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "added project %s (%d projects on this machine)\n", path, len(gc.Projects.Paths))
	}
	seedUI(cmd, path)
	return nil
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

// repoArg is the project a project command names: the argument, else the
// repo at the working directory, as its canonical root.
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
		return "", errors.New("not in a satelle project; name the project's path")
	}
	return filepath.Abs(config.CanonicalRepoRoot(filepath.Dir(r.DataDir)))
}
