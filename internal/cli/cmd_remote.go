package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// statusPoll is how often `remote status --wait` asks again.
const statusPoll = 5 * time.Second

func remoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Fire a remote for an item and follow its run",
		Long: `A remote is a definition nodes are started from (.satelle/remotes/<id>/
remote.toml). A node drives the item it is fired for, or runs a task, and
brings the item's ledger and its verdict back.

A docker-local remote runs locally: 'remote fire' builds the node image from
the repo's Dockerfile, starts the node's container, seats the item at
remote:<node>, runs the node on the item and imports its chain, verified. It
needs docker, and no sign-in or bound project. Any other remote is started by
satelle-server for the bound project: 'remote fire' pushes the item's chain
first; 'remote status' pulls it back once the run concluded. See
'satelle help remotes'.`,
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
		return withBound(k, r, func(s *ledgersync.Syncer) error { return fn(cmd, k, s, args) })
	}
}

// withBound runs fn with a syncer for the bound project of the open kernel.
func withBound(k *kernel.Kernel, r repo, fn func(s *ledgersync.Syncer) error) error {
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
	return fn(newSyncer(k, c, b.Project, r.Runtime))
}

// localRemote reads the remote's definition from this repo when it is a
// docker-local one, which runs here; ok is false for any other remote, and
// for one not defined here: satelle-server runs those.
func localRemote(r repo, id string) (def remotedef.Definition, bundle []byte, ok bool, err error) {
	def, bundle, err = remotenode.LoadLocal(r.DataDir, id)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return def, nil, false, nil
	case err != nil:
		return def, nil, false, err
	}
	return def, bundle, def.Provider == remotedef.ProviderDockerLocal, nil
}

// localRuns is where the runs of the remotes fired here are recorded.
func localRuns(r repo) remotenode.LocalRuns {
	return remotenode.LocalRuns{Dir: filepath.Join(r.Runtime, remotenode.RemotesDir)}
}

func remoteFireCmd() *cobra.Command {
	var steps int
	cmd := &cobra.Command{
		Use:   "fire <remote-id> <item>",
		Short: "Fire a run of the remote for the item (a docker-local remote runs here)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if steps < 0 {
				return fmt.Errorf("--steps %d: a step count is 0 or more", steps)
			}
			k, r, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			def, bundle, local, err := localRemote(r, args[0])
			if err != nil {
				return err
			}
			if local {
				run, err := remotenode.FireLocal(cmd.Context(), remotenode.FireOptions{
					Kernel: k, Tree: r.Tree, DataDir: r.DataDir, Runs: localRuns(r),
					Remote: args[0], Definition: def, Bundle: bundle, Item: args[1], Steps: steps, Actor: actor(),
					Out: cmd.OutOrStdout(),
				})
				if err != nil {
					return err
				}
				printRun(cmd.OutOrStdout(), args[0], args[1], run, 0)
				if run.GetVerdict() != kernel.VerdictAccept {
					return &exitError{code: ExitBlocked, msg: "the run concluded " + run.GetVerdict(), quiet: true}
				}
				return nil
			}
			if steps != 0 {
				return errors.New("--steps applies to a docker-local remote")
			}
			return withBound(k, r, func(s *ledgersync.Syncer) error { return fireOnServer(cmd, k, s, args) })
		},
	}
	cmd.Flags().IntVar(&steps, "steps", 0, "a docker-local node drives at most this many steps (default 0: as far as the item goes)")
	return cmd
}

// fireOnServer pushes the item's chain and asks satelle-server to fire the run.
func fireOnServer(cmd *cobra.Command, k *kernel.Kernel, s *ledgersync.Syncer, args []string) error {
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
}

func remoteStatusCmd() *cobra.Command {
	var wait time.Duration
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "status <remote-id> <item>",
		Short: "Show the latest run of the remote for the item; pull its chain once it concluded",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, r, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			_, _, local, err := localRemote(r, args[0])
			if err != nil {
				return err
			}
			show := func(get func(ctx context.Context) (*satellepb.ItemRun, error), pull func(ctx context.Context) (int, error)) error {
				run, err := pollRun(cmd.Context(), wait, get)
				if err != nil {
					return err
				}
				pulled := 0
				if pull != nil && remotenode.RunFinished(run.GetState()) {
					if pulled, err = pull(cmd.Context()); err != nil {
						return err
					}
				}
				if *asJSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{"state": run.GetState(), "node": run.GetNodeId(),
						"verdict": run.GetVerdict(), "notes": run.GetNotes(), "reason": run.GetReason(), "pulled": pulled})
				}
				printRun(cmd.OutOrStdout(), args[0], args[1], run, pulled)
				return nil
			}
			if local {
				// The fire imported the node's chain: there is nothing to pull.
				runs := localRuns(r)
				return show(func(context.Context) (*satellepb.ItemRun, error) { return runs.Load(args[0], args[1]) }, nil)
			}
			return withBound(k, r, func(s *ledgersync.Syncer) error {
				return show(func(ctx context.Context) (*satellepb.ItemRun, error) {
					run, err := s.Client.Remotes.GetItemRun(ctx, &satellepb.GetItemRunRequest{RemoteId: args[0], ItemId: args[1]})
					if err != nil {
						return nil, hosted.Describe("RemoteService.GetItemRun", err)
					}
					return run, nil
				}, func(ctx context.Context) (int, error) {
					res, err := s.PullItem(ctx, args[1])
					return res.Events, err
				})
			})
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 0, "poll until the run concluded or stopped, for at most this long")
	asJSON = jsonFlag(cmd)
	return cmd
}

// pollRun gets the run once, or with wait > 0 until it is finished, for at
// most wait.
func pollRun(parent context.Context, wait time.Duration, get func(ctx context.Context) (*satellepb.ItemRun, error)) (*satellepb.ItemRun, error) {
	ctx := parent
	if wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, wait)
		defer cancel()
	}
	for {
		run, err := get(ctx)
		if err != nil {
			return nil, err
		}
		if wait <= 0 || remotenode.RunFinished(run.GetState()) {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the run is still %s after %s", run.GetState(), wait)
		case <-time.After(statusPoll):
		}
	}
}

// printRun prints a run: its state and node, verdict, notes and reason.
func printRun(out io.Writer, remote, itemID string, run *satellepb.ItemRun, pulled int) {
	fmt.Fprintf(out, "%s on %s: %s", remote, itemID, run.GetState())
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
}

func nodeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "node", Short: "Run as a remote node", Hidden: true}
	cmd.AddCommand(nodeJoinCmd(), nodeRunLocalCmd())
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

func nodeRunLocalCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "run-local",
		Short: "Run the local node a docker-local fire started, from the files it handed over",
		Long: `run-local is what a docker-local node runs in its container. It reads its
definition, substrate, run and the item's chain from <dir>/in, drives the item
and writes its outcome to <dir>/out; the starter copies the chain back. It
exits 0 when the run concluded accepting, 2 when it concluded rejecting.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, err := remotenode.RunLocal(cmd.Context(), remotenode.LocalOptions{Dir: dir, Log: cmd.ErrOrStderr(), Version: buildinfo.Version})
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
	cmd.Flags().StringVar(&dir, "dir", remotenode.LocalNodeDir, "the node's root")
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
