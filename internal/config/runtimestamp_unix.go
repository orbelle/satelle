//go:build unix

package config

import (
	"io/fs"
	"syscall"
)

func extraLinks(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
