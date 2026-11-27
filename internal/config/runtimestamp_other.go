//go:build !unix

package config

import "io/fs"

func FileIdentity(fs.FileInfo) (FileID, bool) { return FileID{}, false }
