//go:build unix

package procgroup

import (
	"errors"
	"syscall"
)

// signalLiveness is the signal-0 existence check. EPERM means the process exists
// but belongs to someone else, which is alive for this purpose. It does not lean
// on /proc, so a run that died is noticed on every unix, not only Linux.
func signalLiveness(pid int) Liveness {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return Alive
	}
	return Gone
}
