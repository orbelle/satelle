//go:build !windows

package ledgerstore

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// signalEnd describes how a signal ended cmd: "signal <NAME>, pid <pid>,
// process group <pgid>". It is empty when cmd has not been waited or exited
// on its own. The group is read from the SysProcAttr procgroup.Set wrote on
// cmd, since the reaped child can no longer be asked for it.
func signalEnd(cmd *exec.Cmd) string {
	ps := cmd.ProcessState
	if ps == nil {
		return ""
	}
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}
	name := unix.SignalName(ws.Signal())
	if name == "" {
		name = strconv.Itoa(int(ws.Signal()))
	}
	pid := ps.Pid()
	pgid := syscall.Getpgrp()
	if a := cmd.SysProcAttr; a != nil && a.Setpgid {
		pgid = pid
		if a.Pgid != 0 {
			pgid = a.Pgid
		}
	}
	return fmt.Sprintf("signal %s, pid %d, process group %d", name, pid, pgid)
}
