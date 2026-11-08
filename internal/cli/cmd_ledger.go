package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func ledgerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ledger",
		Short: "Read, append to, verify and rebuild the ledger",
		Long: `The ledger is satelle's history of record: one append-only, hash-chained
JSONL file per item under the runtime directory. The sqlite cache is rebuilt
from it.`,
	}
	cmd.AddCommand(ledgerAppendCmd(), ledgerListCmd(), ledgerVerifyCmd(), ledgerRebuildCmd(), ledgerStoreTestCmd(), ledgerPushCmd(), ledgerPullCmd())
	return cmd
}

func ledgerStoreTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "store-test",
		Short: "Prove the configured ledger store command against the adapter contract",
		Long: `Drive the external store command configured as
[ledger] store = { command = "...", args = ["..."] } through the adapter
contract: put-if-absent, get and list, and the error line. It creates and reads
keys under conformance/ only, prints one line per check, and exits 0 only when
every check passes. The contract has no delete, so each run's keys stay under
conformance/<run>/. See 'satelle help ledger-store'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			c, err := storeCommand(k, "store-test")
			if err != nil {
				return err
			}
			spec := k.Settings.Ledger.Store
			out := cmd.OutOrStdout()
			var failed []string
			for _, r := range ledgerstore.Conformance(cmd.Context(), c) {
				if r.OK {
					fmt.Fprintf(out, "ok    %s\n", r.Name)
					continue
				}
				failed = append(failed, r.Name)
				fmt.Fprintf(out, "FAIL  %s: %s\n", r.Name, r.Reason)
			}
			if len(failed) > 0 {
				return &exitError{code: ExitError, msg: fmt.Sprintf("store-test: %d of %d checks failed: %s",
					len(failed), ledgerstore.ConformanceChecks, strings.Join(failed, "; "))}
			}
			fmt.Fprintf(out, "store-test: %d checks passed against %s\n", ledgerstore.ConformanceChecks, spec.Command)
			return nil
		},
	}
}

// storeCommand is the configured external store command, run in the working
// tree without satelle's credentials. With no external command configured (no
// [ledger] store, or a built-in store named) it refuses, naming the verb, and
// nothing is run.
func storeCommand(k *kernel.Kernel, verb string) (ledgerstore.Command, error) {
	spec := k.Settings.Ledger.Store
	const only = "only an external store command ([ledger] store = { command = ..., args = [...] }) implements the adapter operations put-if-absent, get and list"
	if !spec.External() {
		if spec.Kind == "" {
			return ledgerstore.Command{}, &exitError{code: ExitError, msg: "satelle ledger " + verb + ": no [ledger] store is configured; " + only}
		}
		return ledgerstore.Command{}, &exitError{code: ExitError, msg: fmt.Sprintf("satelle ledger %s: [ledger] store is the built-in %q store, which implements no adapter operations; %s", verb, spec.Kind, only)}
	}
	path := spec.Command
	if strings.ContainsRune(path, '/') && !filepath.IsAbs(path) {
		path = filepath.Join(k.Dir, path)
	}
	return ledgerstore.Command{Path: path, Args: spec.Args, Dir: k.Dir,
		Withhold: []string{hosted.SessionTokenEnv, provider.EnvNodeToken}}, nil
}

// replicationFailure is push's or pull's exit 1 when an item diverged or was
// refused; gaps never change the exit.
func replicationFailure(verb string, res ledgerrepl.Result) error {
	if !res.Failed() {
		return nil
	}
	var parts []string
	if len(res.Diverged) > 0 {
		parts = append(parts, fmt.Sprintf("%d item(s) diverged from the store: %s", len(res.Diverged), strings.Join(res.DivergedItems(), ", ")))
	}
	if len(res.Refused) > 0 {
		parts = append(parts, fmt.Sprintf("%d item(s) refused: %s", len(res.Refused), strings.Join(res.Refused, "; ")))
	}
	return &exitError{code: ExitError, msg: verb + ": " + strings.Join(parts, "; ")}
}

func ledgerPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Write every chain and the blobs it references to the ledger store",
		Long: `Write every item's chain to the external store command configured as
[ledger] store, one object per event at chains/<item>/<seq>.json by
put-if-absent, and each blob a chain references (attachment bodies and
performer outputs) at blobs/<sha256> before the event that references it.

