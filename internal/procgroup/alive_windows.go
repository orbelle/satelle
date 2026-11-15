//go:build windows

package procgroup

import (
	"errors"
	"strconv"

	"golang.org/x/sys/windows"
)

const stillActive = 259

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
