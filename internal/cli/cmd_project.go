package cli

// `satelle project create` / `list` / `bind` / `show` — the CLI counterpart to
// the hosted-server projects API (sty_2da92fd5) plus the per-repo project
// binding personal sync needs (sty_0aa3df89). create POSTs to /api/v1/projects
// (the caller becomes owner); list GETs the caller's projects; bind records
// which hosted project this repo's personal sync targets; show prints server,
// binding, and sign-in state. All reuse the login flow's resolveServer
// (--server → machine [hosted] server → DefaultHostedServer) and the per-user
// credential store. Like login/whoami these commands never touch the local DB,
// so they carry no store annotation and work in a fresh clone.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/spf13/cobra"
)

func init() {
	project := &cobra.Command{
		Use:   "project",
		Short: "Manage projects on the hosted satelle-server",
		Long: `Manage the hosted-server projects this machine can reach, and which one this
repo syncs to.

Hosted only: every subcommand needs a session (satelle login) and the network.
Local work never depends on it — the OSS tier runs entirely offline, and a repo
with no bound project simply does not sync.`,
	}

	var (
		createServer string
		slug         string
		name         string
	)
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a project on the hosted server (you become its owner)",
		Long: `create POSTs to the hosted server's /api/v1/projects endpoint using the
stored credentials, making you the new project's owner, and prints its id, slug,
and name. Requires a prior "satelle login".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectCreate(cmd, createServer, slug, name)
		},
	}
	create.Flags().StringVar(&createServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	create.Flags().StringVar(&slug, "slug", "", "Project slug (required).")
	create.Flags().StringVar(&name, "name", "", "Human-readable project name (required).")
	project.AddCommand(create)

	var listServer string
	list := &cobra.Command{
		Use:   "list",
		Short: "List your projects on the hosted server",
		Long: `List the projects your credential can see on the hosted server.

Reach for it to find the slug bind expects. It asks the server, so it needs
login and the network; use satelle project show to see what this repo is already
bound to.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectList(cmd, listServer)
		},
	}
	list.Flags().StringVar(&listServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	project.AddCommand(list)

	// bind — record which hosted project this repo's personal sync targets.
	bind := &cobra.Command{
		Use:   "bind <slug>",
		Short: "Bind this repo to a hosted project slug (the sync target, and what decides its scope)",
		Long: `bind writes the project slug to the committed .satelle/satelle.toml [sync]
project key; "satelle project show" prints it.

Your account decides the scope: a personal-team project syncs personal, a
project of your team syncs team, any other stays local — from the next sync;
"satelle sync status" names it.

A project the server reports with no substrate is seeded by its substrate
holder: bind publishes the repo's team-owned substrate as the project's first
hosted version, or the embedded defaults when the repo has none of its own. A
project that already has substrate is not reseeded; bind deploys it and writes
its team-owned files into the tree.

--pull-only marks this machine never to push (see "satelle project
pull-only"), before any seed; --no-pull-only clears it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectBind(cmd, args[0])
		},
	}
	bind.Flags().Bool("pull-only", false, "Mark this machine pull-only: it pulls from the project and never pushes to it.")
	bind.Flags().Bool("no-pull-only", false, "Clear the pull-only mark, so this machine pushes again.")
	bind.MarkFlagsMutuallyExclusive("pull-only", "no-pull-only")
	project.AddCommand(bind)

	pullOnly := &cobra.Command{
		Use:   "pull-only on|off",
		Short: "Mark this machine to pull from its bound project and never push (on), or clear it (off)",
		Long: `pull-only on marks this machine to read its bound project and never publish
to it: satelle sync push and every implicit push (bare sync, the per-area
pushes, the serve daemon's push, a bind seed, a task node's workstate push)
refuse with a message naming the setting, while every pull still works. off
clears it.

The mark is machine state, written to the gitignored
.satelle/satelle.local.toml as [sync] pull_only; a pull_only key in the
committed or hosted satelle.toml marks nothing. satelle sync status and
satelle project show print the push mode.`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectPullOnly(cmd, args[0])
		},
	}
	project.AddCommand(pullOnly)

	var showServer string
	show := &cobra.Command{
		Use:   "show",
		Short: "Show this repo's hosted server, bound project, and sign-in state",
		Long: `Show this repo's hosted binding: which server, which project slug, and whether
this machine is signed in.

The first thing to read when a sync does nothing or is refused — it separates
"no project bound" from "bound but not signed in", which the sync commands
themselves report only indirectly.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectShow(cmd, showServer)
		},
	}
	show.Flags().StringVar(&showServer, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	project.AddCommand(show)

	register(project)
}

// resolveBoundProject returns the [sync] project (or leftover [hosted]
// project, or the repo directory name). Empty only when there is no repo root
// to default from.
func resolveBoundProject(cfg config.Config, repoRoot string) (string, error) {
	slug := config.ResolveBoundProject(cfg, repoRoot)
	if slug == "" {
		return "", fmt.Errorf("no sync project — run \"satelle login\", then \"satelle project create --slug <slug> --name <name>\" (or \"satelle project bind <slug>\"), or set [sync] project in .satelle/satelle.toml")
	}
	return slug, nil
}

