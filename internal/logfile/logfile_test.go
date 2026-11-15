package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppend_rollsAtMaxSizeAndPrunes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "operations.log")
	cfg := Config{MaxSizeBytes: 50, MaxFiles: 2}
	base := time.Now().UTC()
	for i := 0; i < 12; i++ {
		if err := Append(base.Add(time.Duration(i)*time.Second), path, cfg, "line-"+strings.Repeat("x", 15)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active log missing after rotation: %v", err)
	}
	rot, _ := filepath.Glob(filepath.Join(dir, "logs", "operations-*.log"))
	if len(rot) == 0 {
		t.Fatal("expected rotation to have occurred, got none")
	}
	if len(rot) > cfg.MaxFiles {
		t.Errorf("expected <= %d rotated files after pruning, got %d: %v", cfg.MaxFiles, len(rot), rot)
	}
}

func TestAppend_rollsOnNewDay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operations.log")
	cfg := Config{MaxFiles: 5}
	now := time.Date(2026, 7, 2, 1, 0, 0, 0, time.UTC)
	yesterday := now.AddDate(0, 0, -1)

	if err := Append(yesterday, path, cfg, "day1 line"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}
	if err := Append(now, path, cfg, "day2 line"); err != nil {
		t.Fatal(err)
	}

	rot, _ := filepath.Glob(filepath.Join(dir, "operations-*.log"))
	if len(rot) != 1 {
		t.Fatalf("expected 1 rotated file after a day change, got %d: %v", len(rot), rot)
	}
	if active, _ := os.ReadFile(path); strings.Contains(string(active), "day1") || !strings.Contains(string(active), "day2") {
		t.Errorf("active log should hold only day2 after the roll:\n%s", active)
	}
	if rolled, _ := os.ReadFile(rot[0]); !strings.Contains(string(rolled), "day1") {
		t.Errorf("rotated file should hold day1:\n%s", rolled)
	}
}

func TestAppend_noRollWithinDayUnderSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.log")
	cfg := Config{MaxSizeBytes: 1 << 20, MaxFiles: 3}
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if err := Append(base.Add(time.Duration(i)*time.Second), path, cfg, "small line"); err != nil {
			t.Fatal(err)
		}
	}
	if rot, _ := filepath.Glob(filepath.Join(dir, "reviewer-*.log")); len(rot) != 0 {
		t.Errorf("no roll expected within a day under the size cap, got %v", rot)
	}
	if b, _ := os.ReadFile(path); strings.Count(string(b), "small line") != 5 {
		t.Errorf("all 5 lines should remain in the active file:\n%s", b)
	}
}
