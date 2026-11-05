// Package config is satelle's machine-level configuration and paths: the
// global config under ~/.satelle, the home-keyed runtime directory of a repo,
// and the repo root walk. A repo's own settings (.satelle/satelle.toml) are the
// kernel's.
package config

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultDataDir is a repo's configuration directory, <repo>/.satelle.
	DefaultDataDir = ".satelle"
	// ConfigName is the repo settings file inside DefaultDataDir.
	ConfigName = "satelle.toml"
	// DefaultWebPort is satelled's listen port.
	DefaultWebPort = 8787
	// DefaultLogsMaxSizeKB caps each log file before it rolls; DefaultLogsMaxFiles
	// bounds how many rotations are kept.
	DefaultLogsMaxSizeKB = 5120
	DefaultLogsMaxFiles  = 7
	// SessionEnv names the session a CLI call acts for when no harness hook
	// input supplies one.
	SessionEnv = "SATELLE_SESSION"
)

// SessionFromEnv returns the trimmed SATELLE_SESSION value, or "".
func SessionFromEnv() string { return strings.TrimSpace(os.Getenv(SessionEnv)) }

// FindDataDir walks up from start (empty → CWD) for a .satelle directory that
// is not the machine-wide home, returning its path and whether one was found.
func FindDataDir(start string) (string, bool) {
	dir := strings.TrimSpace(start)
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		dir = cwd
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for {
		candidate := filepath.Join(dir, DefaultDataDir)
		if st, err := os.Stat(candidate); err == nil && st.IsDir() && !IsGlobalDir(candidate) {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// LocalOnlyPath reports whether a path must never be transmitted: any segment
// with a `.local` dot-component (satelle.local.toml, notes.local.md).
func LocalOnlyPath(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		parts := strings.Split(seg, ".")
		for _, c := range parts[1:] {
			if strings.EqualFold(c, "local") {
				return true
			}
		}
	}
	return false
}
