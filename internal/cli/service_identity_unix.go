//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// statIdentity extracts device+inode from a Unix os.FileInfo (Linux/macOS both
// expose *syscall.Stat_t via Sys()). ok is false when the underlying Sys() value
// is not a Stat_t (should not happen on a real Unix os.Stat result).
func statIdentity(info os.FileInfo) (dev, ino uint64, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
