package logsread

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFormatDispatchName(t *testing.T) {
	name := FormatDispatchName("reviewer", "sty_abc", 99)
	agent, story, nano, ok := ParseDispatchName(name)
	if !ok || agent != "reviewer" || story != "sty_abc" || nano != 99 {
		t.Fatalf("parse %q = %s %s %d %v", name, agent, story, nano, ok)
	}
}

func TestSelectNewestRole(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "dispatch")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(agent, story string, nano int64, body string) {
		p := filepath.Join(d, FormatDispatchName(agent, story, nano))
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	for i := 1; i <= 60; i++ {
		b.WriteString(strings.Repeat("x", 1) + "\n")
	}
	write("reviewer", "sty_x", 10, "old\n")
	write("reviewer", "sty_x", 50, b.String())
	write("planner", "sty_x", 90, "decoy\n")
	f, ok, err := Select(dir, "sty_x", "reviewer")
	if err != nil || !ok {
		t.Fatalf("select: %v %v", ok, err)
	}
	if !strings.HasSuffix(f.Path, FormatDispatchName("reviewer", "sty_x", 50)) {
		t.Fatalf("got %s", f.Path)
	}
	raw, _ := os.ReadFile(f.Path)
	lines := LastNLines(string(raw), 50)
	if len(lines) != 50 {
		t.Fatalf("tail %d", len(lines))
	}
}

func TestLatestDispatchAtOrBefore(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "dispatch")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, FormatDispatchName("planner", "sty_x", 100)), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(d, FormatDispatchName("planner", "sty_x", 200)), []byte("b"), 0o644)
	f, ok, err := LatestDispatchAtOrBefore(dir, "sty_x", "planner", time.Unix(0, 150))
	if err != nil || !ok || f.Nano != 100 {
		t.Fatalf("got %+v ok=%v err=%v", f, ok, err)
	}
}

// DispatchPath names a log ListDispatch finds; PruneDispatch keeps the newest.
func TestDispatchPathAndPrune(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dispatch"), 0o700); err != nil {
		t.Fatal(err)
	}
	for n := int64(1); n <= 5; n++ {
		if err := os.WriteFile(DispatchPath(dir, "reviewer-alt", "sto_1", n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneDispatch(dir, 3); err != nil {
		t.Fatal(err)
	}
	files, err := ListDispatch(dir)
	if err != nil || len(files) != 3 || files[0].Nano != 5 || files[2].Nano != 3 || files[0].Agent != "reviewer-alt" {
		t.Fatalf("%v %+v", err, files)
	}
	if err := PruneDispatch(t.TempDir(), 3); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
}
