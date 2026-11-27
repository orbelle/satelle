package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/pack"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func packCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pack",
		Short: "Fetch, install, update, remove, report, publish and change the visibility of packs on the satelle.dev marketplace",
	}
	cmd.AddCommand(packFetchCmd(), packInstallCmd(), packUpdateCmd(), packRemoveCmd(), packStatusCmd(), packPublishCmd(), packVisibilityCmd())
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
			"whose performer may change the tree, honours [gate] locked_paths for every file it writes, and writes nothing when a check fails. " +
			"It validates the whole configuration as it will stand with the pack's skills written, and writes nothing when that configuration is invalid.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, ver, err := packArgs(args[0], version)
			if err != nil {
				return err
			}
			data, names, check, closeFn, err := packTarget("pack install", true)
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

// treeEditCheck resolves the repo and builds the edit gate a satelle command
// that writes tracked files passes for each path: resolveInTree, then
// hook.Gate.Edit (seat, locked paths, other repositories).
func treeEditCheck(verb string, prospective bool) (repo, func(string) error, func(), error) {
	r, err := findRepo("")
	if err != nil {
		return repo{}, nil, nil, err
	}
	session := sessionFromEnv()
	g := hook.Gate{Root: r.Tree, TempRoots: []string{}}
	closeFn := func() {}
	if prospective && len(kernel.Validate(r.DataDir)) > 0 {
		st, err := kernel.LoadSettings(r.DataDir)
		if err != nil {
			return repo{}, nil, nil, err
		}
		g.Locked = st.Gate.LockedPaths
		g.Grant = func(tree string) hook.Grant { return seatGrantUnconfigured(r, session, tree) }
	} else {
		k, err := r.open()
		if err != nil {
			return repo{}, nil, nil, err
		}
		closeFn = func() { k.Close() }
		g.Locked = k.Settings.Gate.LockedPaths
		g.Grant = func(tree string) hook.Grant { return grantAt(k, session, tree) }
	}
	check := func(p string) error {
		c, err := resolveInTree(verb, r.Tree, p)
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
	return r, check, closeFn, nil
}

func packTarget(verb string, prospective bool) (string, pack.Names, func(string) error, func(), error) {
	r, check, closeFn, err := treeEditCheck(verb, prospective)
	if err != nil {
		return "", pack.Names{}, nil, nil, err
	}
	fail := func(err error) (string, pack.Names, func(string) error, func(), error) {
		closeFn()
		return "", pack.Names{}, nil, nil, err
	}
	data, err := resolveInTree(verb, r.Tree, r.DataDir)
	if err != nil {
		return fail(err)
	}
	names := pack.Names{Skills: kernel.SkillsDir, Record: kernel.PacksFile, StagePrefix: packStagePrefix, MergeDir: kernel.PackMergeDir,
		Validate: func(skills string) error {
			if errs := kernel.ValidateWithSkills(data, skills); len(errs) > 0 {
				return errors.Join(errs...)
			}
			return nil
		}}
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
			"for every file it writes, and writes nothing when a check fails. It validates the whole configuration as it will stand with the pack's skills written, " +
			"and writes nothing when that configuration is invalid.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, ver, err := packArgs(args[0], version)
			if err != nil {
				return err
			}
			data, names, check, closeFn, err := packTarget("pack update", true)
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
			data, names, check, closeFn, err := packTarget("pack remove", false)
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

const (
	marketplaceUnread = "the marketplace could not be read: "
	packLookupTimeout = 10 * time.Second
)

func packStatusCmd() *cobra.Command {
	var server string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "status [--server URL]",
		Short: "List each installed pack's version, its locally edited and missing files, and the newest version the marketplace serves",
		Long: "Status reads .satelle/packs.toml and lists each installed pack's ref and version, every recorded file whose sha256 differs " +
			"(edited) or that is gone (missing), and the newest version the marketplace serves (its latest version; no version comparison). " +
			"It needs no seat and writes nothing. It refuses a symlinked record, skills directory, skill directory or file, and a .satelle " +
			"outside the worktree. When the marketplace cannot be read it says so in newest_error and still exits 0.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := findRepo("")
			if err != nil {
				return err
			}
			data, err := resolveInTree("pack status", r.Tree, r.DataDir)
			if err != nil {
				return err
			}
			names := pack.Names{Skills: kernel.SkillsDir, Record: kernel.PacksFile}
			sts, err := pack.Status(data, names)
			if err != nil {
				return err
			}
			if len(sts) > 0 {
				lookupNewest(cmd.Context(), server, sts)
			}
			skills := filepath.ToSlash(filepath.Join(filepath.Base(data), names.Skills))
			for i := range sts {
				for j, p := range sts[i].Edited {
					sts[i].Edited[j] = skills + "/" + p
				}
				for j, p := range sts[i].Missing {
					sts[i].Missing[j] = skills + "/" + p
				}
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"packs": sts})
			}
			out := cmd.OutOrStdout()
			if len(sts) == 0 {
				fmt.Fprintln(out, "no packs installed (.satelle/packs.toml records none)")
				return nil
			}
			for _, st := range sts {
				if st.NewestError != "" {
					fmt.Fprintf(out, "%s %s: newest unknown (%s)\n", st.Ref, st.Version, st.NewestError)
				} else {
					fmt.Fprintf(out, "%s %s: newest %s\n", st.Ref, st.Version, st.Newest)
				}
				for _, p := range st.Edited {
					fmt.Fprintf(out, "  edited %s\n", quoteControl(p))
				}
				for _, p := range st.Missing {
					fmt.Fprintf(out, "  missing %s\n", quoteControl(p))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "satelle.dev server URL")
	asJSON = jsonFlag(cmd)
	return cmd
}

func lookupNewest(ctx context.Context, server string, sts []pack.PackStatus) {
	unread := func(err error) {
		for i := range sts {
			sts[i].NewestError = marketplaceUnread + pack.Printable(err.Error())
		}
	}
	srv, err := projectServer(server)
	if err != nil {
		unread(err)
		return
	}
	c, err := dial(srv)
	if err != nil {
		unread(err)
		return
	}
	defer c.Close()
	for i := range sts {
		callCtx, cancel := context.WithTimeout(ctx, packLookupTimeout)
		resp, err := c.Packs.GetPack(callCtx, &satellepb.GetPackRequest{Ref: sts[i].Ref, Version: sts[i].Version})
		cancel()
		sts[i].Newest, sts[i].NewestError = newestOf(sts[i].Ref, sts[i].Version, resp, err)
	}
}

func newestOf(ref, version string, resp *satellepb.PackVersion, err error) (string, string) {
	switch {
	case err != nil && hosted.IsNotFound(err):
		return "", fmt.Sprintf("%spack %s version %s was not found", marketplaceUnread, ref, version)
	case err != nil:
		return "", marketplaceUnread + pack.Printable(hosted.Describe("pack status", err).Error())
	}
	latest := resp.GetPack().GetLatestVersion()
	switch {
	case latest == "":
		return "", marketplaceUnread + "it named no latest version"
	case pack.ValidateSegment(latest) != nil || pack.ValidateToken(latest) != nil:
		return "", marketplaceUnread + "it named a latest version that is not a plain version"
	}
	return latest, ""
}

func quoteControl(s string) string {
	if reporeg.HasUnsafe(s) {
		return strconv.Quote(s)
	}
	return s
}

func resolveInTree(verb, tree, p string) (string, error) {
	c := canonicalPath(p)
	if !hook.Within(tree, c) {
		return "", fmt.Errorf("%s: %s resolves to %s, outside the worktree %s", verb, p, c, tree)
	}
	return c, nil
}

var packVisibilities = map[string]satellepb.PackVisibility{
	"public":    satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC,
	"workspace": satellepb.PackVisibility_PACK_VISIBILITY_WORKSPACE,
	"shared":    satellepb.PackVisibility_PACK_VISIBILITY_SHARED,
}

func packVisibilityName(v satellepb.PackVisibility) string {
	for name, have := range packVisibilities {
		if have == v {
			return name
		}
	}
	return ""
}

func packVisibilityFlags(visibility string, shareWith []string) (satellepb.PackVisibility, []string, error) {
	v := strings.TrimSpace(visibility)
	if v == "" {
		return 0, nil, errors.New("--visibility is required: public, workspace or shared")
	}
	vis, ok := packVisibilities[v]
	if !ok {
		return 0, nil, fmt.Errorf("--visibility %q is not public, workspace or shared", v)
	}
	shared := make([]string, 0, len(shareWith))
	for _, id := range shareWith {
		id = strings.TrimSpace(id)
		if id == "" {
			return 0, nil, errors.New("--share-with needs a workspace id")
		}
		shared = append(shared, id)
	}
	if vis != satellepb.PackVisibility_PACK_VISIBILITY_SHARED && len(shared) > 0 {
		return 0, nil, errors.New("--share-with is only for --visibility shared")
	}
	if vis == satellepb.PackVisibility_PACK_VISIBILITY_SHARED && len(shared) == 0 {
		return 0, nil, errors.New("--visibility shared needs at least one --share-with workspace id")
	}
	if len(shared) == 0 {
		shared = nil
	}
	return vis, shared, nil
}

func packPublishCmd() *cobra.Command {
	var workspace, server, visibility string
	var shareWith []string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "publish <entry> --workspace ID --visibility public|workspace|shared [--share-with ID]... [--server URL]",
		Short: "Publish a pack version to the satelle.dev marketplace from its entry skill",
		Long: "Publish reads .satelle/skills/<entry>/pack.toml and publishes the pack at its version for the owner satelle.dev workspace, " +
			"sending the entry and member skill directories' files, sha256s and digest exactly as the working tree holds them, with paths " +
			"relative to .satelle/skills. It refuses a symlink or non-regular file in those directories and any layout install would refuse. " +
			"Only admins of the owner satelle.dev workspace may publish, and the same version with different files is refused; either way " +
			"nothing is published. Publish writes nothing locally. The pack's --visibility (public, workspace or shared) is required, with no " +
			"default, and is sent with the publish; shared names the workspaces it is shared with, one --share-with each.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws := strings.TrimSpace(workspace)
			if ws == "" {
				return errors.New("--workspace is required")
			}
			vis, shared, err := packVisibilityFlags(visibility, shareWith)
			if err != nil {
				return err
			}
			r, err := findRepo("")
			if err != nil {
				return err
			}
			data, err := resolveInTree("pack publish", r.Tree, r.DataDir)
			if err != nil {
				return err
			}
			bundle, err := pack.Collect(filepath.Join(data, kernel.SkillsDir), args[0])
			if err != nil {
				return err
			}
			manifest := bundle.GetVersion().GetManifest()
			srv, err := projectServer(server)
			if err != nil {
				return err
			}
			c, err := dial(srv)
			if err != nil {
				return err
			}
			defer c.Close()
			got, err := c.Packs.PublishPack(cmd.Context(), &satellepb.PublishPackRequest{
				OwnerWorkspaceId: ws, Manifest: manifest, Files: bundle.GetFiles(), Visibility: vis, SharedWith: shared,
			})
			switch status.Code(err) {
			case codes.OK:
			case codes.AlreadyExists:
				return fmt.Errorf("pack %s version %s is already published with different files for workspace %s; nothing was published", manifest.GetName(), manifest.GetVersion(), ws)
			case codes.PermissionDenied:
				return fmt.Errorf("you may not publish packs for workspace %s; nothing was published", ws)
			default:
				return hosted.Describe("pack publish", err)
			}
			ref := got.GetPack().GetRef()
			if ref == "" {
				ref = ws + "/" + manifest.GetName()
			}
			paths := make([]string, len(manifest.GetFiles()))
			for i, f := range manifest.GetFiles() {
				paths[i] = f.GetPath()
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"ref":                ref,
					"owner_workspace_id": ws,
					"name":               manifest.GetName(),
					"version":            manifest.GetVersion(),
					"digest":             manifest.GetDigest(),
					"files":              paths,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "published %s@%s (%d file(s), digest %.12s)\n", ref, manifest.GetVersion(), len(paths), manifest.GetDigest())
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "owner satelle.dev workspace id (required)")
	packVisibilityFlagVars(cmd, &visibility, &shareWith)
	cmd.Flags().StringVar(&server, "server", "", "satelle.dev server URL")
	asJSON = jsonFlag(cmd)
	return cmd
}

