package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
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
	cmd.AddCommand(ledgerAppendCmd(), ledgerListCmd(), ledgerVerifyCmd(), ledgerRebuildCmd())
	return cmd
}

func ledgerAppendCmd() *cobra.Command {
	var kind, payload string
	cmd := &cobra.Command{
		Use:   "append <item> --kind K [--payload JSON]",
		Short: "Append an event to an item's chain (carried, never read by satelle)",
		Args:  cobra.ExactArgs(1),
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
		Short: "Verify every item's hash chain",
		Args:  cobra.NoArgs,
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
			fmt.Fprintf(cmd.OutOrStdout(), "ledger verified: %d chains intact\n", len(items))
			return nil
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
					fmt.Fprintln(out)
				}
			}
			return nil
		},
	}
	asJSON = jsonFlag(show)
	cmd.AddCommand(show)
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
