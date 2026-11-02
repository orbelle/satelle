//go:build unix

package remotedef

import (
	"os"
	"syscall"
)

// privateToMe is true when info is mode 0600 and owned by this process's user.
func privateToMe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().Perm() == 0o600 && int(st.Uid) == os.Getuid()
}

// joinAlive is true when pid is a live process this user may signal: a join
// that has gone, or a pid since reused by another user, is not.
func joinAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}
