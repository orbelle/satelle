//go:build darwin || ios || freebsd || netbsd

package config

import "syscall"

func ctime(st *syscall.Stat_t) int64 { return st.Ctimespec.Nano() }
