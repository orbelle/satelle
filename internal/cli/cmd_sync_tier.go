package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/snapsync"
)

// materialiseProjectTier writes the deployed version's team-owned files to the
// project tier (sty_8dedc0a4), replacing the old tier whole. The team set is
// the deployed satelle.toml's [team] paths, under satelle.local.toml's. A path
// in an area a snapshot governs is written only when the snapshot names it, so
// a file deleted from the area stays gone. A repo with no explicit project
// binding, or whose runtime state is still in the tree, gets no tier.
func materialiseProjectTier(cmd *cobra.Command, cfg config.Config, repoRoot, dataDir string, client *hosted.Client, server string, manifest []hosted.ConfigItem, version int, snaps *configDeploySnapshots) error {
	project := cfg.SyncProject()
	if project == "" {
		return nil
	}
	out := cmd.OutOrStdout()
	rt := cfg.ResolveRuntimeDir(repoRoot)
	if rt.Legacy {
		fmt.Fprintf(out, "project tier: not written — runtime state is still under %s (run `satelle runtime migrate`).\n", rt.Dir)
		return nil
	}
	fetch := func(path string) ([]byte, bool, error) {
		body, _, err := client.ConfigFileContent(cmd.Context(), project, path, version)
		if errors.Is(err, hosted.ErrConfigFileMissing) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("project tier: fetch %s: %w", path, err)
		}
		return body, true, nil
	}
	var deployed []string
	for _, it := range manifest {
		if it.Path != config.ConfigName {
			continue
		}
		body, ok, err := fetch(it.Path)
		if err != nil {
			return err
		}
		if ok {
			if paths, declared := config.DeployedTeamPaths(body); declared {
				deployed = paths
			}
		}
	}
	paths := deployed
	if local, ok := config.LocalTeamPaths(dataDir); ok {
		paths = local
	}
	team := config.NewTeamSet(paths)
	files := map[string][]byte{}
	for _, it := range manifest {
		// The legacy bare agents.toml is not the agents layer, though a team glob
		// naming agents.toml owns it: only workflows/agents.toml fills the tier's.
		if config.LegacyAgentsKey(it.Path) {
			continue
		}
		if _, isRecord := snapsync.AreaOfRecordPath(it.Path); isRecord || !team.Owns(it.Path) {
			continue
		}
		if governed, named := snaps.governs(cfg, repoRoot, it.Path); governed && !named {
			continue
		}
		body, ok, err := fetch(it.Path)
		if err != nil {
			return err
		}
		if ok {
			files[it.Path] = body
		}
	}
	stamp := config.ProjectTierStamp{Project: project, Server: server, Version: version, TeamPaths: deployed}
	if err := config.WriteProjectTier(rt.Dir, stamp, files); err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Fprintln(out, "project tier: the deployed version holds no team-owned file — none written; every path resolves from the tree.")
		return nil
	}
	fmt.Fprintf(out, "project tier: %d team-owned file(s) of project %q → %s (these govern; the tree's copies of them do not).\n",
		len(files), project, config.ProjectTierDir(rt.Dir))
	return nil
}

// governs reports whether a planned snapshot governs path and, if so, whether
// that snapshot names it.
func (s *configDeploySnapshots) governs(cfg config.Config, repoRoot, path string) (governed, named bool) {
	if !s.owns(cfg, repoRoot, path) {
		return false, false
	}
	area := config.SyncAreaOf(cfg, repoRoot, path, s.driver.Ignore)
	for _, job := range s.jobs {
		if job.Area == area {
			_, ok := job.Snapshot()[path]
			return true, ok
		}
	}
	return true, false
}