func runProjectBind(cmd *cobra.Command, slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return fmt.Errorf("a project slug is required")
	}
	_, cfgPath, err := config.Load("")
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("load config: %w", err)
	}
	// Empty-tree rehydrate: create .satelle/satelle.toml when absent so bind
	// works before init/config deploy (epic:workspace-rehydrate order:4).
	if cfgPath == "" {
		cwd, cerr := os.Getwd()
		if cerr != nil {
			return fmt.Errorf("resolve cwd: %w", cerr)
		}
		cfgPath = filepath.Join(cwd, config.DefaultDataDir, config.ConfigName)
	}
	edit := config.BoundProjectEdit(slug)
	if err := config.SaveConfigValues(cfgPath, []config.KeyEdit{edit}); err != nil {
		return fmt.Errorf("record hosted project in satelle.toml: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Bound this repo to project %q (recorded in %s).\n", slug, cfgPath)
	if on, set := bindPullOnlyFlag(cmd); set {
		if err := setPullOnly(cmd.OutOrStdout(), config.RepoRootFromConfigPath(cfgPath), on); err != nil {
			return err
		}
	}
	return nil
}

// bindPullOnlyFlag is the pull-only mark bind was asked to write, and whether it
// was asked at all.
func bindPullOnlyFlag(cmd *cobra.Command) (on, set bool) {
	for _, f := range []struct {
		name string
		on   bool
	}{{"pull-only", true}, {"no-pull-only", false}} {
		if fl := cmd.Flags().Lookup(f.name); fl != nil && fl.Changed && fl.Value.String() == "true" {
			return f.on, true
		}
	}
	return false, false
}

func runProjectPullOnly(cmd *cobra.Command, mode string) error {
	var on bool
	switch strings.TrimSpace(mode) {
	case "on":
		on = true
	case "off":
	default:
		return fmt.Errorf("pull-only takes on or off, got %q", mode)
	}
	_, repoRoot, _, err := loadRepoConfig()
	if err != nil {
		return err
	}
	return setPullOnly(cmd.OutOrStdout(), repoRoot, on)
}

func runProjectShow(cmd *cobra.Command, serverArg string) error {
	cfg, cfgPath, err := config.Load("")
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("load config: %w", err)
	}
	server := strings.TrimRight(strings.TrimSpace(serverArg), "/")
	if server == "" {
		server = config.ResolveHostedServer(cfg)
	}
	out := cmd.OutOrStdout()
	if server == "" {
		fmt.Fprintln(out, "hosted server: (none configured)")
	} else {
		fmt.Fprintf(out, "hosted server: %s\n", server)
	}
	root := ""
	if cfgPath != "" {
		root = config.RepoRootFromConfigPath(cfgPath)
	}
	if slug := config.ResolveBoundProject(cfg, root); slug == "" {
		fmt.Fprintln(out, "bound project: (none — run \"satelle project bind <slug>\")")
	} else {
		fmt.Fprintf(out, "bound project: %s\n", slug)
	}
	if root != "" {
		fmt.Fprintln(out, pushModeLine(root))
	}
	signed := "signed out"
	switch u := resolveUserFor(server, root); {
	case u.Unresolved():
		signed = sessionUnresolved
	case u.IsSession():
		signed = "session token (user " + u.PrincipalID + ")"
	case u.SignedIn():
		signed = "signed in"
	}
	fmt.Fprintf(out, "sign-in state: %s\n", signed)
	return nil
}

func runProjectCreate(cmd *cobra.Command, serverArg, slug, name string) error {
	// Validate client-side BEFORE any network construction (AC2).
	slug, name = strings.TrimSpace(slug), strings.TrimSpace(name)
	if slug == "" {
		return fmt.Errorf("--slug is required")
	}
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	server := resolveServer(serverArg)
	if server == "" {
		return fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}

	p, err := hosted.NewClient(server, hosted.DefaultStore(), nil).CreateProject(cmd.Context(), slug, name)
	if err != nil {
		switch {
		case errors.Is(err, hosted.ErrLoginRequired):
			return err
		case errors.Is(err, hosted.ErrSlugConflict):
			return fmt.Errorf("slug %q already exists on %s", slug, server)
		default:
			return fmt.Errorf("create project: %w", err)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Created project on %s:\n  id:   %s\n  slug: %s\n  name: %s\n", server, p.ID, p.Slug, p.Name)
	return nil
}

func runProjectList(cmd *cobra.Command, serverArg string) error {
	server := resolveServer(serverArg)
	if server == "" {
		return fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}

	projects, err := hosted.NewClient(server, hosted.DefaultStore(), nil).ListProjects(cmd.Context())
	if err != nil {
		if errors.Is(err, hosted.ErrLoginRequired) {
			return err
		}
		return fmt.Errorf("list projects: %w", err)
	}
	out := cmd.OutOrStdout()
	if len(projects) == 0 {
		fmt.Fprintf(out, "No projects on %s.\n", server)
		return nil
	}
	fmt.Fprintf(out, "%-24s  %-20s  %-24s  %s\n", "ID", "SLUG", "NAME", "ROLE")
	for _, p := range projects {
		role := p.Role
		if role == "" {
			role = "-"
		}
		fmt.Fprintf(out, "%-24s  %-20s  %-24s  %s\n", p.ID, p.Slug, p.Name, role)
	}
	return nil
}
