package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledgersync"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// statusPoll is how often `remote status --wait` asks again.
const statusPoll = 5 * time.Second

func remoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Fire a remote for an item and follow its run",
		Long: `A remote is a definition satelle-server starts nodes from (.satelle/remotes/<id>/
remote.toml). A node drives the item it is fired for, or runs a task, and
pushes the item's ledger and its verdict back. 'remote fire' pushes the
item's chain first; 'remote status' pulls it back once the run concluded.`,
	}
	cmd.AddCommand(remoteFireCmd(), remoteStatusCmd())
	return cmd
}

// openBound opens the kernel and a client for the bound project.
func openBound(fn func(cmd *cobra.Command, k *kernel.Kernel, s *ledgersync.Syncer, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
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
		return fn(cmd, k, newSyncer(k, c, b.Project, r.Runtime), args)
	}
}

func remoteFireCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fire <remote-id> <item>",
		Short: "Push the item's chain and fire a run of the remote for it",
		Args:  cobra.ExactArgs(2),
		RunE: openBound(func(cmd *cobra.Command, k *kernel.Kernel, s *ledgersync.Syncer, args []string) error {
			if _, err := k.Get(args[1]); err != nil {
				return err
			}
			res, err := s.Push(cmd.Context())
			if err != nil {
				return err
			}
			for _, c := range res.Conflicts {
				if c.Item == args[1] {
					reportConflicts(cmd.ErrOrStderr(), res.Conflicts)
					return fmt.Errorf("%s diverged from the project; pull and resolve it before firing", args[1])
				}
			}
			r, err := s.Client.Remotes.FireRemote(cmd.Context(), &satellepb.FireRemoteRequest{RemoteId: args[0], Project: s.Project, ItemId: args[1]})
			if err != nil {
				return hosted.Describe("RemoteService.FireRemote", err)
			}
			out := cmd.OutOrStdout()
			switch {
			case r.GetPending():
				fmt.Fprintf(out, "queued: %s for %s at position %d\n", args[0], args[1], r.GetPosition())
			default:
				fmt.Fprintf(out, "fired %s for %s: run %s (%s)\n", args[0], args[1], r.GetRunId(), r.GetOutcome())
			}
			if d := r.GetDetail(); d != "" {
				fmt.Fprintln(out, d)
			}
			return nil
		}),
	}
}

func remoteStatusCmd() *cobra.Command {
	var wait time.Duration
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "status <remote-id> <item>",
		Short: "Show the latest run of the remote for the item; pull its chain once it concluded",
		Args:  cobra.ExactArgs(2),
		RunE: openBound(func(cmd *cobra.Command, k *kernel.Kernel, s *ledgersync.Syncer, args []string) error {
			ctx := cmd.Context()
			if wait > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, wait)
				defer cancel()
			}
			var run *satellepb.ItemRun
			for {
				var err error
				run, err = s.Client.Remotes.GetItemRun(ctx, &satellepb.GetItemRunRequest{RemoteId: args[0], ItemId: args[1]})
				if err != nil {
					return hosted.Describe("RemoteService.GetItemRun", err)
				}
				if wait <= 0 || finished(run.GetState()) {
					break
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("the run is still %s after %s", run.GetState(), wait)
				case <-time.After(statusPoll):
				}
			}
			pulled := 0
			if finished(run.GetState()) {
				res, err := s.PullItem(cmd.Context(), args[1])
				if err != nil {
					return err
				}
				pulled = res.Events
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"state": run.GetState(), "node": run.GetNodeId(),
					"verdict": run.GetVerdict(), "notes": run.GetNotes(), "reason": run.GetReason(), "pulled": pulled})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s on %s: %s", args[0], args[1], run.GetState())
			if n := run.GetNodeId(); n != "" {
				fmt.Fprintf(out, " (node %s)", n)
			}
			fmt.Fprintln(out)
			if v := run.GetVerdict(); v != "" {
				fmt.Fprintf(out, "verdict: %s\n", v)
			}
			for _, l := range []string{run.GetNotes(), run.GetReason()} {
				if l != "" {
					fmt.Fprintln(out, strings.TrimRight(l, "\n"))
				}
			}
			if pulled > 0 {
				fmt.Fprintf(out, "pulled %d event(s)\n", pulled)
			}
			return nil
		}),
	}
	cmd.Flags().DurationVar(&wait, "wait", 0, "poll until the run concluded or stopped, for at most this long")
	asJSON = jsonFlag(cmd)
	return cmd
}

