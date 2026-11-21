package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/pack"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func packCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pack",
		Short: "Fetch, install, update and remove packs from the satelle.dev marketplace",
	}
	cmd.AddCommand(packFetchCmd(), packInstallCmd(), packUpdateCmd(), packRemoveCmd())
	return cmd
}

func packFetchCmd() *cobra.Command {
	var version, dir, server string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "fetch <ref> --version V --dir D [--server URL]",
		Short: "Fetch a verified version of a pack from the marketplace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, ver, err := packArgs(args[0], version)
			if err != nil {
				return err
			}
			target := strings.TrimSpace(dir)
			if target == "" {
				return errors.New("--dir is required")
			}
			verified, err := fetchVerified(cmd, ref, ver, server)
			if err != nil {
				return err
			}
			if err := pack.Write(target, verified); err != nil {
				return err
			}

			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"ref":     ref,
					"name":    verified.Name(),
					"version": verified.Version(),
					"digest":  verified.Digest(),
					"dir":     target,
					"files":   verified.Paths(),
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "fetched %s@%s (%d file(s), digest %s) into %s\n", ref, verified.Version(), len(verified.Paths()), verified.Digest()[:12], target)
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "pack version to fetch")
	cmd.Flags().StringVar(&dir, "dir", "", "directory to write verified pack files into")
	cmd.Flags().StringVar(&server, "server", "", "satelle.dev server URL")
	asJSON = jsonFlag(cmd)
	return cmd
}

func packArgs(ref, version string) (string, string, error) {
	ref, version = strings.TrimSpace(ref), strings.TrimSpace(version)
	if ref == "" {
		return "", "", errors.New("pack ref is required")
	}
	if version == "" {
		return "", "", errors.New("--version is required")
	}
	return ref, version, nil
}

func fetchVerified(cmd *cobra.Command, ref, version, server string) (pack.Verified, error) {
	srv, err := projectServer(server)
	if err != nil {
		return pack.Verified{}, err
	}
	c, err := dial(srv)
	if err != nil {
		return pack.Verified{}, err
	}
	defer c.Close()
	bundle, err := c.Packs.FetchPack(cmd.Context(), &satellepb.GetPackRequest{Ref: ref, Version: version})
	if err != nil {
		if hosted.IsNotFound(err) {
			return pack.Verified{}, fmt.Errorf("pack %s version %s was not found", ref, version)
		}
		return pack.Verified{}, hosted.Describe("pack fetch", err)
	}
	verified, err := pack.Verify(bundle)
	if err != nil {
		return pack.Verified{}, err
	}
	if verified.Version() != version {
		return pack.Verified{}, fmt.Errorf("pack %s returned version %q, want %q", ref, verified.Version(), version)
	}
	return verified, nil
}

const packStagePrefix = ".satelle-pack-staging"

func packInstallCmd() *cobra.Command {
	var version, server string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "install <ref> --version V [--server URL]",
		Short: "Install a verified pack's skill directories under .satelle/skills and record them in .satelle/packs.toml",
		Long: "Install fetches and verifies a pack version, then places its entry and member skill directories under .satelle/skills " +
			"and records its ref, version, digest, skills and each file's sha256 in .satelle/packs.toml. It needs a seat at this worktree on a state " +
			"whose performer may change the tree, honours [gate] locked_paths for every file it writes, and writes nothing when a check fails.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, ver, err := packArgs(args[0], version)
			if err != nil {
				return err
			}
			data, names, check, closeFn, err := packTarget("pack install")
			if err != nil {
				return err
			}
			defer closeFn()
			verified, err := fetchVerified(cmd, ref, ver, server)
			if err != nil {
				return err
			}
			res, err := pack.Install(data, ref, verified, names, check)
			if err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"ref":               res.Ref,
					"name":              res.Name,
					"version":           res.Version,
					"digest":            res.Digest,
					"skills":            res.Skills,
					"files":             res.Files,
					"record":            filepath.ToSlash(filepath.Join(filepath.Base(data), names.Record)),
					"already_installed": res.AlreadyInstalled,
				})
			}
			if res.AlreadyInstalled {
				fmt.Fprintf(cmd.OutOrStdout(), "%s@%s is already installed (digest %.12s); nothing written\n", res.Ref, res.Version, res.Digest)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s@%s (%d skill(s), %d file(s), digest %.12s) into .satelle/skills: %s; recorded in .satelle/packs.toml\n",
				res.Ref, res.Version, len(res.Skills), res.Files, res.Digest, strings.Join(res.Skills, ", "))
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "pack version to install")
	cmd.Flags().StringVar(&server, "server", "", "satelle.dev server URL")
	asJSON = jsonFlag(cmd)
	return cmd
}