An event already in the store with the same hash is already there; a different
hash at a sequence is a divergence: that item's push stops there, naming the
item and sequence, other items continue, and push exits 1. A blob neither this
runtime nor the store holds is a gap: named, never fatal. A store error ends
the push with its cause and exit 1; a rerun is safe. See 'satelle help
ledger-store'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			c, err := storeCommand(k, "push")
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			r := ledgerrepl.Replicator{Ledger: k.Ledger, Objects: c, Refs: kernel.BlobRefs, Out: out}
			res, err := r.Push(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "push: %d event(s) and %d blob(s) written on %d item(s); %d blob gap(s)\n",
				res.Events, res.Blobs, res.Items, len(res.Gaps))
			return replicationFailure("push", res)
		},
	}
}

func ledgerPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Bring the ledger store's chains and blobs into this runtime, verified",
		Long: `Read every chain the external store command configured as [ledger] store
holds and append the events this runtime lacks, after verifying them against
the hash chain. Before appending, the store's event at the last sequence both
chains hold is compared, whatever their lengths: a chain that diverged is
refused, naming the item and fork point, nothing is appended to it, and pull
exits 1. A store chain that is a prefix of the local one is not a conflict;
the next push writes the rest.

Then each blob a chain references and this runtime lacks is fetched and
checked against its sha256 before it is stored. One the store lacks, or holds
with other bytes, is a gap: named, never fatal. See 'satelle help
ledger-store'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			c, err := storeCommand(k, "pull")
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			r := ledgerrepl.Replicator{Ledger: k.Ledger, Objects: c, Refs: kernel.BlobRefs, Out: out}
			res, err := r.Pull(cmd.Context())
			if serr := k.Cache.Sync(k.Ledger); err == nil {
				err = serr
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "pull: %d event(s) on %d item(s) and %d blob(s); %d blob gap(s)\n",
				res.Events, res.Items, res.Blobs, len(res.Gaps))
			return replicationFailure("pull", res)
		},
	}
}

func ledgerAppendCmd() *cobra.Command {
	var kind, payload string
	cmd := &cobra.Command{
		Use:   "append <item> --kind K [--payload JSON]",
		Short: "Append an event to an item's chain (carried, never read by satelle)",
		Long: `Append an event of a kind satelle carries but never reads. The kinds satelle
reads (its item, seat and attachment events, such as item.transitioned and
attachment.added) are refused: only satelle's own verbs write them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if _, err := k.Get(args[0]); err != nil {
				return err
			}
			if strings.TrimSpace(kind) == "" {
				return fmt.Errorf("--kind is required")
			}
			if item.Folded(kind) {
				return fmt.Errorf("satelle ledger append carries kinds satelle never reads; %s is written only by satelle's own verbs", kind)
			}
			var p any
			if payload != "" {
				var raw json.RawMessage
				if err := json.Unmarshal([]byte(payload), &raw); err != nil {
					return fmt.Errorf("--payload is not JSON: %w", err)
				}
				p = raw
			}
			e, err := k.Ledger.Append(args[0], kind, actor(), p)
			if err != nil {
				return err
			}
			if err := k.Cache.SyncItem(k.Ledger, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", e.ID, e.Item, e.Kind)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "event kind")
	cmd.Flags().StringVar(&payload, "payload", "", "JSON payload")
	return cmd
}

func ledgerListCmd() *cobra.Command {
	var kind string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "list [item]",
		Short: "List events: one item's chain, or every item's by time",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			events, err := k.Cache.Events(id, kind)
			if err != nil {
				return err
			}
			if *asJSON {
				if events == nil {
					events = []ledger.Event{}
				}
				return writeJSON(cmd.OutOrStdout(), events)
			}
			for _, e := range events {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %-14s %-20s %-12s %s\n",
					e.At.Local().Format("2006-01-02 15:04:05"), e.Item, e.Kind, e.Actor, compact(e.Payload))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "only this kind")
	asJSON = jsonFlag(cmd)
	return cmd
}

