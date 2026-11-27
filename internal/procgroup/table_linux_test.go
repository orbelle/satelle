//go:build linux

package procgroup

import (
	"os"
	"testing"
)

func TestTableHoldsThisProcess(t *testing.T) {
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
	t.Fatalf("table of %d entries lacks pid %d", len(tab), os.Getpid())
}
