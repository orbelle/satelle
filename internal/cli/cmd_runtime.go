// `satelle runtime` — inspect and migrate the home-keyed runtime plane
// (sty_4660bbe1 / epic:substrate-planes). Runtime state (satelle.db, logs,
// backups, stories cache) lives under ~/.satelle/<repo-key>/; authored substrate
// stays under <repo>/.satelle. Migration is EXPLICIT — open never moves a DB.
package cli

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/store"
	_ "modernc.org/sqlite"
)

func init() {
	root := &cobra.Command{
		Use:   "runtime",
		Short: "Inspect or migrate the home-keyed runtime plane (~/.satelle/<repo-key>/)",
		Long: `Runtime state (satelle.db, logs/, backups/, stories/) lives under
~/.satelle/<repo-key>/, keyed by a stable repo identity so worktrees share one
store. Authored substrate stays under <repo>/.satelle.

  satelle runtime path      print the resolved runtime dir and whether it is legacy
  satelle runtime migrate   copy a legacy in-repo DB + runtime dirs to the home key

Migration is never automatic: an unmigrated repo still opens from the legacy
path and prints a deprecation note. See decision-substrate-planes-local-first.`,
	}

	pathCmd := &cobra.Command{
		Use:   "path",
		Short: "Print the resolved runtime directory for this repo",
		Long: `Print the runtime directory satelle resolved for this repo — where the database,
logs and backups actually live.

The runtime plane is home-keyed (~/.satelle/<repo-key>/), NOT in the repo, so
this is the answer to "where is my data" that a directory listing will not give
you. Read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := config.Load("")
			if err != nil && err != config.ErrNotFound {
				return err
			}
			repoRoot := "."
			if cfgPath != "" {
				repoRoot = config.RepoRootFromConfigPath(cfgPath)
			} else if wd, e := os.Getwd(); e == nil {
				repoRoot = wd
			}
			return runRuntimePath(cmd.OutOrStdout(), cfg, repoRoot)
		},
	}

	var orphansOnly bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List home-keyed runtime dirs under ~/.satelle (or SATELLE_HOME)",
		Long: `List each home-keyed runtime key dir under the machine home:

  key        directory basename (<name>-<hex8>)
  status     linked | stale | unknown
  repo       resolved repo root (from repo.path marker or workspace registry)
  size       approximate on-disk size
  db_mtime   mtime of satelle.db when present

Status:
  linked   marker or registry match and the repo root still exists
  stale    resolved root no longer exists
  unknown  no marker and no registry match (typical of leaked test key dirs)

Does not delete anything, ever. When it finds unknown/stale dirs it points at
` + "`satelle runtime reap`" + `, which reports them and removes them only with --yes.
Use --orphans to list only unknown and stale entries (sty_c36c211f).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRuntimeList(cmd.OutOrStdout(), orphansOnly)
		},
	}
	listCmd.Flags().BoolVar(&orphansOnly, "orphans", false, "list only unknown/stale key dirs")

	var reapYes, reapIncludeUnknown bool
	reapCmd := &cobra.Command{
		Use:   "reap",
		Short: "Report — and with --yes, remove — runtime planes and registry entries for repos that are gone",
		Long: `Clear the debris a deleted repo leaves behind: its home-keyed runtime plane
under the machine home, and its workspace-registry entry. Both, in one action,
because an operator cleaning up after a deleted repo wants both gone.

A BARE invocation reports and removes NOTHING. Removal happens only with --yes,
and only for what the report just listed.

Scope:
  stale     the plane's resolved repo root no longer exists  (default)
  unknown   no marker and no registry match — leaked test dirs (--include-unknown)
  linked    the repo root still exists — NEVER removed, under any flag

Dangling registry entries are collected independently of planes, so an entry
whose plane was already removed by hand is still cleared.

satelle never deletes IMPLICITLY. It will delete what it has just reported, when
you ask with --yes, and only where the repo path does not resolve. A path can be
absent for reasons other than deletion — an unmounted volume, a detached disk, a
checkout not yet restored — so read the report before you pass --yes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRuntimeReap(cmd.OutOrStdout(), reapYes, reapIncludeUnknown)
		},
	}
	reapCmd.Flags().BoolVar(&reapYes, "yes", false, "remove what was reported (default is report-only)")
	reapCmd.Flags().BoolVar(&reapIncludeUnknown, "include-unknown", false, "also target unknown key dirs (no marker, no registry match)")

	root.AddCommand(pathCmd, listCmd, reapCmd)
	register(root)
}

// runRuntimeReap reports stale runtime planes and dangling registry entries and,
// when yes is set, removes exactly what it reported (sty_bd8af0b6).
func runRuntimeReap(out io.Writer, yes, includeUnknown bool) error {
	home, entries, err := collectRuntimeEntries()
	if err != nil {
		return err
	}

	// Targets. A linked plane is never a target — no flag widens to it.
	var planes []runtimeListEntry
	for _, e := range entries {
		switch e.Status {
		case "stale":
			planes = append(planes, e)
		case "unknown":
			if includeUnknown {
				planes = append(planes, e)
			}
		}
	}
	regTargets := danglingRegistryEntries()

	if len(planes) == 0 && len(regTargets) == 0 {
		fmt.Fprintln(out, "nothing to reap: no stale runtime planes, no dangling registry entries")
		if !includeUnknown {
			var unknown int
			for _, e := range entries {
				if e.Status == "unknown" {
					unknown++
				}
			}
			if unknown > 0 {
				fmt.Fprintf(out, "(%d unknown key dir(s) not shown — re-run with --include-unknown)\n", unknown)
			}
		}
		return nil
	}

	fmt.Fprintf(out, "home  %s\n\n", home)
	if len(planes) > 0 {
		fmt.Fprintf(out, "runtime planes (%d):\n", len(planes))
		for _, e := range planes {
			repo := e.Repo
			if repo == "" {
				repo = "<no repo.path marker, no registry match>"
			}
			fmt.Fprintf(out, "  %-8s  %s\n            repo: %s\n", e.Status, e.Dir, repo)
		}
	}
	if len(regTargets) > 0 {
		if len(planes) > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "workspace registry entries (%d):\n", len(regTargets))
		for _, p := range regTargets {
			fmt.Fprintf(out, "  %s\n", p)
		}
	}

	if !yes {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "nothing removed — re-run with --yes to remove exactly what is listed above")
		return nil
	}

	fmt.Fprintln(out)
	var removedPlanes, removedEntries int
	for _, e := range planes {
		if rerr := os.RemoveAll(e.Dir); rerr != nil {
			fmt.Fprintf(out, "  FAILED %s: %v\n", e.Dir, rerr)
			continue
		}
		removedPlanes++
		fmt.Fprintf(out, "  removed plane %s\n", e.Dir)
	}
	if len(regTargets) > 0 {
		gc, lerr := config.LoadGlobal()
		if lerr != nil {
			return fmt.Errorf("runtime reap: load global config: %w", lerr)
		}
		for _, p := range regTargets {
			if gc.Workspace.RemoveRepo(p) {
				removedEntries++
				fmt.Fprintf(out, "  removed registry entry %s\n", p)
			}
		}
		if removedEntries > 0 {
			if serr := config.SaveGlobal(gc); serr != nil {
				return fmt.Errorf("runtime reap: save global config: %w", serr)
			}
		}
	}
	fmt.Fprintf(out, "\nreaped %d plane(s), %d registry entry(ies)\n", removedPlanes, removedEntries)
	return nil
}

// danglingRegistryEntries returns workspace-registry paths that no longer
// resolve to a directory. Computed independently of runtime planes: the observed
// case had planes already removed by hand while the registry entries survived,
// and those entries are what keep counting as UNHEALTHY.
func danglingRegistryEntries() []string {
	gc, err := config.LoadGlobal()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range gc.Workspace.Repos {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if st, serr := os.Stat(p); serr != nil || !st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

func runRuntimePath(out io.Writer, cfg config.Config, repoRoot string) error {
	res := cfg.ResolveRuntimeDir(repoRoot)
	key := config.RepoKey(repoRoot)
	fmt.Fprintf(out, "repo_root    %s\n", repoRoot)
	fmt.Fprintf(out, "repo_key     %s\n", key)
	fmt.Fprintf(out, "runtime_dir  %s\n", res.Dir)
	fmt.Fprintf(out, "database     %s\n", filepath.Join(res.Dir, config.DefaultDBName))
	fmt.Fprintf(out, "data_dir     %s\n", cfg.ResolveDataDir(repoRoot))
	if res.Legacy {
		fmt.Fprintf(out, "layout       legacy (run: satelle runtime migrate)\n")
	} else {
		fmt.Fprintf(out, "layout       home-keyed\n")
	}
	return nil
}

// runtimeListEntry is one home-keyed runtime dir for `satelle runtime list`.
type runtimeListEntry struct {
	Key     string
	Dir     string
	Status  string // linked | stale | unknown
	Repo    string
	Size    int64
	DBMtime string
	HasDB   bool
}

// collectRuntimeEntries scans the home plane and returns every runtime key dir
// with its repo resolved (marker first, workspace registry as fallback) and its
// status classified. Shared by `runtime list` and `runtime reap` so the two can
// never disagree about what an orphan is.
func collectRuntimeEntries() (string, []runtimeListEntry, error) {
	home := config.GlobalDir()
	entries, err := listRuntimeKeyDirs(home)
	if err != nil {
		return home, nil, err
	}
	reg := registryKeyMap()
	out := make([]runtimeListEntry, 0, len(entries))
	for _, e := range entries {
		if e.Repo == "" {
			if p, ok := reg[e.Key]; ok {
				e.Repo = p
			}
		}
		e.Status = classifyRuntimeEntry(e.Repo)
		out = append(out, e)
	}
	return home, out, nil
}

func runRuntimeList(out io.Writer, orphansOnly bool) error {
	home, entries, err := collectRuntimeEntries()
	if err != nil {
		return err
	}
	var shown []runtimeListEntry
	for _, e := range entries {
		if orphansOnly && e.Status == "linked" {
			continue
		}
		shown = append(shown, e)
	}
	if len(shown) == 0 {
		if orphansOnly {
			fmt.Fprintln(out, "no orphan/stale runtime key dirs")
		} else {
			fmt.Fprintln(out, "no runtime key dirs under", home)
		}
		return nil
	}
	fmt.Fprintf(out, "home  %s\n\n", home)
	fmt.Fprintf(out, "%-28s  %-8s  %10s  %-20s  %s\n", "KEY", "STATUS", "SIZE", "DB_MTIME", "REPO")
	for _, e := range shown {
		size := formatSize(e.Size)
		mtime := e.DBMtime
		if mtime == "" {
			mtime = "-"
		}
		repo := e.Repo
		if repo == "" {
			repo = "-"
		}
		fmt.Fprintf(out, "%-28s  %-8s  %10s  %-20s  %s\n", e.Key, e.Status, size, mtime, repo)
	}
	// Point at the supported removal path. `list` itself still never deletes;
	// `runtime reap` reports first and removes only when asked (sty_bd8af0b6).
	var orphans int
	for _, e := range shown {
		if e.Status == "unknown" || e.Status == "stale" {
			orphans++
		}
	}
	if orphans > 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "%d orphan/stale key dir(s). To review and remove them:\n", orphans)
		fmt.Fprintln(out, "  satelle runtime reap          # report only, removes nothing")
		fmt.Fprintln(out, "  satelle runtime reap --yes    # remove what was reported")
	}
	return nil
}

func listRuntimeKeyDirs(home string) ([]runtimeListEntry, error) {
	ents, err := os.ReadDir(home)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("runtime list: read %s: %w", home, err)
	}
	var out []runtimeListEntry
	for _, ent := range ents {
		if !ent.IsDir() || !config.IsRuntimeKeyDir(ent.Name()) {
			continue
		}
		dir := filepath.Join(home, ent.Name())
		e := runtimeListEntry{
			Key:  ent.Name(),
			Dir:  dir,
			Repo: config.ReadRepoPathMarker(dir),
			Size: dirSize(dir),
		}
		dbPath := filepath.Join(dir, config.DefaultDBName)
		if st, err := os.Stat(dbPath); err == nil && !st.IsDir() {
			e.HasDB = true
			e.DBMtime = st.ModTime().UTC().Format("2006-01-02T15:04:05Z")
		}
		out = append(out, e)
	}
	return out, nil
}

// registryKeyMap builds RepoKey(path) → path for every workspace registry entry.
func registryKeyMap() map[string]string {
	gc, err := config.LoadGlobal()
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, p := range gc.Workspace.Repos {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		m[config.RepoKey(p)] = p
	}
	return m
}

func classifyRuntimeEntry(repo string) string {
	if repo == "" {
		return "unknown"
	}
	if st, err := os.Stat(repo); err != nil || !st.IsDir() {
		return "stale"
	}
	return "linked"
}

func dirSize(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(_ string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil || fi.IsDir() {
			return nil
		}
		n += fi.Size()
		return nil
	})
	return n
}

func formatSize(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
	)
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1fM", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1fK", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func runRuntimeMigrate(out io.Writer, a *app.App, force, allowLive, dryRun bool) error {
	cfg := a.Config
	repoRoot := a.RepoRoot
	dataDir := a.DataDir
	if dataDir == "" {
		dataDir = cfg.ResolveDataDir(repoRoot)
	}
	legacyDB := filepath.Join(dataDir, config.DefaultDBName)
	if !fileExists(legacyDB) {
		// Already home-keyed, or never had a DB under data_dir.
		home := filepath.Join(config.GlobalDir(), config.RepoKey(repoRoot))
		if fileExists(filepath.Join(home, config.DefaultDBName)) {
			fmt.Fprintf(out, "already migrated: %s\n", home)
			return nil
		}
		return fmt.Errorf("runtime migrate: no legacy database at %s — nothing to migrate", legacyDB)
	}

	targetDir := filepath.Join(config.GlobalDir(), config.RepoKey(repoRoot))
	targetDB := filepath.Join(targetDir, config.DefaultDBName)
	if fileExists(targetDB) && !force {
		return fmt.Errorf("runtime migrate: target already has %s (pass --force to overwrite)", targetDB)
	}

	// Liveness check before any copy (sty_5308eb60). Independent of runMigrate's
	// check so the public entrypoint is safe on its own.
	holders, liveErr := ensureLiveOK(out, cfg, repoRoot, legacyDB, allowLive, !dryRun)
	if liveErr != nil {
		return liveErr
	}

	if dryRun {
		fmt.Fprintf(out, "dry-run: would copy\n")
		fmt.Fprintf(out, "  database: %s → %s (VACUUM INTO)\n", legacyDB, targetDB)
		for _, name := range []string{"logs", "backups", "stories"} {
			src := filepath.Join(dataDir, name)
			st, err := os.Lstat(src)
			if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
				continue
			}
			fmt.Fprintf(out, "  dir:      %s → %s\n", src, filepath.Join(targetDir, name))
		}
		fmt.Fprintf(out, "  leave legacy tree in place at %s\n", dataDir)
		if len(holders) > 0 {
			printLivePlanLine(out, holders)
			fmt.Fprintln(out, "  (would refuse apply: live runtime — wait, or pass --allow-live)")
		}
		return nil
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("runtime migrate: mkdir %s: %w", targetDir, err)
	}
	// Remove a prior target DB when --force so VACUUM INTO can create cleanly.
	if force {
		for _, p := range []string{targetDB, targetDB + "-wal", targetDB + "-shm"} {
			_ = os.Remove(p)
		}
	}

	if err := vacuumInto(legacyDB, targetDB); err != nil {
		return fmt.Errorf("runtime migrate: copy database: %w", err)
	}
	fmt.Fprintf(out, "copied database → %s\n", targetDB)

	for _, name := range []string{"logs", "backups", "stories"} {
		src := filepath.Join(dataDir, name)
		dst := filepath.Join(targetDir, name)
		st, err := os.Lstat(src)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			continue
		}
		if err := copyDir(src, dst); err != nil {
			return fmt.Errorf("runtime migrate: copy %s: %w", name, err)
		}
		fmt.Fprintf(out, "copied %s/ → %s\n", name, dst)
	}

	fmt.Fprintf(out, "\nmigration complete. Legacy tree left at %s\n", dataDir)
	fmt.Fprintf(out, "After verifying with `satelle status`, you may remove runtime leftovers:\n")
	fmt.Fprintf(out, "  rm -f %s %s-wal %s-shm\n", legacyDB, legacyDB, legacyDB)
	fmt.Fprintf(out, "  rm -rf %s %s\n",
		filepath.Join(dataDir, "backups"),
		filepath.Join(dataDir, "stories"))
	fmt.Fprintf(out, "  # only if logs/ is a real directory, not the .satelle/logs pointer:\n")
	fmt.Fprintf(out, "  rm -rf %s\n", filepath.Join(dataDir, "logs"))
	fmt.Fprintf(out, "(Do not remove authored files: satelle.toml, constitution.md, workflows/ (incl. agents.toml), skills/, principles/, documents/, tasks/.)\n")
	return nil
}

// vacuumInto copies srcDB into dstDB using SQLite VACUUM INTO (atomic, consistent
// snapshot; avoids torn wal/shm file copies of a live database).
func vacuumInto(srcDB, dstDB string) error {
	if err := os.MkdirAll(filepath.Dir(dstDB), 0o755); err != nil {
		return err
	}
	// Open source read-only so we never mutate the operator's live ledger.
	db, err := sql.Open("sqlite", store.ReadOnlyDSN(srcDB))
	if err != nil {
		return err
	}
	defer db.Close()
	// VACUUM INTO wants a path string; escape single quotes for the SQL literal.
	escaped := strings.ReplaceAll(dstDB, "'", "''")
	_, err = db.Exec("VACUUM INTO '" + escaped + "'")
	return err
}

// copyDir recursively copies a directory tree (files + dirs). Destination parents
// are created. Existing files at the destination are overwritten.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil // skip symlinks
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o644)
}