// compact is a payload on one line, cut to a readable length.
func compact(p json.RawMessage) string {
	s := strings.Join(strings.Fields(string(p)), " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

func ledgerVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Verify every item's hash chain and the blobs the chains reference",
		Long: `Verify every item's hash chain, then check that this runtime holds every blob
a chain references (attachment bodies and performer outputs) intact, printing
one gap line per missing or corrupt blob with its item, event and sha256.

Exit codes:
  0  every chain is intact and every referenced blob is held
  1  a chain is broken (or the ledger could not be read)
  3  every chain is intact but a referenced blob is missing or corrupt: a
     pipeline can gate on it until a later pull fills the gap`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if err := k.Ledger.Verify(); err != nil {
				return err
			}
			items, err := k.Ledger.Items()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			gaps := 0
			for _, item := range items {
				events, err := k.Ledger.Read(item)
				if err != nil {
					return err
				}
				gs, err := k.Ledger.BlobGaps(item, kernel.BlobRefs(events))
				if err != nil {
					return err
				}
				for _, g := range gs {
					fmt.Fprintln(out, g)
				}
				gaps += len(gs)
			}
			if gaps == 0 {
				fmt.Fprintf(out, "ledger verified: %d chains intact\n", len(items))
				return nil
			}
			fmt.Fprintf(out, "ledger verified: %d chains intact; %d blob gap(s)\n", len(items), gaps)
			return &exitError{code: ExitBlobGaps, msg: fmt.Sprintf("ledger verify: %d blob gap(s): a chain references a blob this runtime does not hold intact", gaps)}
		},
	}
}

func ledgerRebuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Drop the cache and rebuild it from the ledger",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if err := k.Cache.Rebuild(k.Ledger); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "cache rebuilt from the ledger")
			return nil
		},
	}
}

func workflowCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "workflow", Short: "Show the configured workflows"}
	var asJSON *bool
	show := &cobra.Command{
		Use:   "show [name]",
		Short: "Show a workflow's states, transitions and gates (default: the default workflow)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			name := k.Rules.Default
			if len(args) == 1 {
				name = args[0]
			}
			w, ok := k.Workflows[name]
			if !ok {
				names := make([]string, 0, len(k.Workflows))
				for n := range k.Workflows {
					names = append(names, n)
				}
				sort.Strings(names)
				return fmt.Errorf("no workflow %q (have: %s)", name, strings.Join(names, ", "))
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), workflowJSON(w))
			}
			v := buildRoute(w, item.Item{Workflow: w.Name}, nil)
			v.Item = w.File
			out := cmd.OutOrStdout()
			printRoute(out, v)
			if len(w.Gates) > 0 {
				fmt.Fprintln(out, "\ngates")
				for _, n := range sortedGateNames(w) {
					g := w.Gates[n]
					judges := strings.Join(g.Judges(), ", ")
					if judges == "" {
						judges = "check"
					}
					fmt.Fprintf(out, "  %-24s skill %-20s by %s", n, g.Skill, judges)
					if len(g.Paths) > 0 {
						fmt.Fprintf(out, "  paths %s", strings.Join(g.Paths, " "))
					}
					if g.Trigger != "" {
						fmt.Fprintf(out, "  trigger %s", g.Trigger)
					}
					if g.Carry {
						fmt.Fprint(out, "  carry")
					}
					if g.Timeout != "" {
						fmt.Fprintf(out, "  timeout %s", g.Timeout)
					}
					fmt.Fprintln(out)
				}
			}
			return nil
		},
	}
	asJSON = jsonFlag(show)
	cmd.AddCommand(show, workflowSelectCmd())
	return cmd
}

// workflowSelectCmd prints the workflow the repo's rules choose for an item
// with the given kind, tags and category, and that workflow's start state:
// the choice create makes when no --workflow is given. It reads only.
func workflowSelectCmd() *cobra.Command {
	var kind, category string
	var tags []string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "select --kind K [--tag T]... [--category C]",
		Short: "Print the workflow and start state the rules choose for a kind, tags and category",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(kind) == "" {
				return fmt.Errorf("--kind is required")
			}
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			name, start, err := k.WorkflowFor(item.Item{Kind: kind, Tags: tags, Category: category})
			if err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]string{"workflow": name, "start": start})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", name, start)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&kind, "kind", "", "the item kind (required)")
	fl.StringArrayVar(&tags, "tag", nil, "a tag (repeatable)")
	fl.StringVar(&category, "category", "", "the category")
	asJSON = jsonFlag(cmd)
	return cmd
}

func sortedGateNames(w *workflow.Workflow) []string {
	out := make([]string, 0, len(w.Gates))
	for n := range w.Gates {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// workflowJSON is a workflow in the shape its TOML has.
func workflowJSON(w *workflow.Workflow) map[string]any {
	return map[string]any{"name": w.Name, "file": w.File, "states": w.States, "gates": w.Gates, "transitions": w.Transitions}
}
