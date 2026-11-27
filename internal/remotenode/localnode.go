package remotenode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

type LocalOptions struct {
	Dir        string
	Log        io.Writer
	Version    string
	Dispatcher agent.Dispatcher
	Secrets    io.Reader
	Progress   bool
}

type stepLog struct{ path string }

func (s *stepLog) record(line string) {
	if s == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(line+"\n"), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

func (s *stepLog) start(mode, item string) { s.record(fmt.Sprintf("start %s %s", mode, item)) }

func (s *stepLog) setup(phase string, i, n int) { s.record(fmt.Sprintf("setup %s %d/%d", phase, i, n)) }

func (s *stepLog) execution(task, exec string) {
	s.record(fmt.Sprintf("task %s: execution %s", task, exec))
}

func (s *stepLog) drive(target string) { s.record("drive " + target) }

func (s *stepLog) dispatch(role agent.Role) { s.record("dispatch " + string(role)) }

func (s *stepLog) concluded(verdict, status string) {
	s.record(fmt.Sprintf("concluded %s at %s", verdict, status))
}

type stepDispatcher struct {
	inner agent.Dispatcher
	steps *stepLog
}

func (d stepDispatcher) Dispatch(ctx context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	d.steps.dispatch(role)
	return d.inner.Dispatch(ctx, role, req)
}

const maxSecretPayload = 1 << 20

const localSetupTimeout = 20 * time.Minute

func RunLocal(ctx context.Context, o LocalOptions) (out LocalOutcome, err error) {
	if o.Log == nil {
		o.Log = io.Discard
	}
	var values []string
	var buf bytes.Buffer
	log := o.Log
	o.Log = &buf
	defer func() {
		_, _ = io.WriteString(log, redact(buf.String(), values))
		out.Notes = redact(out.Notes, values)
		if err != nil {
			err = errors.New(redact(err.Error(), values))
		}
	}()
	return runLocal(ctx, o, &values)
}

func checkLayout(got, implements int) error {
	if got == 0 {
		return fmt.Errorf("%s names no node layout; this node implements node layout %d; refused before setup", localRunFile, implements)
	}
	if got != implements {
		return fmt.Errorf("%s is laid out for node layout %d; this node implements node layout %d; refused before setup", localRunFile, got, implements)
	}
	return nil
}

func runLocal(ctx context.Context, o LocalOptions, values *[]string) (LocalOutcome, error) {
	if o.Dispatcher == nil {
		o.Dispatcher = agent.Runner{}
	}
	in := filepath.Join(o.Dir, "in")
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
	if err := checkLayout(run.Layout, NodeLayout); err != nil {
		return LocalOutcome{}, err
	}
	bundle, err := os.ReadFile(filepath.Join(in, localDefinition))
	if err != nil {
		return LocalOutcome{}, err
	}
	def, overlay, err := ReadBundle(bundle)
	if err != nil {
		return LocalOutcome{}, err
	}
	var steps *stepLog
	if o.Progress {
		if names := secretNames(def); len(names) > 0 {
			return LocalOutcome{}, fmt.Errorf("--progress cannot be given to a node whose definition declares secret %s", names[0])
		}
		steps = &stepLog{path: filepath.Join(o.Dir, "out", detachedProgress)}
		o.Dispatcher = stepDispatcher{inner: o.Dispatcher, steps: steps}
	}
	steps.start(run.Run.Mode, run.Run.ItemID)
	fmt.Fprintf(o.Log, "node: definition %s, run %s %s\n", def.Name, run.Run.Mode, run.Run.ItemID)
	var priv ed25519.PrivateKey
	if *values, priv, err = readNodeInput(def, o.Secrets); err != nil {
		return LocalOutcome{}, err
	}
	var clean func(string) string
	if len(*values) > 0 {
		vs := *values
		clean = func(s string) string { return redact(s, vs) }
		fmt.Fprintf(o.Log, "node: read %d secrets\n", len(vs))
	}

	root := filepath.Join(o.Dir, nodeSubstrateDir)
	tree := filepath.Join(o.Dir, "work")
	runtime := filepath.Join(o.Dir, "runtime")
	if err := unpackSubstrate(in, root, overlay); err != nil {
		return LocalOutcome{}, err
	}
	if err := os.MkdirAll(tree, 0o755); err != nil {
		return LocalOutcome{}, err
	}
	if run.WorkCommit != "" {
		fmt.Fprintf(o.Log, "node: work tree at %s\n", run.WorkCommit)
	}
	if err := sealSubstrate(root); err != nil {
		return LocalOutcome{}, err
	}
	sealed, err := snapshotSubstrate(root, true)
	if err != nil {
		return LocalOutcome{}, err
	}
	guard := &substrateGuard{root: root, sealed: sealed}
	// The substrate is complete and sealed, so the stamped digest is final:
	// every satelle that setup, a check or a performer runs from the work tree
	// opens this substrate and this node's runtime from here on.
	nr, err := MarkNodeRun(o.Dir, run.NodeID, root, tree, secretNames(def))
	if err != nil {
		return LocalOutcome{}, err
	}
	defer nr.Restore()
	reject := func(head string, lines []string, prefix, status string) (LocalOutcome, error) {
		fmt.Fprintln(o.Log, "node: "+head)
		return rejectRun(o.Dir, *values, priv, head, lines, prefix, steps, status)
	}
	for _, phase := range []struct {
		name string
		cmds []string
	}{{SetupOnCreate, def.Setup.OnCreate}, {SetupPostStart, def.Setup.PostStart}} {
		if len(phase.cmds) == 0 {
			continue
		}
		fmt.Fprintf(o.Log, "node: setup %s: %d commands\n", phase.name, len(phase.cmds))
		for i, c := range phase.cmds {
			steps.setup(phase.name, i+1, len(phase.cmds))
			err := RunSetup(ctx, tree, phase.name, []string{c}, []string{kernel.SubstrateVar(root)}, localSetupTimeout, o.Log)
			if guard.check() != nil {
				return reject(changedBy(fmt.Sprintf("setup %s command %d", phase.name, i+1)), guard.failures(), "", "setup")
			}
			if err != nil {
				var se *SetupError
				if errors.As(err, &se) {
					se.Index = i + 1
				}
				return LocalOutcome{}, err
			}
		}
	}
	if lines, err := hashedDiff(guard); err != nil {
		return LocalOutcome{}, err
	} else if len(lines) > 0 {
		return reject(changedBy("setup"), lines, "", "setup")
	}
	records, err := prepareRecords(tree)
	if err != nil {
		return reject(err.Error(), nil, "", "setup")
	}
	k, err := openNodeKernel(root, tree, runtime, nil, guardDispatcher{inner: o.Dispatcher, guard: guard}, nil, clean)
	if err != nil {
		return LocalOutcome{}, err
	}
	k.RecordsDir = records
	k.Guard = guard.check
	var closeOnce sync.Once
	closeKernel := func() { closeOnce.Do(func() { k.Close() }) }
	defer closeKernel()

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
		steps.execution(run.Run.ItemID, exec.ID)
		prefix = fmt.Sprintf("task %s: execution %s over %d items\n", run.Run.ItemID, exec.ID, len(rec.Inputs))
		fmt.Fprint(o.Log, "node: "+prefix)
	}

	var warn string
	if base, _, _ := strings.Cut(o.Version, "+"); def.SatelleVersion != "" && base != def.SatelleVersion {
		warn = fmt.Sprintf("warning: the definition pins satelle %s; the node runs %s", def.SatelleVersion, o.Version)
		fmt.Fprintln(o.Log, "node:", warn)
	}
	steps.drive(target)
	fmt.Fprintf(o.Log, "node: driving %s\n", target)
	res, err := k.DriveSteps(ctx, target, actor, run.Steps)
	if err != nil {
		if lines := guard.failures(); len(lines) > 0 {
			closeKernel()
			return reject(changedBy("the run"), lines, prefix, "")
		}
		return LocalOutcome{}, err
	}
	out := LocalOutcome{Verdict: res.Verdict, Notes: fmt.Sprintf("%s%s: %s", prefix, target, res.Notes), Steps: res.Steps}
	gates, err := runGateNotes(k.Ledger, target, actor)
	if err != nil {
		return LocalOutcome{}, err
	}
	out.Notes += gates
	if warn != "" {
		out.Notes += "\n" + warn
	}
	lines, err := hashedDiff(guard)
	if err != nil {
		return LocalOutcome{}, err
	}
	if len(lines) > 0 {
		closeKernel()
		return reject(changedBy("the run"), lines, out.Notes+"\n", res.Item.Status)
	}
	out.Notes = redact(out.Notes, *values)
	steps.concluded(res.Verdict, res.Item.Status)
	fmt.Fprintf(o.Log, "node: %s at %s: %s\n", target, res.Item.Status, res.Notes)
	outDir := filepath.Join(o.Dir, "out")
	if err := nr.Verify(); err != nil {
		if rerr := os.Remove(filepath.Join(outDir, localOutcome)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
		return LocalOutcome{}, err
	}
	if out.Heads, err = ledgerHeads(k.Ledger); err != nil {
		return LocalOutcome{}, err
	}
	if out.Seal, err = sealOutcome(priv, out); err != nil {
		return LocalOutcome{}, err
	}
	return out, writeOutcome(outDir, out)
}

func runGateNotes(l *ledger.Store, target, actor string) (string, error) {
	events, err := l.Read(target)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range events {
		if e.Kind != gate.KindRun || e.Actor != actor {
			continue
		}
		var r gate.Result
		if err := json.Unmarshal(e.Payload, &r); err != nil {
			return "", fmt.Errorf("a gate.run of %s: %w", target, err)
		}
		fmt.Fprintf(&b, "\ngate %s %s:", r.Gate, r.Result)
		if notes := strings.TrimRight(r.Notes, "\n"); notes != "" {
			b.WriteString("\n" + notes)
		}
	}
	return b.String(), nil
}

func readNodeInput(def remotedef.Definition, in io.Reader) ([]string, ed25519.PrivateKey, error) {
	names := secretNames(def)
	if in == nil {
		if len(names) > 0 {
			return nil, nil, fmt.Errorf("the definition declares secret %s and none was given on stdin", names[0])
		}
		return nil, nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(in, maxSecretPayload+1))
	if err != nil {
		return nil, nil, fmt.Errorf("reading secrets from stdin: %w", err)
	}
	if len(b) > maxSecretPayload {
		return nil, nil, fmt.Errorf("the secrets on stdin are over %d bytes", maxSecretPayload)
	}
	given := map[string]string{}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &given); err != nil {
			return nil, nil, errors.New("the secrets on stdin are not a JSON object of names to values")
		}
	}
	var priv ed25519.PrivateKey
	if v, ok := given[sealPayloadKey]; ok {
		k, err := hex.DecodeString(v)
		if err != nil || len(k) != ed25519.PrivateKeySize {
			return nil, nil, errors.New("the run's seal key on stdin is not an Ed25519 private key")
		}
		priv = ed25519.PrivateKey(k)
	}
	if len(names) == 0 {
		return nil, priv, nil
	}
	taken := map[string]string{}
	for _, n := range names {
		v := given[n]
		if v == "" {
			return nil, nil, fmt.Errorf("secret %s was not given on stdin", n)
		}
		taken[n] = v
	}
	for _, n := range names {
		if err := os.Setenv(n, taken[n]); err != nil {
			return nil, nil, fmt.Errorf("secret %s: %w", n, err)
		}
	}
	return longestFirst(taken), priv, nil
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
