package buildinfo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func installFile(t *testing.T, p, body string, mtime ...time.Time) os.FileInfo {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	tmp := f.Name()
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range mtime {
		if err := os.Chtimes(tmp, m, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRecordAndLookup(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	bin := filepath.Join(dir, "satelle")
	writeFile(t, bin, "one")
	line := "satelle 1.0.0 (commit a, built b)"

	if got := Lookup(home, bin); !strings.Contains(got, "has not run since it was installed") {
		t.Errorf("unrecorded file: %q", got)
	}
	if err := Record(home, bin, line); err != nil {
		t.Fatal(err)
	}
	if got := Lookup(home, bin); got != line {
		t.Errorf("Lookup = %q, want %q", got, line)
	}
	writeFile(t, bin, "two, longer")
	if got := Lookup(home, bin); !strings.Contains(got, "changed since it last ran") {
		t.Errorf("rewritten file: %q", got)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if got := Lookup(home, bin); !strings.HasPrefix(got, "unknown (no satelle at ") {
		t.Errorf("missing file: %q", got)
	}
	other := filepath.Join(dir, "satelled")
	writeFile(t, other, "d")
	if err := Record(home, other, "satelled 1.0.0 (commit a, built b)"); err != nil {
		t.Fatal(err)
	}
	m := readRecords(home)
	if _, ok := m[resolvePath(bin)]; ok || len(m) != 1 {
		t.Errorf("the removed file's entry was kept: %v", m)
	}
	writeFile(t, filepath.Join(home, RecordsFile), "{not json")
	if got := Lookup(home, other); !strings.Contains(got, "has not run since it was installed") {
		t.Errorf("torn record: %q", got)
	}
}

func TestRecordSelfWritesOnlyOnChange(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "satelle")
	writeFile(t, bin, "x")
	line := "satelle 1.0.0 (commit a, built b)"
	if err := recordIfChanged(home, bin, line); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(home, RecordsFile)
	st1, err := os.Stat(rec)
	if err != nil {
		t.Fatal(err)
	}
	past := st1.ModTime().Add(-time.Hour)
	if err := os.Chtimes(rec, past, past); err != nil {
		t.Fatal(err)
	}
	if err := recordIfChanged(home, bin, line); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(rec); !st2.ModTime().Equal(past) {
		t.Errorf("an unchanged entry rewrote %s", RecordsFile)
	}
	if err := recordIfChanged(home, bin, "satelle 1.0.1 (commit a, built b)"); err != nil {
		t.Fatal(err)
	}
	if got := Lookup(home, bin); got != "satelle 1.0.1 (commit a, built b)" {
		t.Errorf("a changed line was not recorded: %q", got)
	}
}

func TestIdentifyProbesOnceThenReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a shell script")
	}
	home := t.TempDir()
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	bin := filepath.Join(dir, "satelled")
	body := func(v string) string {
		return "#!/bin/sh\necho x >> " + count + "\necho 'satelled " + v + " (commit a, built b)'\n"
	}
	probes := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
	installFile(t, bin, body("1.0.0"))
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if got := Identify(ctx, home, bin); got != "satelled 1.0.0 (commit a, built b)" {
			t.Fatalf("Identify = %q", got)
		}
	}
	if n := probes(); n != 1 {
		t.Errorf("probed %d times, want once", n)
	}
	rec, ok := readRecords(home)[resolvePath(bin)]
	if !ok {
		t.Fatal("no record after Identify")
	}
	if len(body("1.0.1")) != len(body("1.0.0")) {
		t.Fatal("the rewritten body is not the same length")
	}
	st := installFile(t, bin, body("1.0.1"), rec.ModTime)
	if st.Size() != rec.Size || !st.ModTime().UTC().Equal(rec.ModTime) {
		t.Fatalf("on disk size %d mtime %v, recorded size %d mtime %v", st.Size(), st.ModTime(), rec.Size, rec.ModTime)
	}
	if got := Identify(ctx, home, bin); got != "satelled 1.0.1 (commit a, built b)" {
		t.Errorf("a rewritten binary was not probed again: %q", got)
	}
	if n := probes(); n != 2 {
		t.Errorf("probed %d times, want twice", n)
	}
}

func TestLookupOldRecordIsChanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a record without identity is the normal state on windows")
	}
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "satelled")
	st := installFile(t, bin, "#!/bin/sh\necho 'satelled 1.0.0 (commit a, built b)'\n")
	line := "satelled 1.0.0 (commit a, built b)"
	old := map[string]diskEntry{resolvePath(bin): {Line: line, Size: st.Size(), ModTime: st.ModTime().UTC()}}
	if err := writeRecords(home, old); err != nil {
		t.Fatal(err)
	}
	if got := Lookup(home, bin); !strings.Contains(got, "changed since it last ran") {
		t.Errorf("an old record was trusted: %q", got)
	}
	if got := Identify(context.Background(), home, bin); got != line {
		t.Fatalf("Identify = %q", got)
	}
	if got := Lookup(home, bin); got != line {
		t.Errorf("Lookup after re-record = %q, want %q", got, line)
	}
	if e := readRecords(home)[resolvePath(bin)]; e.ID == nil {
		t.Error("the re-recorded entry has no identity")
	}
}

func TestCLIBeside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	real := t.TempDir()
	writeFile(t, filepath.Join(real, "satelled"), "d")
	link := filepath.Join(t.TempDir(), "satelled")
	if err := os.Symlink(filepath.Join(real, "satelled"), link); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolvePath(real), "satelle")
	if got := CLIBeside(link); got != want {
		t.Errorf("CLIBeside(%s) = %q, want %q", link, got, want)
	}
}

func TestVersionOf(t *testing.T) {
	if v, ok := VersionOf("satelle 1.0.45 (commit a, built b)"); !ok || v != "1.0.45" {
		t.Errorf("identity line: %q %v", v, ok)
	}
	if v, ok := VersionOf("satelled 1.0.45 (commit a, built b)"); !ok || v != "1.0.45" {
		t.Errorf("daemon line: %q %v", v, ok)
	}
	if _, ok := VersionOf("unknown (no satelle at /x)"); ok {
		t.Error("an unknown line parsed")
	}
	if _, ok := VersionOf(""); ok {
		t.Error("an empty line parsed")
	}
}
