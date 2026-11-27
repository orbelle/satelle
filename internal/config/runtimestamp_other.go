//go:build !unix

package config

import "io/fs"

func extraLinks(fs.FileInfo) bool { return false }
