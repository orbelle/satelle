//go:build darwin

package procgroup

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// zombie is the kernel's SZOMB process state.
const zombie = 5

// osProbe is the signal-0 existence check plus the process's start time from the
// kernel's process table. A pid reused by another process has a different start
// time; a zombie can never record a result and is gone.
func osProbe(pid int) (Liveness, string) {
	if signalLiveness(pid) == Gone {
		return Gone, ""
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return Alive, "" // alive, but the process table will not say who it is
	}
	if kp.Proc.P_pid == 0 || kp.Proc.P_stat == zombie {
		return Gone, ""
	}
	t := kp.Proc.P_starttime
	return Alive, fmt.Sprintf("%d.%06d", t.Sec, t.Usec)
}
