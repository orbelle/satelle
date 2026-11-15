//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func ownGroup(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func signalGroup(c *exec.Cmd, sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok && c.Process != nil {
		_ = syscall.Kill(-c.Process.Pid, s)
	}
}
