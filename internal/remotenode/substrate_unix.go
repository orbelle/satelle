//go:build unix

package remotenode

import "golang.org/x/sys/unix"

func identity(p string) (statID, error) {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return statID{}, err
	}
	return statID{Dev: uint64(st.Dev), Ino: uint64(st.Ino), Nlink: uint64(st.Nlink), Ctime: st.Ctim.Nano()}, nil
}
