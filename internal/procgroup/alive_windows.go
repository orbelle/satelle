//go:build windows

package procgroup

import (
	"errors"
	"strconv"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess reports for a running process.
// A process that really exited with code 259 is indistinguishable by that alone;
// the creation time recorded with the pid, and the gate's own timeout, bound it.
const stillActive = 259

// osProbe opens the process and asks whether it has exited. A pid that names no
// process is gone; a process this user may not inspect is Unknown, which a
// session is never held on. The creation time is the identity: a pid reused by
// another process has a different one.
func osProbe(pid int) (Liveness, string) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return Gone, ""
		}
		return Unknown, ""
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return Unknown, ""
	}
	if code != stillActive {
		return Gone, ""
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return Alive, ""
	}
	return Alive, strconv.FormatUint(uint64(created.HighDateTime)<<32|uint64(created.LowDateTime), 10)
}
