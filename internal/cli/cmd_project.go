package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// bindingFile holds, in the repo's runtime directory (machine-local, never in
// git), the server and project this repo syncs with.
const bindingFile = "project.toml"

// binding is a repo's project on a server.
type binding struct {
	Server  string `toml:"server"`
	Project string `toml:"project"` // the project id
	Slug    string `toml:"slug"`
}

func loadBinding(r repo) (binding, bool, error) {
	var b binding
	_, err := toml.DecodeFile(filepath.Join(r.Runtime, bindingFile), &b)
	if errors.Is(err, os.ErrNotExist) {
		return b, false, nil
	}
	if err != nil {
		return b, false, err
	}
	return b, b.Project != "", nil
}

func saveBinding(r repo, b binding) error {
	if err := os.MkdirAll(r.Runtime, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(r.Runtime, bindingFile))
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// projectServer resolves the server for a project command: --server, else the
// repo's binding, else the machine's.
func projectServer(flag string) (string, error) {
	if strings.TrimSpace(flag) == "" {
		if r, err := findRepo(""); err == nil {
			if b, ok, _ := loadBinding(r); ok && b.Server != "" {
				return b.Server, nil
			}
		}
	}
	return resolveServer(flag)
}

func projectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Bind this repo to a satelle-server project; create, list and show projects",
	}
	var server string
	cmd.PersistentFlags().StringVar(&server, "server", "", "server URL")
	withClient := func(fn func(cmd *cobra.Command, args []string, srv string, c *hosted.Client) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			srv, err := projectServer(server)
			if err != nil {
				return err
			}
			c, err := dial(srv)
			if err != nil {
				return err
			}
			defer c.Close()
			return fn(cmd, args, srv, c)
		}
	}

	bind := func(cmd *cobra.Command, srv string, p *satellepb.Project) error {
		r, err := findRepo("")
		if err != nil {
			return err
		}
		if err := saveBinding(r, binding{Server: hosted.Normalize(srv), Project: p.GetId(), Slug: p.GetSlug()}); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Bound this repo to %s (%s) on %s.\n", p.GetSlug(), p.GetId(), srv)
		return nil
	}

	bindCmd := &cobra.Command{
		Use:   "bind <project>",
		Short: "Bind this repo to a project (id or slug); recorded on this machine only",
		Args:  cobra.ExactArgs(1),
		RunE: withClient(func(cmd *cobra.Command, args []string, srv string, c *hosted.Client) error {
			p, err := c.Projects.GetProject(cmd.Context(), &satellepb.GetProjectRequest{Project: args[0]})
			if err != nil {
				return hosted.Describe("project bind", err)
			}
			return bind(cmd, srv, p)
		}),
	}

	var name, workspace string
	var doBind bool
	createCmd := &cobra.Command{
		Use:   "create <slug>",
		Short: "Create a project",
		Args:  cobra.ExactArgs(1),
		RunE: withClient(func(cmd *cobra.Command, args []string, srv string, c *hosted.Client) error {
			p, err := c.Projects.CreateProject(cmd.Context(), &satellepb.CreateProjectRequest{Slug: args[0], Name: name, WorkspaceId: workspace})
			if err != nil {
				return hosted.Describe("project create", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s (%s).\n", p.GetSlug(), p.GetId())
			if doBind {
				return bind(cmd, srv, p)
			}
			return nil
		}),
	}
	createCmd.Flags().StringVar(&name, "name", "", "display name (default the slug)")
	createCmd.Flags().StringVar(&workspace, "workspace", "", "workspace id (default the server's choice)")
	createCmd.Flags().BoolVar(&doBind, "bind", false, "bind this repo to the new project")

	var listJSON *bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List the projects you belong to",
		Args:  cobra.NoArgs,
		RunE: withClient(func(cmd *cobra.Command, _ []string, _ string, c *hosted.Client) error {
			resp, err := c.Projects.ListProjects(cmd.Context(), &satellepb.ListProjectsRequest{})
			if err != nil {
				return hosted.Describe("project list", err)
			}
			if *listJSON {
				return writeJSON(cmd.OutOrStdout(), resp.GetProjects())
			}
			bound := ""
			if r, err := findRepo(""); err == nil {
				if b, ok, _ := loadBinding(r); ok {
					bound = b.Project
				}
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "\tSLUG\tID\tNAME\tROLE")
			for _, p := range resp.GetProjects() {
				mark := ""
				if p.GetId() == bound {
					mark = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", mark, p.GetSlug(), p.GetId(), p.GetName(), p.GetRole())
			}
			return tw.Flush()
		}),
	}
	listJSON = jsonFlag(listCmd)

	showCmd := &cobra.Command{
		Use:   "show [project]",
		Short: "Show a project (default the one this repo is bound to)",
		Args:  cobra.MaximumNArgs(1),
		RunE: withClient(func(cmd *cobra.Command, args []string, srv string, c *hosted.Client) error {
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			} else {
				r, err := findRepo("")
				if err != nil {
					return err
				}
				b, ok, err := loadBinding(r)
				if err != nil {
					return err
				}
				if !ok {
					return errNotBound
				}
				ref = b.Project
			}
			p, err := c.Projects.GetProject(cmd.Context(), &satellepb.GetProjectRequest{Project: ref})
			if err != nil {
				return hosted.Describe("project show", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slug:   %s\nid:     %s\nname:   %s\nrole:   %s\nserver: %s\n", p.GetSlug(), p.GetId(), p.GetName(), p.GetRole(), srv)
			return nil
		}),
	}
	cmd.AddCommand(bindCmd, createCmd, listCmd, showCmd)
	return cmd
}

var errNotBound = errors.New("this repo is not bound to a project; run `satelle project bind <project>`")
