//go:build linux

package kernel

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

// Only a process a dry run started finds itself under it: a marker naming one
// of its ancestors (pid and start identity) makes it read-only; this
// process's own marker, a gone or reused pid, or no marker, does not, so
// another session's writes are never refused.
func TestUnderDryRunOnlyForTheDryRunsDescendants(t *testing.T) {
	k, _, _ := dryFixture(t)
	dir := k.Ledger.Dir()
	if UnderDryRun(dir) {
		t.Fatal("read-only with no dry run in progress")
	}
	unmark, err := k.markDryRun()
	if err != nil {
		t.Fatal(err)
	}
	if UnderDryRun(dir) {
		t.Fatal("a dry run's own marker made the process itself read-only")
	}
	unmark()
	if entries, _ := os.ReadDir(filepath.Join(dir, dryRunDir)); len(entries) != 0 {
		t.Fatalf("marker left behind: %v", entries)
	}

	parent := procgroup.Ancestors(os.Getpid())
	if len(parent) == 0 {
		t.Skip("no readable parent")
	}
	marker := filepath.Join(dir, dryRunDir, strconv.Itoa(parent[0].PID))
	if err := os.WriteFile(marker, []byte("not-its-start"), 0o644); err != nil {
		t.Fatal(err)
	}
	if UnderDryRun(dir) {
		t.Fatal("a reused pid (another start identity) matched")
	}
	if err := os.WriteFile(marker, []byte(parent[0].Start), 0o644); err != nil {
		t.Fatal(err)
	}
	if !UnderDryRun(dir) {
		t.Fatal("a dry run's descendant is not read-only")
	}
}
