//go:build windows

package ledgerstore

import "os/exec"

func signalEnd(cmd *exec.Cmd) string { return "" }
