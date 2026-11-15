package kernel

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

// dryRunDir, under the ledger's directory, holds one marker per dry run in
// progress: a file named by the dry run's pid holding its start identity.
const dryRunDir = "dry-run"

// markDryRun records this process as a dry run in progress until the
// returned func removes the marker.
func (k *Kernel) markDryRun() (func(), error) {
	dir := filepath.Join(k.Ledger.Dir(), dryRunDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	self := procgroup.Self()
	path := filepath.Join(dir, strconv.Itoa(self.PID))
	if err := os.WriteFile(path, []byte(self.Start), 0o644); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

// UnderDryRun reports whether a dry run in progress, recorded under the
// ledger directory ledgerDir, started this process: one of its ancestors
// holds a marker with the same pid and start identity. A marker whose process
// is gone, or one on a platform that cannot read a process's parents, never
// matches, so the ledger stays writable for every other process.
func UnderDryRun(ledgerDir string) bool {
	entries, err := os.ReadDir(filepath.Join(ledgerDir, dryRunDir))
	if err != nil || len(entries) == 0 {
		return false
	}
	for _, a := range procgroup.Ancestors(os.Getpid()) {
		b, err := os.ReadFile(filepath.Join(ledgerDir, dryRunDir, strconv.Itoa(a.PID)))
		if err == nil && string(b) == a.Start {
			return true
		}
	}
	return false
}
