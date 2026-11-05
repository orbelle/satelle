package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoTidyHintWordingInGo(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), "Fix: satelle story tidy") {
			t.Errorf("%s compiles the tidy hint wording into Go; it belongs in the reviewer skill", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
