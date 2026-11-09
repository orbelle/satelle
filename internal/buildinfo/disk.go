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
)

// RecordsFile is the machine's record of the builds on disk, under the satelle
// home (the caller passes config.GlobalDir()). Each binary records its own
// identity line when it runs, and 'satelle update' records what it installs, so
// satelled can name the installed builds without running them (it never execs).
const RecordsFile = "builds.json"

// diskEntry is one binary's recorded identity, with the size and modification
// time of the file it was recorded for: an entry counts only while the file on
// disk still matches it.
type diskEntry struct {
	Line    string    `json:"line"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// ExeSuffix is the platform's executable suffix: ".exe" on Windows, else empty.
func ExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// Probe runs `path --version` and returns the identity line it prints. CLI only:
// satelled never execs.
func Probe(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// VersionOf is the version field of an identity line ("<name> <version> (…)"),
// only when the line names satelle or satelled, so an "unknown (…)" line never
// parses.
func VersionOf(line string) (string, bool) {
	f := strings.Fields(line)
	if len(f) < 2 || (f[0] != CLIName && f[0] != DaemonName) {
		return "", false
	}
	return f[1], true
}

// CLIBeside is the satelle CLI installed beside the satelled at daemonPath, the
// layout 'satelle update' maintains. A path that does not resolve is used as is.
func CLIBeside(daemonPath string) string {
	p := daemonPath
	if r, err := filepath.EvalSymlinks(daemonPath); err == nil {
		p = r
	}
	return filepath.Join(filepath.Dir(p), CLIName+ExeSuffix())
}

// resolvePath is path with its symlinks resolved and made absolute; the record
// is keyed by it.
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
		return map[string]diskEntry{} // a torn or unparsable record reads as none
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

// Record stores line as the identity of the binary at path, with the file's
// current size and modification time. It re-reads the record first, so
// concurrent writers merge, and drops entries whose file no longer exists.
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
	m[p] = diskEntry{Line: line, Size: st.Size(), ModTime: st.ModTime().UTC()}
	return writeRecords(home, m)
}

// recordIfChanged records line for path only when the stored entry's line, size
// or modification time differs, so a steady-state run costs one stat and one
// small read.
func recordIfChanged(home, path, line string) error {
	p := resolvePath(path)
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if e, ok := readRecords(home)[p]; ok && e.Line == line && e.Size == st.Size() && e.ModTime.Equal(st.ModTime().UTC()) {
		return nil
	}
	return Record(home, p, line)
}

// RecordSelf records this process's own build for its executable on disk.
func RecordSelf(home string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return recordIfChanged(home, self, Resolve().String())
}

// Lookup is the recorded identity of the binary at path, when the file on disk
// still matches the entry. It never execs: otherwise it says why it cannot name
// the build.
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
	case e.Size != st.Size() || !e.ModTime.Equal(st.ModTime().UTC()):
		return fmt.Sprintf("unknown (%s changed since it last ran)", p)
	}
	return e.Line
}

// Known reports whether a Lookup line names a build (it is not "unknown (…)").
func Known(line string) bool {
	return line != "" && !strings.HasPrefix(line, "unknown (")
}

// Identify is the identity of the binary at path: the record when it is current,
// else a probe of the binary, which is then recorded. CLI only.
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
