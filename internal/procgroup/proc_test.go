//go:build linux || darwin

package procgroup

import (
	"os"
	"os/exec"
	"testing"
)

// The chain starts at this process's parent; this process is alive; a child
// that was killed and reaped is gone; and a running pid with another start
// identity is a reused pid, so gone (sto_e127b2e6 AC5).
func TestProbeAndAncestors(t *testing.T) {
	anc := Ancestors(os.Getpid())
	if len(anc) == 0 || anc[0].PID != os.Getppid() {
		t.Fatalf("Ancestors(self) = %+v, want the parent %d first", anc, os.Getppid())
	}
	if l := Probe(anc[0]); l != Alive {
		t.Errorf("the parent probes %v", l)
	}
	self := Self()
	if l := Probe(self); l != Alive {
		t.Fatalf("Probe(Self()) = %v, want alive", l)
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, start := ProbeIdentity(cmd.Process.Pid)
	child := Proc{PID: cmd.Process.Pid, Start: start}
	if l := Probe(child); l != Alive {
		t.Fatalf("a running child probes %v", l)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if l := Probe(child); l != Gone {
		t.Errorf("a killed child probes %v, want gone", l)
	}
	if l := Probe(Proc{PID: self.PID, Start: "x"}); l != Gone {
		t.Errorf("a reused pid probes %v, want gone", l)
	}
}
