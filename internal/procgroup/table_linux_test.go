//go:build linux

package procgroup

import (
	"os"
	"testing"
)

func TestTableHoldsThisProcess(t *testing.T) {
	if _, err := os.ReadFile("/proc/self/stat"); err != nil {
		t.Skipf("/proc/self cannot be read here: %v", err)
	}
	tab, err := Table()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range tab {
		if e.PID != os.Getpid() {
			continue
		}
		if e.PPID != os.Getppid() {
			t.Errorf("ppid %d, want %d", e.PPID, os.Getppid())
		}
		if len(e.Argv) == 0 || e.Comm == "" {
			t.Errorf("entry %+v lacks argv or comm", e)
		}
		return
	}
	t.Fatalf("table of %d entries lacks pid %d: %+v", len(tab), os.Getpid(), tab)
}
