//go:build windows

package cli

import "os"

// statIdentity has no portable dev+inode equivalent from a plain os.FileInfo on
// Windows; exe-identity verification is a Linux-only mechanism (the calling code
// already gates on runtime.GOOS), so this stub only needs to COMPILE here, never
// to succeed.
func statIdentity(info os.FileInfo) (dev, ino uint64, ok bool) {
	return 0, 0, false
}
