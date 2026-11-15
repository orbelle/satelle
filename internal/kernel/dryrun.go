package kernel

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

const dryRunDir = "dry-run"

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
