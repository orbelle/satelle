package remotenode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/ledger"
)

type LocalOptions struct {
	Dir        string
	Log        io.Writer
	Version    string
	Dispatcher agent.Dispatcher
}

func RunLocal(ctx context.Context, o LocalOptions) (LocalOutcome, error) {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Dispatcher == nil {
		o.Dispatcher = agent.Runner{}
	}
	in := filepath.Join(o.Dir, "in")
	bundle, err := os.ReadFile(filepath.Join(in, localDefinition))
	if err != nil {
		return LocalOutcome{}, err
	}
	def, overlay, err := ReadBundle(bundle)
	if err != nil {
		return LocalOutcome{}, err
	}
	var run LocalRun
	b, err := os.ReadFile(filepath.Join(in, localRunFile))
	if err != nil {
		return LocalOutcome{}, err
	}
	if err := json.Unmarshal(b, &run); err != nil {
		return LocalOutcome{}, fmt.Errorf("%s: %w", localRunFile, err)
	}
	if run.NodeID == "" || run.Run.ItemID == "" || run.Run.Mode != ModeItem && run.Run.Mode != ModeTask {
		return LocalOutcome{}, fmt.Errorf("%s names no node or item, or is not an item or task run", localRunFile)
	}
	fmt.Fprintf(o.Log, "node: definition %s, run %s %s\n", def.Name, run.Run.Mode, run.Run.ItemID)

	tree := filepath.Join(o.Dir, "work")
	root := filepath.Join(tree, ".satelle")
	sub, err := os.ReadFile(filepath.Join(in, localSubstrate))
	if err != nil {
		return LocalOutcome{}, err
	}
	entries, err := filebundle.Decode(sub, nil)
	if err != nil {
		return LocalOutcome{}, fmt.Errorf("%s: %w", localSubstrate, err)
	}
	if err := filebundle.Write(root, entries, false, nil); err != nil {
		return LocalOutcome{}, err
	}
	k, err := openNodeKernel(root, tree, filepath.Join(o.Dir, "runtime"), overlay, o.Dispatcher, nil)
	if err != nil {
		return LocalOutcome{}, err
	}
	defer k.Close()

	if err := importIn(k.Ledger, in, run.Run.ItemID); err != nil {
		return LocalOutcome{}, err
	}
	if err := k.Cache.Sync(k.Ledger); err != nil {
		return LocalOutcome{}, err
	}
	actor := "node:" + run.NodeID
	target, prefix := run.Run.ItemID, ""
	if run.Run.Mode == ModeTask {
		exec, rec, err := k.TaskRun(run.Run.ItemID, "", actor)
		if err != nil {
			return LocalOutcome{}, err
		}
		target = exec.ID
		prefix = fmt.Sprintf("task %s: execution %s over %d items\n", run.Run.ItemID, exec.ID, len(rec.Inputs))
		fmt.Fprint(o.Log, "node: "+prefix)
	}

	var warn string
	if base, _, _ := strings.Cut(o.Version, "+"); def.SatelleVersion != "" && base != def.SatelleVersion {
		warn = fmt.Sprintf("warning: the definition pins satelle %s; the node runs %s", def.SatelleVersion, o.Version)
		fmt.Fprintln(o.Log, "node:", warn)
	}
	fmt.Fprintf(o.Log, "node: driving %s\n", target)
	res, err := k.DriveSteps(ctx, target, actor, run.Steps)
	if err != nil {
		return LocalOutcome{}, err
	}
	out := LocalOutcome{Verdict: res.Verdict, Notes: fmt.Sprintf("%s%s: %s", prefix, target, res.Notes), Steps: res.Steps}
	if warn != "" {
		out.Notes += "\n" + warn
	}
	fmt.Fprintf(o.Log, "node: %s at %s: %s\n", target, res.Item.Status, res.Notes)
	return out, writeOutcome(filepath.Join(o.Dir, "out"), out)
}

func importIn(dst *ledger.Store, in, itemID string) error {
	src, err := ledger.Open(in)
	if err != nil {
		return err
	}
	ids, err := src.Items()
	if err != nil {
		return err
	}
	if !slices.Contains(ids, itemID) {
		return fmt.Errorf("the run's item %s has no chain in %s", itemID, in)
	}
	for _, id := range ids {
		chain, err := src.Read(id)
		if err != nil {
			return err
		}
		if len(chain) == 0 {
			return fmt.Errorf("the chain of %s in %s is empty", id, in)
		}
		if err := ledger.VerifyChain(id, chain); err != nil {
			return fmt.Errorf("the chain of %s does not verify: %w", id, err)
		}
		if _, err := dst.Import(chain); err != nil {
			return err
		}
	}
	return nil
}

func writeOutcome(dir string, out LocalOutcome) error {
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, localOutcome), append(b, '\n'), 0o644)
}
