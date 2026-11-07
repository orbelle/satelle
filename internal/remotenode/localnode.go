package remotenode

// The node side of a local run: `satelle node run-local` in the container
// FireLocal started. It reads from <dir>/in what satelle-server would have
// served a joining node, and writes its outcome to <dir>/out. It holds no
// token and reaches nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// LocalOptions say where a local node works.
type LocalOptions struct {
	// Dir is the node's root: in/ is read, work/, runtime/ and out/ written.
	Dir string
	// Log is the node's progress. Nil discards.
	Log io.Writer
	// Version is the satelle the node runs, compared with the definition's pin.
	Version string
	// Dispatcher runs the node's bindings; nil is agent.Runner.
	Dispatcher agent.Dispatcher
}

// RunLocal runs one local node: it reads its definition, substrate, run and
// the item's chain from <dir>/in, installs the substrate and the definition's
// overlay into <dir>/work/.satelle, imports the chain (verified), drives the
// item for the run's steps as node:<id>, and writes the outcome to
// <dir>/out/outcome.json. A definition pinning another satelle version than
// the node's adds a warning to the notes; the run goes on.
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
	if run.NodeID == "" || run.Run.ItemID == "" || run.Run.Mode != ModeItem {
		return LocalOutcome{}, fmt.Errorf("%s names no node or item, or is not an item run", localRunFile)
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

	src, err := ledger.Open(in)
	if err != nil {
		return LocalOutcome{}, err
	}
	chain, err := src.Read(run.Run.ItemID)
	if err != nil {
		return LocalOutcome{}, err
	}
	if len(chain) == 0 {
		return LocalOutcome{}, fmt.Errorf("the run's item %s has no chain in %s", run.Run.ItemID, in)
	}
	if err := ledger.VerifyChain(run.Run.ItemID, chain); err != nil {
		return LocalOutcome{}, fmt.Errorf("the item's chain does not verify: %w", err)
	}
	if _, err := k.Ledger.Import(chain); err != nil {
		return LocalOutcome{}, err
	}
	if err := k.Cache.Sync(k.Ledger); err != nil {
		return LocalOutcome{}, err
	}

	var warn string
	if base, _, _ := strings.Cut(o.Version, "+"); def.SatelleVersion != "" && base != def.SatelleVersion {
		warn = fmt.Sprintf("warning: the definition pins satelle %s; the node runs %s", def.SatelleVersion, o.Version)
		fmt.Fprintln(o.Log, "node:", warn)
	}
	fmt.Fprintf(o.Log, "node: driving %s\n", run.Run.ItemID)
	res, err := k.DriveSteps(ctx, run.Run.ItemID, "node:"+run.NodeID, run.Steps)
	if err != nil {
		return LocalOutcome{}, err
	}
	out := LocalOutcome{Verdict: res.Verdict, Notes: fmt.Sprintf("%s: %s", run.Run.ItemID, res.Notes), Steps: res.Steps}
	if warn != "" {
		out.Notes += "\n" + warn
	}
	fmt.Fprintf(o.Log, "node: %s at %s: %s\n", run.Run.ItemID, res.Item.Status, res.Notes)
	return out, writeOutcome(filepath.Join(o.Dir, "out"), out)
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