func finished(state string) bool { return state == "concluded" || state == "stopped" }

func nodeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "node", Short: "Run as a remote node", Hidden: true}
	cmd.AddCommand(nodeJoinCmd())
	return cmd
}

func nodeJoinCmd() *cobra.Command {
	var server, node, dir string
	var noSetup bool
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join satelle-server as the node it started, run its work and report back",
		Long: `join is what a started node runs. The node id and server come from flags or
` + provider.EnvNodeID + ` and ` + provider.EnvServer + `; the node token only from ` + provider.EnvNodeToken + `.
It exits 0 when the run concluded accepting, 2 when it concluded rejecting.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				dir = filepath.Join(home, strings.TrimPrefix(remotenode.NodeRoot, "~/"))
			}
			out, err := remotenode.Join(cmd.Context(), remotenode.JoinOptions{
				Server: server, NodeID: node, Token: os.Getenv(provider.EnvNodeToken), Dir: dir,
				NoSetup: noSetup, Log: cmd.ErrOrStderr(), Version: buildinfo.Version,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "concluded %s: %s\n", out.Verdict, out.Notes)
			if out.Verdict != kernel.VerdictAccept {
				return &exitError{code: ExitBlocked, msg: "the run concluded " + out.Verdict, quiet: true}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", os.Getenv(provider.EnvServer), "satelle-server (default $"+provider.EnvServer+")")
	cmd.Flags().StringVar(&node, "node", os.Getenv(provider.EnvNodeID), "the node id (default $"+provider.EnvNodeID+")")
	cmd.Flags().StringVar(&dir, "dir", "", "the node's working root (default "+remotenode.NodeRoot+")")
	cmd.Flags().BoolVar(&noSetup, "no-setup", false, "skip [setup]: the provider already ran it")
	return cmd
}

func collectCmd(kind string) *cobra.Command {
	var branch, base string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "collect <id> --branch <b>",
		Short: "Record the branch a step's work came back on; its gates judge that branch",
		Long: `collect reads the branch (a local branch, or origin/<b> once fetched) and records
on the ledger its commit and its merge base with the base branch: --base <ref>,
else origin/main, else the item's recorded base. HEAD is never used, so the base
is the same whichever tree collect runs in. From then on the item's gates judge
the diff from that base to that commit. A branch that adds nothing over that
base is refused. collect never changes the repository: merging the branch, if
the workflow needs it, is a step's work.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if _, err := getItem(k, kind, args[0]); err != nil {
				return err
			}
			c, err := k.Collect(args[0], branch, base, actor())
			if err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), c)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "collected %s at %s (base %s from %s) on %s\n", c.Branch, short(c.Commit), short(c.Base), c.BaseRef, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "the branch the work came back on")
	cmd.Flags().StringVar(&base, "base", "", "the ref the branch's diff base is taken from: its merge base with that ref, resolved now (default origin/main, else the item's recorded base)")
	_ = cmd.MarkFlagRequired("branch")
	asJSON = jsonFlag(cmd)
	return cmd
}

// withRemote gives the kernel the remote placement of the bound project, if
// any; the returned close releases the connection.
func withRemote(k *kernel.Kernel, r repo) (func(), error) {
	b, ok, err := loadBinding(r)
	if err != nil || !ok {
		return func() {}, err
	}
	c, err := dial(b.Server)
	if err != nil {
		return func() {}, err
	}
	k.Remote = remotenode.Placer{Client: c, Project: b.Project, Sync: newSyncer(k, c, b.Project, r.Runtime)}
	return func() { c.Close() }, nil
}