func packTarget(verb string) (string, pack.Names, func(string) error, func(), error) {
	k, r, err := openKernel()
	if err != nil {
		return "", pack.Names{}, nil, nil, err
	}
	closeFn := func() { k.Close() }
	fail := func(err error) (string, pack.Names, func(string) error, func(), error) {
		closeFn()
		return "", pack.Names{}, nil, nil, err
	}
	session := sessionFromEnv()
	g := hook.Gate{
		Root:      r.Tree,
		Locked:    k.Settings.Gate.LockedPaths,
		TempRoots: []string{},
		Grant:     func(tree string) hook.Grant { return grantAt(k, session, tree) },
	}
	resolve := func(p string) (string, error) {
		c := canonicalPath(p)
		if !hook.Within(r.Tree, c) {
			return "", fmt.Errorf("%s: %s resolves to %s, outside the worktree %s", verb, p, c, r.Tree)
		}
		return c, nil
	}
	check := func(p string) error {
		c, err := resolve(p)
		if err != nil {
			return err
		}
		if v := g.Edit(c); !v.Allow {
			if session == "" {
				return errors.New(v.Reason + " (no session id: pass --session or set SATELLE_SESSION)")
			}
			return errors.New(v.Reason)
		}
		return nil
	}
	data, err := resolve(r.DataDir)
	if err != nil {
		return fail(err)
	}
	names := pack.Names{Skills: kernel.SkillsDir, Record: kernel.PacksFile, StagePrefix: packStagePrefix, MergeDir: kernel.PackMergeDir}
	recordPath := filepath.Join(data, names.Record)
	for _, p := range []string{filepath.Join(data, names.Skills), recordPath} {
		if err := check(p); err != nil {
			return fail(err)
		}
	}
	if _, err := pack.LoadRecord(recordPath); err != nil {
		return fail(err)
	}
	return data, names, check, closeFn, nil
}

func packUpdateCmd() *cobra.Command {
	var version, server string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "update <ref> --version V [--server URL]",
		Short: "Move an installed pack to another verified version, leaving locally edited files for the agent to merge",
		Long: "Update fetches and verifies a pack version and moves the installed pack recorded in .satelle/packs.toml to it. " +
			"A file whose sha256 still matches packs.toml is replaced, removed or added. An edited file (its sha256 differs, it is missing, " +
			"or it is a local file the new version now publishes) is left exactly as it is, and its installed-version and new-version copies " +
			"are written under .satelle/pack-merge/<ref>/<version>/ and named for the agent to merge; satelle merges nothing. packs.toml then " +
			"records the new version. It needs a seat at this worktree on a state whose performer may change the tree, honours [gate] locked_paths " +
			"for every file it writes, and writes nothing when a check fails.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, ver, err := packArgs(args[0], version)
			if err != nil {
				return err
			}
			data, names, check, closeFn, err := packTarget("pack update")
			if err != nil {
				return err
			}
			defer closeFn()
			next, err := fetchVerified(cmd, ref, ver, server)
			if err != nil {
				return err
			}
			rec, err := pack.LoadRecord(filepath.Join(data, names.Record))
			if err != nil {
				return err
			}
			installed := func() (pack.Verified, error) {
				in, _ := rec.Find(ref)
				return fetchVerified(cmd, ref, in.Version, server)
			}
			res, err := pack.Update(data, ref, next, installed, names, check)
			if err != nil {
				return err
			}
			skills := filepath.ToSlash(filepath.Join(filepath.Base(data), names.Skills))
			shown := func(paths []string) []string {
				out := make([]string, len(paths))
				for i, p := range paths {
					out[i] = skills + "/" + p
				}
				return out
			}
			edited := make([]pack.EditedFile, len(res.Edited))
			for i, e := range res.Edited {
				edited[i] = pack.EditedFile{Path: skills + "/" + e.Path, Installed: e.Installed, New: e.New}
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"ref":               res.Ref,
					"name":              res.Name,
					"from_version":      res.FromVersion,
					"version":           res.Version,
					"digest":            res.Digest,
					"skills":            res.Skills,
					"replaced":          shown(res.Replaced),
					"added":             shown(res.Added),
					"removed":           shown(res.Removed),
					"edited":            edited,
					"record":            filepath.ToSlash(filepath.Join(filepath.Base(data), names.Record)),
					"already_installed": res.AlreadyInstalled,
				})
			}
			out := cmd.OutOrStdout()
			if res.AlreadyInstalled {
				fmt.Fprintf(out, "%s@%s is already installed; nothing written\n", res.Ref, res.Version)
				return nil
			}
			fmt.Fprintf(out, "updated %s %s -> %s (%d replaced, %d added, %d removed, %d edited left for merging, digest %.12s); recorded in .satelle/packs.toml\n",
				res.Ref, res.FromVersion, res.Version, len(res.Replaced), len(res.Added), len(res.Removed), len(res.Edited), res.Digest)
			none := func(s string) string {
				if s == "" {
					return "(none)"
				}
				return s
			}
			for _, e := range edited {
				fmt.Fprintf(out, "edited %s: installed %s, new %s\n", e.Path, none(e.Installed), none(e.New))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "pack version to update to")
	cmd.Flags().StringVar(&server, "server", "", "satelle.dev server URL")
	asJSON = jsonFlag(cmd)
	return cmd
}

