package provider

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUntarRefusesABackslashEscape(t *testing.T) {
	cases := map[string]string{
		"backslash dotdot": `ledger/..\..\escape`,
		"backslash only":   `ledger\..\..\escape`,
		"backslash inner":  `ledger/sub\x`,
	}
	if runtime.GOOS == "windows" {
		cases["drive letter"] = `ledger/C:/escape`
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := tw.WriteHeader(&tar.Header{Name: entry, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			host := filepath.Join(root, "in", "ledger")
			err := untar(&buf, "ledger", host)
			if err == nil || !strings.Contains(err.Error(), "outside ledger") {
				t.Fatalf("untar of %q = %v, want a refusal", entry, err)
			}
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				t.Fatalf("untar wrote %v", entries)
			}
		})
	}
}
