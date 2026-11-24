package config

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultDataDir       = ".satelle"
	ConfigName           = "satelle.toml"
	DefaultWebPort       = 8787
	DefaultLogsMaxSizeKB = 5120
	DefaultLogsMaxFiles  = 7
	SessionEnv           = "SATELLE_SESSION"
	ProjectDirEnv        = "SATELLE_PROJECT_DIR"
	SubstrateDirEnv      = "SATELLE_SUBSTRATE_DIR"
	// WorkerEnv names the worker role a dispatched child runs as. The value
	// is the writing process's self-report, used only for an event origin's
	// role; it is provenance, not an authorisation fact.
	WorkerEnv = "SATELLE_WORKER"
)

func SessionFromEnv() string { return strings.TrimSpace(os.Getenv(SessionEnv)) }

// WorkerFromEnv returns the worker role this process was dispatched as, if any.
func WorkerFromEnv() string { return strings.TrimSpace(os.Getenv(WorkerEnv)) }

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
