package cli

import (
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func init() {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show the local repo's config, database, and store counts",
		Long: `Show what satelle resolved for THIS repo: the config it loaded, where the
database and runtime plane live, and how many stories, tasks and docs it holds.

The orientation command in an unfamiliar checkout, and the first check when
something writes to a place you did not expect. For "can satelle govern here at
all", use satelle doctor, which judges rather than reports.

It also names the hosted server and the hosted project this checkout syncs to,
and whether satelle acts through a session token (SATELLE_TOKEN) or a stored
login: no network call, no sign-in needed, and never a credential.`,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			stories, err := a.Store.Stories.Count(ctx, workitem.KindStory)
			if err != nil {
				return err
			}
			tasks, err := a.Store.Stories.Count(ctx, workitem.KindTask)
			if err != nil {
				return err
			}
			events, err := a.Store.Ledger.Count(ctx)
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "repo root\t%s\n", a.RepoRoot)
			fmt.Fprintf(w, "data dir\t%s\n", a.DataDir)
			fmt.Fprintf(w, "runtime dir\t%s\n", a.RuntimeDir)
			fmt.Fprintf(w, "database\t%s\n", a.DBPath)
			if note := a.Config.LegacyRuntimeNote(a.RepoRoot); note != "" {
				fmt.Fprintf(w, "runtime layout\tlegacy — run satelle runtime migrate\n")
			} else {
				fmt.Fprintf(w, "runtime layout\thome-keyed\n")
				if line := runtimeResidueLine(a); line != "" {
					fmt.Fprint(w, line)
				}
			}
			// Real availability, not a config echo (sty_fb5e6d96): the URL a
			// user opens plus whether anything is answering on it.
			fmt.Fprintf(w, "web service\t%s\n", probeWebAvailability().statusValue())
			// Hosted binding (sty_1c4ad7e2): config resolvers only, no network, so
			// status renders offline. The identity row (sty_e82d5f49) adds one local
			// credential-file read through resolveUser — still no network, and no
			// signed-in state needed.
			fmt.Fprintf(w, "hosted server\t%s\n", config.ResolveHostedServer(a.Config))
			fmt.Fprintf(w, "hosted project\t%s\n", hostedProjectValue(a.Config, a.RepoRoot))
			fmt.Fprintf(w, "identity\t%s\n", identitySourceValue(resolveUser(a.Config, a.RepoRoot)))
			for _, s := range config.MachineScopeStrays(a.DataDir) {
				fmt.Fprintf(w, "config stray\t%s\n", s.Warning())
			}
			fmt.Fprintf(w, "log level\t%s\n", a.Config.ResolveLogLevel())
			fmt.Fprintf(w, "stories\t%d\n", stories)
			fmt.Fprintf(w, "tasks\t%d\n", tasks)
			fmt.Fprintf(w, "ledger entries\t%d\n", events)

			dirs := a.AuthoredDirs()
			for _, kind := range config.AuthoredKinds {
				n, err := a.Store.DocIndex.Count(ctx, kind)
				if err != nil {
					return err
				}
				fmt.Fprintf(w, "indexed %s\t%d  (%s)\n", kind, n, dirs.Dirs[kind])
			}
			return w.Flush()
		},
	}
	register(c)
}

// hostedProjectValue is the status row for the hosted project binding. An
// explicit binding shows the slug; otherwise the row says the checkout is not
// bound and names the directory-derived slug sync would fall back to, so it is
// neither misreported as bound nor hidden.
func hostedProjectValue(cfg config.Config, repoRoot string) string {
	slug := config.ResolveBoundProject(cfg, repoRoot)
	if cfg.SyncProject() != "" {
		return slug
	}
	if slug == "" {
		return "not bound — run satelle project bind <slug>"
	}
	return fmt.Sprintf("not bound — sync would use %q; run satelle project bind <slug>", slug)
}

// identitySourceValue is the status row for the identity source: fixed text per
// source, never the token, the account, or any value derived from them. Who the
// user is stays with satelle whoami.
func identitySourceValue(u userIdentity) string {
	switch u.Source {
	case userSourceSession:
		return "session token (" + hosted.SessionTokenEnv + ")"
	case userSourceAccount:
		return "stored login (satelle login)"
	}
	return "none — local-only (git user); run satelle login"
}

// runtimeResidueLine reports in-repo .satelle/stories when the runtime plane is
// already home-keyed (sty_58fa970e AC4). Empty when legacy layout or no residue.
func runtimeResidueLine(a *app.App) string {
	if a == nil || a.Config.LegacyRuntimeNote(a.RepoRoot) != "" {
		return ""
	}
	residue := filepath.Join(a.DataDir, "stories")
	if !dirExists(residue) {
		return ""
	}
	return "runtime residue\t" + residue + " — pre-relocation attachment dir; rm -rf " + residue + " after verifying home-keyed copy\n"
}
