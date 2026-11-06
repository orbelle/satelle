package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// workspaceCmd is satelle.dev's workspaces: collections of projects, personal
// or team. This machine's projects are under satelle project.
func workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Your satelle.dev workspaces: collections of projects",
		Long: `A workspace is a collection of projects on satelle.dev, personal or team.
This machine's projects (repos or directories) are managed with satelle
project add|remove|list. See satelle help projects.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown command %q for \"satelle workspace\": a workspace is a collection of projects on satelle.dev; add this machine's projects with `satelle project add`", args[0])
		},
	}
	var server string
	var asJSON *bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List your satelle.dev workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv, err := projectServer(server)
			if err != nil {
				return err
			}
			c, err := dial(srv)
			if err != nil {
				return err
			}
			defer c.Close()
			resp, err := c.Workspaces.ListWorkspaces(cmd.Context(), &satellepb.ListWorkspacesRequest{})
			if err != nil {
				return hosted.Describe("workspace list", err)
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), resp.GetWorkspaces())
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKIND\tNAME")
			for _, w := range resp.GetWorkspaces() {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", w.GetId(), w.GetKind(), w.GetName())
			}
			return tw.Flush()
		},
	}
	list.Flags().StringVar(&server, "server", "", "satelle.dev server URL")
	asJSON = jsonFlag(list)
	cmd.AddCommand(list)
	return cmd
}
