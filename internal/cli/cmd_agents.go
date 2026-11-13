package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/substrate"
)

func agentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Install, remove or refresh harness hook files (" + strings.Join(hook.Harnesses, ", ") + ")",
		Long: `agents install writes a harness's hook file so its sessions call satelle's
hook handlers: claude (.claude/settings.json), grok (.grok/hooks/satelle.json),
pi (.pi/extensions/satelle.ts) and cursor (.cursor/hooks.json). satelle's
entries are the hook commands whose first word is satelle and whose second is
hook: they are replaced by the current set. A command whose first word is
satelle naming a command this binary lacks (the pre-v1 satelle reindex) is
removed. Every other entry is kept, a satelle command this binary has
included; a command that wraps satelle (sh -c, env, timeout, NAME=value, an
if-block) is not satelle's entry and is left as it is. A file whose hooks
would not change is not written. On a file git tracks, install writes the
working tree only; commit the change in that repo.
agents remove strips satelle's entries again.
agents refresh applies the install rule to every registered project
(~/.satelle/config.toml [projects]), to each harness in use there: one its
.satelle/agents.toml runs (claude, cursor-agent, grok or pi) or whose
configuration directory (.claude, .cursor, .grok, .pi) the repo has. It leaves
a file git tracks, a pi extension that is not satelle's, and every file of a
repo whose .satelle/satelle.toml sets [hook] refresh = false, and names each.
satelle update runs it after replacing the binaries.`,
	}
	for _, verb := range []string{"install", "remove"} {
		cmd.AddCommand(&cobra.Command{
			Use:       verb + " <harness>...",
			Short:     verb + " harness hook files in this repo",
			Args:      cobra.MinimumNArgs(1),
			ValidArgs: hook.Harnesses,
			RunE: func(cmd *cobra.Command, args []string) error {
				r, err := findRepo("")
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				if verb == "remove" {
					for _, h := range args {
						path, err := hook.Remove(r.Tree, h)
						if err != nil {
							return err
						}
						fmt.Fprintf(out, "removed %s hooks: %s\n", h, path)
					}
					return nil
				}
				return installHooks(out, r.Tree, args, staleHook(substrateTree(NewRootCmd())))
			},
		})
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "refresh",
		Short: "Refresh the hook files of every registered project's harnesses in use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if failed := refreshAllHooks(cmd.OutOrStdout()); failed > 0 {
				return &exitError{code: ExitError, msg: fmt.Sprintf("agents refresh: %d harness file(s) or project(s) not refreshed", failed)}
			}
			return nil
		},
	})
	return cmd
}

// installHooks installs each harness's hooks under tree by hand (agents
// install, init --hooks), naming a file it wrote that git tracks: the change
// is left uncommitted for that repo's own workflow.
func installHooks(out io.Writer, tree string, harnesses []string, stale func(string) bool) error {
	for _, h := range harnesses {
		path, changed, err := hook.Install(tree, h, stale)
		if err != nil {
			return err
		}
		if !changed {
			fmt.Fprintf(out, "%s hooks are current: %s\n", h, path)
			continue
		}
		fmt.Fprintf(out, "installed %s hooks: %s\n", h, path)
		rel, _ := hook.File(h)
		if tracked, err := (gitfacts.Repo{Dir: tree}).Tracks(rel); err == nil && tracked {
			fmt.Fprintf(out, "%s: git tracks this file; the change is uncommitted: commit it in this repo\n", path)
		}
	}
	return nil
}

// harnessesInUse lists, in hook.Harnesses order, the harnesses in use in the
// working tree at tree: one a binding of cat runs (its command's CLI, looked up
// in the hook package's harness registry), or one whose configuration
// directory the tree has.
func harnessesInUse(tree string, cat agent.Catalog) []string {
	run := map[string]bool{}
	for _, b := range cat {
		if h, ok := hook.ForCLI(agent.Preset(b)); ok {
			run[h] = true
		}
	}
	var out []string
	for _, h := range hook.Harnesses {
		if st, err := os.Stat(filepath.Join(tree, hook.ConfigDir(h))); run[h] || (err == nil && st.IsDir()) {
			out = append(out, h)
		}
	}
	return out
}

// repoCatalog is r's agents.toml, empty when it is absent or does not load
// (satelle validate reports that), so the configuration-directory half of the
// in-use rule still applies.
func repoCatalog(r repo) agent.Catalog {
	cat, err := agent.LoadCatalog(filepath.Join(r.DataDir, kernel.AgentsFile))
	if err != nil {
		return agent.Catalog{}
	}
	return cat
}

// staleCall reports whether hook command cmd is a satelle call (its first word
// is satelle) naming a command tree lacks, and why.
func staleCall(tree substrate.Tree, cmd string) (why string, stale bool) {
	words, ok := hook.SatelleCall(cmd)
	if !ok {
		return "", false
	}
	_, why, stale = tree.Lacks(words)
	return why, stale
}

