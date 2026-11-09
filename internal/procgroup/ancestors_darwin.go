//go:build darwin

package procgroup

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Ancestors is pid's chain of parents, nearest first, excluding pid itself and
// launchd, at most maxAncestors long; each with its start identity.
func Ancestors(pid int) []Proc {
	var out []Proc
	for len(out) < maxAncestors {
		kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err != nil {
			return out
		}
		ppid := int(kp.Eproc.Ppid)
		if ppid <= 1 {
			return out
		}
		pp, err := unix.SysctlKinfoProc("kern.proc.pid", ppid)
		if err != nil {
			return out
		}
		t := pp.Proc.P_starttime
		out = append(out, Proc{PID: ppid, Start: fmt.Sprintf("%d.%06d", t.Sec, t.Usec)})
		pid = ppid
	}
	return out
}
