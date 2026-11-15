//go:build windows

package procgroup

import "os/exec"

func Set(_ *exec.Cmd) {}

func Kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