// staleHook is staleCall as the predicate hook.Install, Refresh and Pending
// take.
func staleHook(tree substrate.Tree) func(string) bool {
	return func(cmd string) bool { _, stale := staleCall(tree, cmd); return stale }
}

// harnessFile is one harness's hook file in a project.
type harnessFile struct{ Harness, Path string }

// refreshFailure is a file (or the project's settings) a refresh could not
// finish, and why.
type refreshFailure struct{ Path, Err string }

// projectRefresh is what one project's refresh did: the files it wrote, those
// it left because git tracks them or satelle does not own them, whether the
// repo opted out, and what failed.
type projectRefresh struct {
	OptedOut bool
	Changed  []harnessFile
	Tracked  []harnessFile
	NotOwned []harnessFile
	Failures []refreshFailure
}

// refreshProject applies the install rule (stale) to each harness in use in r
// whose file would change, unless r opts out with [hook] refresh = false. It
// never writes a file git tracks, nor one when git cannot say whether it
// does, nor a pi extension that is not satelle's.
func refreshProject(r repo, stale func(string) bool) projectRefresh {
	var pr projectRefresh
	st, err := kernel.LoadSettings(r.DataDir)
	if err != nil {
		pr.Failures = append(pr.Failures, refreshFailure{filepath.Join(r.DataDir, kernel.SettingsFile), "cannot read .satelle/" + kernel.SettingsFile + ": " + err.Error()})
		return pr
	}
	if !st.Hook.Refreshes() {
		pr.OptedOut = true
		return pr
	}
	for _, h := range harnessesInUse(r.Tree, repoCatalog(r)) {
		path, changed, err := hook.Pending(r.Tree, h, stale)
		switch {
		case errors.Is(err, hook.ErrNotOwned):
			pr.NotOwned = append(pr.NotOwned, harnessFile{h, path})
			continue
		case err != nil:
			pr.Failures = append(pr.Failures, refreshFailure{path, err.Error()})
			continue
		case !changed:
			continue
		}
		rel, _ := hook.File(h)
		tracked, err := gitfacts.Repo{Dir: r.Tree}.Tracks(rel)
		if err != nil {
			pr.Failures = append(pr.Failures, refreshFailure{path, fmt.Sprintf("not refreshed: cannot tell whether git tracks it (%v)", err)})
			continue
		}
		if tracked {
			pr.Tracked = append(pr.Tracked, harnessFile{h, path})
			continue
		}
		if _, changed, err := hook.Refresh(r.Tree, h, stale); err != nil {
			pr.Failures = append(pr.Failures, refreshFailure{path, err.Error()})
		} else if changed {
			pr.Changed = append(pr.Changed, harnessFile{h, path})
		}
	}
	return pr
}

// registeredRepos lists the satelle repos of this machine's registered
// projects (~/.satelle/config.toml [projects]), skipping a path that is gone
// or holds no satelle repo, as the sync pass does.
func registeredRepos() ([]repo, error) {
	gc, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	var out []repo
	for _, path := range gc.Projects.Paths {
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			continue
		}
		if r, err := findRepo(path); err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// refreshAllHooks refreshes every registered project's harness files in use
// with this binary's command tree, printing what it wrote, left and could not
// do. It returns the number of failures.
func refreshAllHooks(out io.Writer) (failed int) {
	repos, err := registeredRepos()
	if err != nil {
		fmt.Fprintf(out, "harness files: cannot read the registered projects: %v\n", err)
		return 1
	}
	stale := staleHook(substrateTree(NewRootCmd()))
	n := 0
	for _, r := range repos {
		// A pre-1.0 repo cannot be read by this satelle: it is listed as
		// needing upgrade (sto_0f76dd4e), and its harness files are left for
		// satelle upgrade --archive, so it is neither refreshed nor a failure.
		if len(kernel.LegacyMarkers(r.DataDir)) > 0 {
			fmt.Fprintf(out, "left %s: pre-1.0 .satelle; run '%s' there\n", r.Tree, kernel.UpgradeCommand)
			continue
		}
		pr := refreshProject(r, stale)
		if pr.OptedOut {
			fmt.Fprintf(out, "left %s: [hook] refresh = false in .satelle/%s\n", r.Tree, kernel.SettingsFile)
		}
		for _, f := range pr.Changed {
			fmt.Fprintf(out, "refreshed %s hooks: %s\n", f.Harness, f.Path)
		}
		for _, f := range pr.Tracked {
			fmt.Fprintf(out, "left %s: git tracks it; run '%s' there and commit the change in that repo\n", f.Path, wireFix(f.Harness))
		}
		for _, f := range pr.NotOwned {
			fmt.Fprintf(out, "left %s: it is not satelle's file; '%s' there replaces it\n", f.Path, wireFix(f.Harness))
		}
		for _, f := range pr.Failures {
			fmt.Fprintf(out, "%s: %s\n", f.Path, f.Err)
		}
		n += len(pr.Changed)
		failed += len(pr.Failures)
	}
	fmt.Fprintf(out, "harness files: %d refreshed across %d project(s)\n", n, len(repos))
	return failed
}
