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
		Short: "Fetch and install packs from the satelle.dev marketplace",
	}
	cmd.AddCommand(packFetchCmd(), packInstallCmd())
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

// fetchVerified fetches ref at version and verifies every file, the digest and
// the version; a pack the caller may not read is not found.
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
			k, r, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
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
					return "", fmt.Errorf("pack install: %s resolves to %s, outside the worktree %s", p, c, r.Tree)
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
				return err
			}
			names := pack.Names{Skills: kernel.SkillsDir, Record: kernel.PacksFile, StagePrefix: packStagePrefix}
			recordPath := filepath.Join(data, names.Record)
			for _, p := range []string{filepath.Join(data, names.Skills), recordPath} {
				if err := check(p); err != nil {
					return err
				}
			}
			if _, err := pack.LoadRecord(recordPath); err != nil {
				return err
			}
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
