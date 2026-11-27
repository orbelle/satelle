package remotenode

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/upgrade"
)

const nodeSubstrateDir = "substrate"

const substrateFileMode = 0o444

type substrateEntry struct {
	Mode   fs.FileMode
	Size   int64
	SHA256 string
	Link   string
	ID     config.FileID
}

type substrateState map[string]substrateEntry

func snapshotSubstrate(root string, hash bool) (substrateState, error) {
	st := substrateState{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		e := substrateEntry{Mode: info.Mode()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if e.Link, err = os.Readlink(p); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			e.Size = info.Size()
		}
		e.ID, _ = config.FileIdentity(info)
		st[filepath.ToSlash(rel)] = e
		return nil
	})
	if err != nil {
		return nil, err
	}
	if hash {
		entries, err := upgrade.Manifest(root)
		if err != nil {
			return nil, err
		}
		for _, m := range entries {
			e, ok := st[m.Path]
			if !ok {
				e = substrateEntry{Mode: m.Mode, Size: m.Size, Link: m.Link}
			}
			e.SHA256 = m.SHA256
			st[m.Path] = e
		}
	}
	return st, nil
}

func diffSubstrate(before, after substrateState) []string {
	var lines []string
	for p, b := range before {
		a, ok := after[p]
		if !ok {
			lines = append(lines, "removed "+p)
			continue
		}
		if b.SHA256 == "" || a.SHA256 == "" {
			a.SHA256, b.SHA256 = "", ""
		}
		if a != b {
			lines = append(lines, "changed "+p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			lines = append(lines, "added "+p)
		}
	}
	sort.Slice(lines, func(i, j int) bool {
		pi, pj := lines[i][strings.IndexByte(lines[i], ' ')+1:], lines[j][strings.IndexByte(lines[j], ' ')+1:]
		if pi != pj {
			return pi < pj
		}
		return lines[i] < lines[j]
	})
	return lines
}

type substrateChangedError struct{ Lines []string }

func (e *substrateChangedError) Error() string {
	return "the node's substrate was changed: " + strings.Join(e.Lines, ", ")
}

type substrateGuard struct {
	root   string
	sealed substrateState
	mu     sync.Mutex
	failed []string
}

func (g *substrateGuard) check() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.failed) > 0 {
		return &substrateChangedError{Lines: g.failed}
	}
	now, err := snapshotSubstrate(g.root, false)
	if err != nil {
		g.failed = []string{"unreadable: " + err.Error()}
		return &substrateChangedError{Lines: g.failed}
	}
	if lines := diffSubstrate(g.sealed, now); len(lines) > 0 {
		g.failed = lines
		return &substrateChangedError{Lines: lines}
	}
	return nil
}

func (g *substrateGuard) failures() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.failed...)
}

type guardDispatcher struct {
	inner agent.Dispatcher
	guard *substrateGuard
}

func (d guardDispatcher) Dispatch(ctx context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	if err := d.guard.check(); err != nil {
		return agent.Result{}, err
	}
	return d.inner.Dispatch(ctx, role, req)
}

func hashedDiff(g *substrateGuard) ([]string, error) {
	if err := g.check(); err != nil {
		return g.failures(), nil
	}
	now, err := snapshotSubstrate(g.root, true)
	if err != nil {
		return nil, err
	}
	return diffSubstrate(g.sealed, now), nil
}

func unpackSubstrate(in, root string, overlay []fileEntry) error {
	sub, err := os.ReadFile(filepath.Join(in, localSubstrate))
	if err != nil {
		return err
	}
	entries, err := filebundle.Decode(sub, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", localSubstrate, err)
	}
	if err := filebundle.Write(root, entries, false, nil); err != nil {
		return err
	}
	if err := writeOverlay(root, overlay); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(root, "satelle.toml")); errors.Is(err, fs.ErrNotExist) {
		return kernel.Init(root)
	} else if err != nil {
		return err
	}
	return nil
}

func sealSubstrate(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return os.Chmod(p, substrateFileMode)
	})
}

func prepareRecords(tree string) (string, error) {
	data := filepath.Join(tree, ".satelle")
	records := filepath.Join(data, "runs")
	if fi, err := os.Lstat(tree); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("refused: the work tree %s is not a directory", tree)
	}
	fi, err := os.Lstat(data)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(data, 0o755); err != nil {
			return "", fmt.Errorf("refused: %w", err)
		}
	case err != nil:
		return "", fmt.Errorf("refused: %w", err)
	case fi.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("refused: %s is a symlink, not a directory", data)
	case !fi.IsDir():
		return "", fmt.Errorf("refused: %s is not a directory", data)
	}
	if _, err := os.Lstat(records); !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("refused: %s already exists in the work tree", records)
	}
	if err := os.Mkdir(records, 0o755); err != nil {
		return "", fmt.Errorf("refused: %w", err)
	}
	evalTree, err := filepath.EvalSymlinks(tree)
	if err != nil {
		return "", fmt.Errorf("refused: %w", err)
	}
	if real, err := filepath.EvalSymlinks(records); err != nil || real != filepath.Join(evalTree, ".satelle", "runs") {
		return "", fmt.Errorf("refused: %s does not resolve inside the work tree", records)
	}
	return records, nil
}

func changedBy(phase string) string {
	return fmt.Sprintf("the node's substrate was changed by %s; nothing of the run is brought back", phase)
}

func rejectRun(dir string, values []string, priv ed25519.PrivateKey, head string, lines []string, prefix string, steps *stepLog, status string) (LocalOutcome, error) {
	runtime := filepath.Join(dir, "runtime")
	for _, name := range []string{"ledger", "blobs"} {
		if err := os.RemoveAll(filepath.Join(runtime, name)); err != nil {
			return LocalOutcome{}, err
		}
	}
	l, err := ledger.Open(runtime)
	if err != nil {
		return LocalOutcome{}, err
	}
	notes := prefix + head
	for _, line := range lines {
		notes += "\n" + line
	}
	out := LocalOutcome{Verdict: kernel.VerdictReject, Notes: redact(notes, values)}
	if out.Heads, err = ledgerHeads(l); err != nil {
		return LocalOutcome{}, err
	}
	if out.Seal, err = sealOutcome(priv, out); err != nil {
		return LocalOutcome{}, err
	}
	steps.concluded(kernel.VerdictReject, status)
	return out, writeOutcome(filepath.Join(dir, "out"), out)
}
