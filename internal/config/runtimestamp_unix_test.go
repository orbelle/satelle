//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func identityOf(t *testing.T, p string) (FileID, os.FileInfo) {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := FileIdentity(info)
	if !ok {
		t.Fatalf("FileIdentity(%s) reported nothing", p)
	}
	return id, info
}

func TestFileIdentity(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, info := identityOf(t, p)
	if first.Ino == 0 || first.Nlink != 1 || extraLinks(info) {
		t.Fatalf("FileIdentity of a fresh file = %+v", first)
	}
	if err := os.Link(p, filepath.Join(dir, "g")); err != nil {
		t.Fatal(err)
	}
	linked, info := identityOf(t, p)
	if linked.Nlink != 2 || !extraLinks(info) {
		t.Fatalf("FileIdentity after a hard link = %+v", linked)
	}
	if err := os.Remove(filepath.Join(dir, "g")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(p, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	rewritten, _ := identityOf(t, p)
	if rewritten.Ino != first.Ino || rewritten.Ctime <= first.Ctime {
		t.Fatalf("FileIdentity after a rewrite in place = %+v, before %+v", rewritten, first)
	}
	// The new file exists before the old is unlinked, so it cannot reuse its inode.
	next := filepath.Join(dir, "next")
	if err := os.WriteFile(next, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, p); err != nil {
		t.Fatal(err)
	}
	recreated, _ := identityOf(t, p)
	if recreated.Ino == rewritten.Ino {
		t.Fatalf("FileIdentity after unlink and recreate kept inode %d", recreated.Ino)
	}
}
