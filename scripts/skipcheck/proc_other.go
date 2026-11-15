//go:build !unix

package main

import (
	"os"
	"os/exec"
)

func ownGroup(*exec.Cmd) {}

func signalGroup(c *exec.Cmd, sig os.Signal) {
	if c.Process != nil {
		_ = c.Process.Signal(sig)
	}
}
