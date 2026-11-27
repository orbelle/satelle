package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

func reposCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Register onboarded target repositories (.satelle/repos.toml) and map their checkouts on this machine (.satelle/repos.local.toml)",
	}
	cmd.AddCommand(repoAddCmd(), repoListCmd())
	return cmd
}

const ignoreFile = ".gitignore"

func repoAddCmd() *cobra.Command {
	var e reporeg.Entry
	var path string
	cmd := &cobra.Command{
		Use:   "add <name> --origin <url> --path <dir> [--base <branch>] [--fetch <cmd>] [--tier standard|protected]",
		Short: "Register a target repository in .satelle/repos.toml and map its checkout here in .satelle/repos.local.toml",
		Long: "Add records the repository's name, canonical origin and optional base, fetch command and tier in the tracked " +
			".satelle/repos.toml, and its local checkout path in this machine's git-ignored .satelle/repos.local.toml, adding " +
			"repos.local.toml to .satelle/.gitignore when that file lacks it. A name repos.toml already declares maps only this " +
			"machine's checkout: --origin, when given, must name the same repository, and no other flag may differ from the tracked " +
			"value. self is the control repository itself and is never declared. A tier is standard or protected; none is standard.\n\n" +
			"It needs a seat at this worktree on a state whose performer may change the tree, honours [gate] locked_paths for each " +
			"file it writes, refuses a file that is a symlink or resolves outside the worktree, and writes nothing when a check fails. " +
			"An operator without a seat edits .satelle/repos.local.toml in an editor. An origin carrying credentials, a query or a " +
			"fragment is refused: register the plain repository URL. An existing checkout whose origin names another repository is " +
			"refused. Add runs no git write and never runs the fetch command; commit repos.toml and .gitignore through this repo's workflow.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return repoAdd(cmd, args[0], e, path)
		},
	}
	cmd.Flags().StringVar(&e.URL, "origin", "", "the repository's canonical origin URL (ssh, scp-style or http(s), no credentials)")
	cmd.Flags().StringVar(&path, "path", "", "this machine's checkout of the repository")
	cmd.Flags().StringVar(&e.Base, "base", "", "default base branch (default "+defaultRepoBase()+")")
	cmd.Flags().StringVar(&e.Fetch, "fetch", "", "the repository's own fetch command, stored and shown, never run by satelle")
	cmd.Flags().StringVar(&e.Tier, "tier", "", "standard or protected (default standard)")
	return cmd
}

func defaultRepoBase() string { return strings.TrimPrefix(kernel.DefaultBaseBranch, "origin/") }

func refuseIrregular(tree, p string) error {
	info, err := os.Lstat(p)
	if err != nil || info.Mode().IsRegular() {
		return nil
	}
	rel, _ := filepath.Rel(tree, p)
	return fmt.Errorf("repo add: %s is not a regular file", filepath.ToSlash(rel))
}

func ignoresLocal(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == kernel.ReposLocalFile {
			return true
		}
	}
	return false
}