func packVisibilityFlagVars(cmd *cobra.Command, visibility *string, shareWith *[]string) {
	cmd.Flags().StringVar(visibility, "visibility", "", "who on satelle.dev may see the pack: public, workspace or shared (required)")
	cmd.Flags().StringArrayVar(shareWith, "share-with", nil, "a satelle.dev workspace id the pack is shared with (repeatable; only with --visibility shared)")
}

const packVisibilityUnchanged = "; the visibility was not changed"

func packVisibilityCmd() *cobra.Command {
	var server, visibility string
	var shareWith []string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "visibility <ref> --visibility public|workspace|shared [--share-with ID]... [--server URL]",
		Short: "Change who on satelle.dev may see a published pack",
		Long: "Visibility changes a published pack's visibility (public, workspace or shared) and the workspaces it is shared with, " +
			"without publishing a version. The ref is <owner workspace id>/<name>. Only admins of the owner satelle.dev workspace may " +
			"change it; to anyone who is not a member of that workspace the pack is not found. The flags are validated as publish " +
			"validates them, and a refused flag sends nothing. --share-with sends the whole shared list, so leaving a workspace out " +
			"revokes its share; --visibility workspace drops every share. Visibility writes nothing locally and needs no seat.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := strings.TrimSpace(args[0])
			owner, name, ok := strings.Cut(ref, "/")
			if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
				return fmt.Errorf("pack ref %q is not <owner workspace id>/<name>", ref)
			}
			vis, shared, err := packVisibilityFlags(visibility, shareWith)
			if err != nil {
				return err
			}
			srv, err := projectServer(server)
			if err != nil {
				return err
			}
			c, err := dial(srv)
			if err != nil {
				return err
			}
			defer c.Close()
			got, err := c.Packs.SetPackVisibility(cmd.Context(), &satellepb.SetPackVisibilityRequest{Ref: ref, Visibility: vis, SharedWith: shared})
			if errors.Is(err, hosted.ErrLoginRequired) {
				return fmt.Errorf("pack visibility: %w"+packVisibilityUnchanged, err)
			}
			switch status.Code(err) {
			case codes.OK:
			case codes.PermissionDenied:
				return fmt.Errorf("only admins of workspace %s may change the visibility of %s: ask an admin of %s to run satelle pack visibility"+packVisibilityUnchanged, owner, ref, owner)
			case codes.NotFound:
				return fmt.Errorf("pack %s was not found, or you are not a member of its owner workspace %s: check the ref and that you are signed in (satelle whoami) as a member of %s"+packVisibilityUnchanged, ref, owner, owner)
			default:
				return hosted.Describe("pack visibility", err)
			}
			if got.GetRef() != "" {
				ref = got.GetRef()
			}
			now := packVisibilityName(got.GetVisibility())
			if now == "" {
				return fmt.Errorf("pack visibility: satelle.dev changed %s but answered with no known visibility (%d); run satelle pack visibility again to confirm", ref, int32(got.GetVisibility()))
			}
			sharedWith := append([]string{}, got.GetSharedWith()...)
			if *asJSON {
				ownerID := got.GetOwnerWorkspaceId()
				if ownerID == "" {
					ownerID = owner
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"ref":                ref,
					"owner_workspace_id": ownerID,
					"visibility":         now,
					"shared_with":        sharedWith,
				})
			}
			line := ref + " is now " + now
			if len(sharedWith) > 0 {
				line += " (shared with " + strings.Join(sharedWith, ", ") + ")"
			}
			fmt.Fprintln(cmd.OutOrStdout(), line)
			return nil
		},
	}
	packVisibilityFlagVars(cmd, &visibility, &shareWith)
	cmd.Flags().StringVar(&server, "server", "", "satelle.dev server URL")
	asJSON = jsonFlag(cmd)
	return cmd
}
