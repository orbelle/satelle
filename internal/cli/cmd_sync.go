package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledgersync"
)

// syncStateFile records, in the runtime directory, what the bound project
// holds: each item's pushed head and each attachment body pushed.
const syncStateFile = "sync-state.json"

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Push and pull items, their ledger and attachments with the bound project",
		Long: `sync carries work state between this repo's ledger and the project it is bound
to (satelle project bind): each item's chain of events, item summaries and
attachment bodies. Configuration is not synced; it lives in git.`,
	}
	cmd.AddCommand(syncPushCmd(), syncPullCmd(), syncStatusCmd())
	return cmd
}

// openSync opens the kernel and a syncer for the bound project.
func openSync(fn func(cmd *cobra.Command, k *kernel.Kernel, s *ledgersync.Syncer) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		k, r, err := openKernel()
		if err != nil {
			return err
		}
		defer k.Close()
		b, ok, err := loadBinding(r)
		if err != nil {
			return err
		}
		if !ok {
			return errNotBound
		}
		c, err := dial(b.Server)
		if err != nil {
			return err
		}
		defer c.Close()
		s := &ledgersync.Syncer{Client: c, Project: b.Project, Ledger: k.Ledger, Cache: k.Cache,
			StatePath: filepath.Join(r.Runtime, syncStateFile)}
		return fn(cmd, k, s)
	}
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "-"
	}
	return h
}

// reportConflicts prints what diverged on each conflicting item.
func reportConflicts(w io.Writer, conflicts []ledgersync.Conflict) {
	for _, c := range conflicts {
		fmt.Fprintf(w, "conflict: %s diverged after %d shared event(s)\n", c.Item, c.Common)
		for _, e := range c.LocalOnly {
			fmt.Fprintf(w, "  local  %s %s %s %s\n", short(e.Hash), e.At.Format("2006-01-02T15:04:05Z07:00"), e.Kind, e.Actor)
		}
		for _, e := range c.RemoteOnly {
			fmt.Fprintf(w, "  remote %s %s %s %s\n", short(e.Hash), e.At.Format("2006-01-02T15:04:05Z07:00"), e.Kind, e.Actor)
		}
	}
	if len(conflicts) > 0 {
		fmt.Fprintln(w, "Conflicting items were not pushed; nothing was rewritten.")
	}
}

func conflictErr(n int) error {
	return &exitError{code: ExitError, msg: fmt.Sprintf("%d item(s) in conflict", n)}
}

func syncPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Append local events the project lacks, then item summaries and attachments",
		Args:  cobra.NoArgs,
		RunE: openSync(func(cmd *cobra.Command, _ *kernel.Kernel, s *ledgersync.Syncer) error {
			res, err := s.Push(cmd.Context())
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Pushed %d event(s) on %d item(s) and %d attachment(s).\n", res.Events, len(res.Items), res.Attachments)
			reportConflicts(out, res.Conflicts)
			if err != nil {
				return err
			}
			if len(res.Conflicts) > 0 {
				return conflictErr(len(res.Conflicts))
			}
			return nil
		}),
	}
}

func syncPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Import the project's events the local ledger lacks, and their attachments",
		Args:  cobra.NoArgs,
		RunE: openSync(func(cmd *cobra.Command, _ *kernel.Kernel, s *ledgersync.Syncer) error {
			res, err := s.Pull(cmd.Context())
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Pulled %d event(s) on %d item(s) and %d attachment(s).\n", res.Events, len(res.Items), res.Attachments)
			reportConflicts(out, res.Conflicts)
			if err != nil {
				return err
			}
			if len(res.Conflicts) > 0 {
				return conflictErr(len(res.Conflicts))
			}
			return nil
		}),
	}
}

func syncStatusCmd() *cobra.Command {
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Compare each item's local and remote head",
		Args:  cobra.NoArgs,
		RunE: openSync(func(cmd *cobra.Command, _ *kernel.Kernel, s *ledgersync.Syncer) error {
			rows, err := s.Status(cmd.Context())
			if err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), rows)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ITEM\tLOCAL\tREMOTE\tSTATE")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Item, short(r.Local), short(r.Remote), r.State)
			}
			return tw.Flush()
		}),
	}
	asJSON = jsonFlag(cmd)
	return cmd
}
