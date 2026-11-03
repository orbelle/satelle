package help

import (
	"io/fs"
	"strings"
	"testing"
)

// satelle-vault's own sealed store is the only secrets backend (satelle-server
// sty_02359fe3, sty_80f756b7): no embedded help topic may tell a user to enter a
// value in Infisical.
func TestEmbeddedTopicsNameNoInfisical(t *testing.T) {
	scanned := 0
	err := fs.WalkDir(topicsFS, "topics", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(topicsFS, path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(strings.ToLower(line), "infisical") {
				t.Errorf("%s:%d names Infisical: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("scanned no embedded help topics")
	}
}
