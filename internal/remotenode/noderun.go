package remotenode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
)

// NodeRun is a node run's mark on its runtime and on the process env: the
// node stamp in <dir>/runtime and the hint every check and dispatch it
// starts inherits.
type NodeRun struct {
	runtime string
	stamp   config.RuntimeStamp
	prev    map[string]*string
}

// MarkNodeRun stamps runDir/runtime as the node runtime of this process,
// recording the substrate and tree the run was given and the substrate's
// digest, and sets the node hint and the withheld secret names in the
// process env. Call it after the node kernel is open, and Restore after.
func MarkNodeRun(runDir, nodeID, substrate, tree string, withhold []string) (*NodeRun, error) {
	var err error
	if runDir, err = config.CanonicalPath(runDir); err != nil {
		return nil, fmt.Errorf("the node run directory: %w", err)
	}
	if substrate, err = config.CanonicalPath(substrate); err != nil {
		return nil, fmt.Errorf("the node's substrate: %w", err)
	}
	if tree, err = config.CanonicalPath(tree); err != nil {
		return nil, fmt.Errorf("the node's work tree: %w", err)
	}
	digest, err := kernel.SubstrateDigest(substrate)
	if err != nil {
		return nil, err
	}
	runtime := filepath.Join(runDir, "runtime")
	if err := os.MkdirAll(runtime, 0o755); err != nil {
		return nil, err
	}
	stamp := config.RuntimeStamp{Kind: config.RuntimeNode, Node: nodeID, Substrate: substrate, Tree: tree,
		Digest: digest, Process: procgroup.Self()}
	if err := config.WriteRuntimeStamp(runtime, stamp); err != nil {
		return nil, err
	}
	nr := &NodeRun{runtime: runtime, stamp: stamp, prev: map[string]*string{}}
	for name, v := range map[string]string{config.NodeRunEnv: runDir, config.NodeWithholdEnv: strings.Join(withhold, ",")} {
		if old, ok := os.LookupEnv(name); ok {
			nr.prev[name] = &old
		} else {
			nr.prev[name] = nil
		}
		if err := os.Setenv(name, v); err != nil {
			nr.Restore()
			return nil, err
		}
	}
	return nr, nil
}

// Restore puts back the env values MarkNodeRun replaced.
func (nr *NodeRun) Restore() {
	if nr == nil {
		return
	}
	for name, old := range nr.prev {
		if old == nil {
			_ = os.Unsetenv(name)
		} else {
			_ = os.Setenv(name, *old)
		}
	}
}

// Verify requires the stamp to be as MarkNodeRun wrote it and, for a
// substrate that is not the tree's own .satelle, its digest to be unchanged.
// It detects a stamp changed during the run; it does not make the runtime
// tamper-proof.
func (nr *NodeRun) Verify() error {
	got, err := config.ReadRuntimeStamp(nr.runtime)
	if err != nil {
		return fmt.Errorf("the node runtime's stamp: %w", err)
	}
	if got != nr.stamp {
		return fmt.Errorf("the node runtime's stamp %s changed during the run", filepath.Join(nr.runtime, config.RuntimeStampName))
	}
	if nr.stamp.Substrate == filepath.Join(nr.stamp.Tree, config.DefaultDataDir) {
		return nil
	}
	d, err := kernel.SubstrateDigest(nr.stamp.Substrate)
	if err != nil {
		return err
	}
	if d != nr.stamp.Digest {
		return fmt.Errorf("the node's substrate %s changed during the run", nr.stamp.Substrate)
	}
	return nil
}