func repoAdd(cmd *cobra.Command, name string, e reporeg.Entry, path string) error {
	r, check, closeFn, err := treeEditCheck("repo add", true)
	if err != nil {
		return err
	}
	defer closeFn()
	data, err := resolveInTree("repo add", r.Tree, r.DataDir)
	if err != nil {
		return err
	}
	trackedPath := filepath.Join(data, kernel.ReposFile)
	localPath := filepath.Join(data, kernel.ReposLocalFile)
	ignorePath := filepath.Join(data, ignoreFile)
	needIgnore := !ignoresLocal(ignorePath)
	writes := []string{trackedPath, localPath}
	if needIgnore {
		writes = append(writes, ignorePath)
	}
	for _, p := range writes {
		if err := check(p); err != nil {
			return err
		}
		if err := refuseIrregular(r.Tree, p); err != nil {
			return err
		}
	}
	if err := reporeg.CheckName(name); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("repo add: --path is required: this machine's checkout of the repository")
	}
	reg, err := reporeg.Load(data)
	if err != nil {
		return err
	}
	tracked, declared := reg.Tracked[name]
	given := cmd.Flags().Changed
	if declared {
		if !reg.EntryOK(name) {
			return errors.New("repo add: fix .satelle/" + kernel.ReposFile + " first; satelle validate lists its problems")
		}
		if given("origin") {
			if err := reporeg.CheckOrigin(name, e.URL); err != nil {
				return err
			}
			a, _ := reporeg.NormaliseOrigin(e.URL)
			b, _ := reporeg.NormaliseOrigin(tracked.URL)
			if a != b {
				return fmt.Errorf("repo add: %s is registered with origin %s, not %s; change .satelle/%s through this repo's workflow",
					name, showOrigin(tracked.URL), showOrigin(e.URL), kernel.ReposFile)
			}
		}
		if err := reporeg.CheckTier(name, e.Tier); err != nil {
			return err
		}
		for _, f := range []struct {
			flag      string
			got, want string
		}{{"base", e.Base, tracked.Base}, {"fetch", e.Fetch, tracked.Fetch}, {"tier", e.EffectiveTier(), tracked.EffectiveTier()}} {
			if given(f.flag) && f.got != f.want {
				return fmt.Errorf("repo add: %s is registered with another %s; change .satelle/%s through this repo's workflow", name, f.flag, kernel.ReposFile)
			}
		}
	} else {
		if !given("origin") {
			return errors.New("repo add: --origin is required to register a new repository")
		}
		if errs := reporeg.CheckEntry(name, e); len(errs) > 0 {
			return errors.Join(errs...)
		}
		tracked = e
	}
	dir, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if reporeg.HasUnsafe(dir) {
		return errors.New("repo add: --path holds a control or format character")
	}
	_, statErr := os.Stat(dir)
	if statErr == nil {
		if actual, err := (gitfacts.Repo{Dir: dir, Env: r.gitEnv}).RemoteURL("origin"); err == nil {
			a, aerr := reporeg.NormaliseOrigin(actual)
			b, _ := reporeg.NormaliseOrigin(tracked.URL)
			if aerr != nil || a != b {
				return fmt.Errorf("repo add: %s has origin %s but the registration is %s", show(dir), showOrigin(actual), showOrigin(tracked.URL))
			}
		}
	}
	out := cmd.OutOrStdout()
	if !declared {
		reg.Tracked[name] = tracked
		body, err := reporeg.RenderTracked(reg.Tracked)
		if err != nil {
			return err
		}
		if err := reporeg.Write(trackedPath, body); err != nil {
			return err
		}
		fmt.Fprintf(out, "registered %s (%s, tier %s) in .satelle/%s\n", name, showOrigin(tracked.URL), tracked.EffectiveTier(), kernel.ReposFile)
	}
	reg.Local[name] = reporeg.Local{Path: dir}
	body, err := reporeg.RenderLocal(reg.Local)
	if err != nil {
		return err
	}
	if err := reporeg.Write(localPath, body); err != nil {
		return err
	}
	fmt.Fprintf(out, "mapped %s to %s in .satelle/%s (this machine's file)\n", name, show(dir), kernel.ReposLocalFile)
	if needIgnore {
		if err := appendIgnore(ignorePath); err != nil {
			return err
		}
		fmt.Fprintf(out, "added %s to .satelle/%s\n", kernel.ReposLocalFile, ignoreFile)
	}
	if statErr != nil {
		fmt.Fprintf(out, "note: %s does not exist yet; satelle doctor reports it missing until the repository is cloned there\n", show(dir))
	}
	if !declared || needIgnore {
		fmt.Fprintf(out, "commit .satelle/%s and .satelle/%s through this repo's workflow\n", kernel.ReposFile, ignoreFile)
	}
	return nil
}

