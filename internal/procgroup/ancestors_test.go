//go:build linux || darwin || windows

package procgroup

import (
	"os"
	"testing"
)

// On every platform satelle builds a dry run's read-only guard for, the chain
// of parents starts at this process's parent, with a start identity.
func TestAncestorsStartAtTheParent(t *testing.T) {
	anc := Ancestors(os.Getpid())
	if len(anc) == 0 {
		t.Fatal("Ancestors(self) is empty: a dry run's guard could not find its descendants here")
	}
	if anc[0].PID != os.Getppid() || anc[0].Start == "" {
		t.Fatalf("Ancestors(self)[0] = %+v, want the parent %d with its start identity", anc[0], os.Getppid())
	}
}
