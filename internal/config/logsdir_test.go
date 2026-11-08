package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// LogsDir is the logs directory under the runtime directory it is given.
func TestLogsDirIsUnderTheRuntime(t *testing.T) {
	rt := filepath.Join(t.TempDir(), "runtime")
	if got, want := LogsDir(rt), filepath.Join(rt, "logs"); got != want {
		t.Fatalf("LogsDir(%q) = %q, want %q", rt, got, want)
	}
}

// LogsDir is the one owner of the logs path: no other non-test Go file joins
// "logs" onto a directory by hand.
func TestNoSecondOwnerOfTheLogsDir(t *testing.T) {
	join := regexp.MustCompile(`filepath\.Join\([^)]*"logs"\s*\)`)
	owner := filepath.Join("internal", "config", "runtime.go")
	var hits []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join("..", "..", top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(filepath.Join("..", ".."), path)
			if rel == owner {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range join.FindAllString(string(body), -1) {
				hits = append(hits, rel+": "+m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("the logs dir is derived outside config.LogsDir:\n%s", strings.Join(hits, "\n"))
	}
}