func appendIgnore(p string) error {
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		b = append(b, '\n')
	}
	return reporeg.Write(p, append(b, kernel.ReposLocalFile+"\n"...))
}

type repoTracked struct {
	URL   string `json:"origin"`
	Base  string `json:"base"`
	Tier  string `json:"tier"`
	Fetch string `json:"fetch"`
}

type repoLocal struct {
	Path string `json:"path"`
	URL  string `json:"origin,omitempty"`
}

type repoRow struct {
	Name    string      `json:"name"`
	Tracked repoTracked `json:"tracked"`
	Local   repoLocal   `json:"local"`
	State   string      `json:"state"`
}

func repoRows(r repo, reg reporeg.Registry) []repoRow {
	selfOrigin := "-"
	if o, err := (gitfacts.Repo{Dir: r.Tree, Env: r.gitEnv}).RemoteURL("origin"); err == nil {
		selfOrigin = showOrigin(o)
	}
	rows := []repoRow{{Name: reporeg.Self, Tracked: repoTracked{URL: selfOrigin, Base: "-", Tier: reporeg.TierStandard, Fetch: "-"},
		Local: repoLocal{Path: show(r.Tree)}, State: repoOK}}
	for _, name := range declaredNames(reg) {
		co := checkoutOf(reg, name, r.gitEnv)
		if co.state == repoInvalid {
			rows = append(rows, repoRow{Name: show(name), Tracked: repoTracked{URL: "-", Base: "-", Tier: "-", Fetch: "-"}, Local: repoLocal{Path: "-"}, State: repoInvalid})
			continue
		}
		e := reg.Tracked[name]
		row := repoRow{Name: show(name), State: co.state,
			Tracked: repoTracked{URL: showOrigin(e.URL), Base: show(e.Base), Tier: e.EffectiveTier(), Fetch: show(e.Fetch)},
			Local:   repoLocal{Path: "-"}}
		if row.Tracked.Base == "" {
			row.Tracked.Base = defaultRepoBase()
		}
		if row.Tracked.Fetch == "" {
			row.Tracked.Fetch = "-"
		}
		if co.path != "" {
			row.Local.Path = show(co.path)
		}
		if co.state == repoMismatch {
			row.Local.URL = showOrigin(co.actual)
		}
		rows = append(rows, row)
	}
	return rows
}

func repoListCmd() *cobra.Command {
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "list [--json]",
		Short: "List self and each registered repository with its tracked definition, this machine's checkout and its state",
		Long: "List prints self (the control repository) first, then each repository .satelle/repos.toml declares: its origin, base, " +
			"tier and fetch command, this machine's checkout from .satelle/repos.local.toml, and its state: ok, unmapped, missing, " +
			"mismatch (the checkout's origin names another repository), unreadable (no origin remote) or invalid (satelle validate " +
			"lists the problems). An absent tier is standard. Every origin is shown without credentials, query or fragment. It needs " +
			"no seat, writes nothing, and its only git command is a read of each checkout's origin URL.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := findRepo("")
			if err != nil {
				return err
			}
			reg, err := reporeg.Load(r.DataDir)
			if err != nil {
				return err
			}
			rows := repoRows(r, reg)
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"repos": rows})
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tORIGIN\tBASE\tTIER\tFETCH\tPATH\tSTATE")
			invalid := false
			for _, row := range rows {
				state := row.State
				if row.Local.URL != "" {
					state += " (checkout origin " + row.Local.URL + ")"
				}
				invalid = invalid || row.State == repoInvalid
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.Name, row.Tracked.URL, row.Tracked.Base, row.Tracked.Tier, row.Tracked.Fetch, row.Local.Path, state)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if invalid {
				fmt.Fprintln(cmd.OutOrStdout(), "satelle validate lists the problems")
			}
			return nil
		},
	}
	asJSON = jsonFlag(cmd)
	return cmd
}
