//go:build windows

package procgroup

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func Ancestors(pid int) []Proc {
	parents := parentPIDs()
	var out []Proc
	_, childStart := ProbeIdentity(pid)
	for len(out) < maxAncestors {
		ppid, ok := parents[uint32(pid)]
		if !ok || ppid == 0 || int(ppid) == pid {
			return out
		}
		l, start := ProbeIdentity(int(ppid))
		if l != Alive || start == "" || (childStart != "" && newer(start, childStart)) {
			return out
		}
		out = append(out, Proc{PID: int(ppid), Start: start})
		pid, childStart = int(ppid), start
	}
	return out
}

func parentPIDs() map[uint32]uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	out := map[uint32]uint32{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		out[e.ProcessID] = e.ParentProcessID
	}
	return out
}

func newer(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}
