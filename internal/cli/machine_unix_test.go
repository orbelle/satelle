//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// recordingStore writes a store command that appends its argv to a log,
// prints error: unavailable: down and exits 1, configures it as the repo's
// [ledger] store, and returns the log's path. A path that must not contact
// the store leaves no log.
func recordingStore(t *testing.T, dir string) string {
	t.Helper()
	work := t.TempDir()
	calls := filepath.Join(work, "calls.log")
	rec := filepath.Join(work, "rec.sh")
	if err := os.WriteFile(rec, []byte("echo \"$@\" >> '"+calls+"'\necho 'error: unavailable: down' >&2\nexit 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configureLedger(t, dir, `store = { command = "sh", args = ['`+rec+`'] }`)
	return calls
}

// noStoreCall fails the test when the recording store was called.
func noStoreCall(t *testing.T, calls string) {
	t.Helper()
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		b, _ := os.ReadFile(calls)
		t.Fatalf("the store was called:\n%s", b)
	}
}
