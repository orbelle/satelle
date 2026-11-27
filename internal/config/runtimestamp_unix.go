//go:build unix

package config

import (
	"io/fs"
	"syscall"
)

// FileIdentity is a file's identity as the OS gives it, from an Lstat or Stat
// result: device, inode, change time in nanoseconds and link count. Writing,
// replacing, renaming, linking or chmodding an entry changes it, and no user
// can set its change time back.
func FileIdentity(info fs.FileInfo) (FileID, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileID{}, false
	}
	return FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino), Ctime: st.Ctim.Nano(), Nlink: uint64(st.Nlink)}, true
}