func packRemoveCmd() *cobra.Command {
	var force bool
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "remove <ref> [--force]",
		Short: "Delete an installed pack's recorded files and its .satelle/packs.toml entry",
		Long: "Remove deletes each file .satelle/packs.toml records for the pack, prunes the skill directories that leaves empty, " +
			"and drops the pack's entry from packs.toml. A recorded file whose sha256 differs from packs.toml is locally edited: remove " +
			"refuses and names each one, unless --force, which deletes them too. A file the pack did not record is never deleted; it and " +
			"its directory are kept. .satelle/pack-merge is left alone. It contacts no marketplace, needs a seat at this worktree on a state " +
			"whose performer may change the tree, honours [gate] locked_paths for every path it changes, and writes nothing when a check fails. " +
			"A workflow that still names a removed skill is reported by satelle validate.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := strings.TrimSpace(args[0])
			if ref == "" {
				return errors.New("pack ref is required")
			}
			data, names, check, closeFn, err := packTarget("pack remove")
			if err != nil {
				return err
			}
			defer closeFn()
			res, err := pack.Remove(data, ref, force, names, check)
			if err != nil {
				return err
			}
			skills := filepath.ToSlash(filepath.Join(filepath.Base(data), names.Skills))
			shown := func(paths []string) []string {
				out := make([]string, len(paths))
				for i, p := range paths {
					out[i] = skills + "/" + p
				}
				return out
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"ref":     res.Ref,
					"name":    res.Name,
					"version": res.Version,
					"digest":  res.Digest,
					"skills":  res.Skills,
					"removed": shown(res.Removed),
					"edited":  shown(res.Edited),
					"missing": shown(res.Missing),
					"kept":    shown(res.Kept),
					"record":  filepath.ToSlash(filepath.Join(filepath.Base(data), names.Record)),
					"forced":  res.Forced,
				})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "removed %s@%s (%d file(s) from %s); dropped from .satelle/packs.toml\n",
				res.Ref, res.Version, len(res.Removed), strings.Join(res.Skills, ", "))
			for _, p := range shown(res.Edited) {
				fmt.Fprintf(out, "removed edited %s\n", p)
			}
			for _, p := range shown(res.Missing) {
				fmt.Fprintf(out, "missing %s: already gone\n", p)
			}
			for _, p := range shown(res.Kept) {
				fmt.Fprintf(out, "kept %s: not recorded by the pack\n", p)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove locally edited recorded files too")
	asJSON = jsonFlag(cmd)
	return cmd
}
