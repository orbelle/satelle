package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func InstanceID(globalDir string) string {
	abs, err := filepath.Abs(strings.TrimSpace(globalDir))
	if err != nil || abs == "" {
		abs = strings.TrimSpace(globalDir)
	}
	if abs == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return hex.EncodeToString(sum[:8])
}

func CurrentInstanceID() string {
	return InstanceID(GlobalDir())
}

func SafeCurrentInstanceID() string {
	if testing.Testing() && strings.TrimSpace(os.Getenv("SATELLE_HOME")) == "" {
		return ""
	}
	return CurrentInstanceID()
}
