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
	// A rewritten file no longer matches its entry.
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
	// The missing file's entry is pruned on the next Record.
	other := filepath.Join(dir, "satelled")
	writeFile(t, other, "d")
	if err := Record(home, other, "satelled 1.0.0 (commit a, built b)"); err != nil {
		t.Fatal(err)
	}
	m := readRecords(home)
	if _, ok := m[resolvePath(bin)]; ok || len(m) != 1 {
		t.Errorf("the removed file's entry was kept: %v", m)
	}
	// A torn record reads as none.
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
	writeFile(t, bin, "#!/bin/sh\necho x >> "+count+"\necho 'satelled 1.0.0 (commit a, built b)'\n")
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if got := Identify(ctx, home, bin); got != "satelled 1.0.0 (commit a, built b)" {
			t.Fatalf("Identify = %q", got)
		}
	}
	if b, _ := os.ReadFile(count); strings.Count(string(b), "x") != 1 {
		t.Errorf("probed %d times, want once", strings.Count(string(b), "x"))
	}
	writeFile(t, bin, "#!/bin/sh\necho x >> "+count+"\necho 'satelled 1.0.1 (commit a, built b)'\n")
	if got := Identify(ctx, home, bin); got != "satelled 1.0.1 (commit a, built b)" {
		t.Errorf("a rewritten binary was not probed again: %q", got)
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
