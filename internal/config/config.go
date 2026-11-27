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
	NodeRunEnv           = "SATELLE_NODE_RUN"
	NodeWithholdEnv      = "SATELLE_NODE_WITHHOLD"
	WorkerEnv            = "SATELLE_WORKER"
)

func SessionFromEnv() string { return strings.TrimSpace(os.Getenv(SessionEnv)) }

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
