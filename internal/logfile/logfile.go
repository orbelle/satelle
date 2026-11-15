package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Config struct {
	MaxSizeBytes int64
	MaxFiles     int
}

var DefaultConfig = Config{MaxSizeBytes: 5 << 20, MaxFiles: 7}

func Append(now time.Time, path string, cfg Config, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		if shouldRoll(info, now, cfg, len(line)+1) {
			rotate(now, path)
			prune(path, cfg.MaxFiles)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintln(f, line)
	return err
}

func shouldRoll(info os.FileInfo, now time.Time, cfg Config, addBytes int) bool {
	if info.ModTime().UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		return true
	}
	if cfg.MaxSizeBytes > 0 && info.Size()+int64(addBytes) > cfg.MaxSizeBytes {
		return true
	}
	return false
}

func rotate(now time.Time, path string) {
	base := strings.TrimSuffix(path, filepath.Ext(path))
	ext := filepath.Ext(path)
	stamp := now.UTC().Format("20060102-150405")
	dest := base + "-" + stamp + ext
	for n := 1; ; n++ {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			break
		}
		dest = fmt.Sprintf("%s-%s.%d%s", base, stamp, n, ext)
	}
	_ = os.Rename(path, dest)
}

func prune(path string, maxFiles int) {
	if maxFiles <= 0 {
		return
	}
	base := strings.TrimSuffix(path, filepath.Ext(path))
	ext := filepath.Ext(path)
	matches, err := filepath.Glob(base + "-*" + ext)
	if err != nil || len(matches) <= maxFiles {
		return
	}
	sort.Slice(matches, func(i, j int) bool {
		fi, ei := os.Stat(matches[i])
		fj, ej := os.Stat(matches[j])
		if ei != nil || ej != nil {
			return matches[i] < matches[j]
		}
		return fi.ModTime().After(fj.ModTime())
	})
	for _, old := range matches[maxFiles:] {
		_ = os.Remove(old)
	}
}
