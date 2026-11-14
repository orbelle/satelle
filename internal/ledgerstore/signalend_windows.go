//go:build windows

package ledgerstore

import "os/exec"

// signalEnd is empty on Windows, which has no signals or process groups; the
// caller then keeps the plain "a signal ended it" reason.
func signalEnd(cmd *exec.Cmd) string { return "" }
