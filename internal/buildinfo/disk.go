package buildinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

const RecordsFile = "builds.json"

type diskEntry struct {
	Line    string         `json:"line"`
	Size    int64          `json:"size"`
	ModTime time.Time      `json:"mod_time"`
	ID      *config.FileID `json:"id,omitempty"`
}

func entryFor(line string, st os.FileInfo) diskEntry {
	e := diskEntry{Line: line, Size: st.Size(), ModTime: st.ModTime().UTC()}
	if id, ok := config.FileIdentity(st); ok {
		e.ID = &id
	}
	return e
}

func (e diskEntry) matches(st os.FileInfo) bool {
	if e.Size != st.Size() || !e.ModTime.Equal(st.ModTime().UTC()) {
		return false
	}
	id, ok := config.FileIdentity(st)
	if !ok {
		return e.ID == nil
	}
	return e.ID != nil && *e.ID == id
}

func ExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func Probe(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func VersionOf(line string) (string, bool) {
	f := strings.Fields(line)
	if len(f) < 2 || (f[0] != CLIName && f[0] != DaemonName) {
		return "", false
	}
	return f[1], true
}

func CLIBeside(daemonPath string) string {
	p := daemonPath
	if r, err := filepath.EvalSymlinks(daemonPath); err == nil {
		p = r
	}
	return filepath.Join(filepath.Dir(p), CLIName+ExeSuffix())
}

func resolvePath(path string) string {
	p := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		p = r
	}
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	return p
}

func readRecords(home string) map[string]diskEntry {
	m := map[string]diskEntry{}
	b, err := os.ReadFile(filepath.Join(home, RecordsFile))
	if err != nil {
		return m
	}
	if json.Unmarshal(b, &m) != nil {
		return map[string]diskEntry{}
	}
	return m
}

func writeRecords(home string, m map[string]diskEntry) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(home, RecordsFile+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(home, RecordsFile)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func Record(home, path, line string) error {
	p := resolvePath(path)
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	m := readRecords(home)
	for k := range m {
		if _, err := os.Stat(k); err != nil {
			delete(m, k)
		}
	}
	m[p] = entryFor(line, st)
	return writeRecords(home, m)
}

func recordIfChanged(home, path, line string) error {
	p := resolvePath(path)
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if e, ok := readRecords(home)[p]; ok && e.Line == line && e.matches(st) {
		return nil
	}
	return Record(home, p, line)
}

func RecordSelf(home string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return recordIfChanged(home, self, Resolve().String())
}

func Lookup(home, path string) string {
	p := resolvePath(path)
	st, err := os.Stat(p)
	if err != nil {
		return fmt.Sprintf("unknown (no %s at %s)", filepath.Base(path), path)
	}
	e, ok := readRecords(home)[p]
	switch {
	case !ok:
		return fmt.Sprintf("unknown (%s has not run since it was installed)", p)
	case !e.matches(st):
		return fmt.Sprintf("unknown (%s changed since it last ran)", p)
	}
	return e.Line
}

func Known(line string) bool {
	return line != "" && !strings.HasPrefix(line, "unknown (")
}

func Identify(ctx context.Context, home, path string) string {
	if line := Lookup(home, path); Known(line) {
		return line
	}
	line, err := Probe(ctx, path)
	if err != nil {
		return fmt.Sprintf("unknown (%v)", err)
	}
	_ = Record(home, path, line)
	return line
}
