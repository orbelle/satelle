//go:build !unix

package config

import "io/fs"

// FileIdentity reports no identity on a system without unix stat.
func FileIdentity(fs.FileInfo) (FileID, bool) { return FileID{}, false }
