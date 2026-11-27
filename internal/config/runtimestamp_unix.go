//go:build unix

package config

import (
	"io/fs"
	"syscall"
)

func FileIdentity(info fs.FileInfo) (FileID, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileID{}, false
	}
	return FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino), Ctime: ctime(st), Nlink: uint64(st.Nlink)}, true
}
