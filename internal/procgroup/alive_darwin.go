//go:build darwin

package procgroup

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const zombie = 5

func osProbe(pid int) (Liveness, string) {
	if signalLiveness(pid) == Gone {
		return Gone, ""
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return Alive, ""
	}
	if kp.Proc.P_pid == 0 || kp.Proc.P_stat == zombie {
		return Gone, ""
	}
	t := kp.Proc.P_starttime
	return Alive, fmt.Sprintf("%d.%06d", t.Sec, t.Usec)
}
