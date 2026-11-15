//go:build unix

package procgroup

import (
	"errors"
	"syscall"
)

func signalLiveness(pid int) Liveness {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return Alive
	}
	return Gone
}
