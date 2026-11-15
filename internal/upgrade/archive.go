package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/bobmcallan/satelle/internal/kernel"
)

type Entry struct {
	Path   string
	Mode   fs.FileMode
	Size   int64
	SHA256 string
	Link   string
}

func Manifest(root string) ([]Entry, error) {
	var out []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		e := Entry{Path: filepath.ToSlash(rel), Mode: info.Mode()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if e.Link, err = os.Readlink(p); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			e.Size = info.Size()
			if e.SHA256, err = fileSHA(p); err != nil {
				return err
			}
		}
		out = append(out, e)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sameManifest(a, b []Entry) error {
	if len(a) != len(b) {
		return fmt.Errorf("%d entries, want %d", len(b), len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			return fmt.Errorf("%s differs", a[i].Path)
		}
	}
	return nil
}

var rename = os.Rename

var copyHook = func(dst string) {}

type Archived struct {
	Source, Dest string
	Markers      []string
	Files        int
}

func Archive(dataDir, runtime string, now time.Time, dryRun bool) (Archived, error) {
	a := Archived{Source: dataDir, Markers: kernel.LegacyMarkers(dataDir)}
	if len(a.Markers) == 0 {
		return a, fmt.Errorf("%s is not a pre-1.0 .satelle (none of its marker files exists); nothing was archived", dataDir)
	}
	a.Dest = filepath.Join(runtime, "archive", now.UTC().Format("20060102T150405Z"), filepath.Base(dataDir))
	if _, err := os.Lstat(a.Dest); err == nil {
		return a, fmt.Errorf("archive destination %s exists; nothing was archived", a.Dest)
	}
	before, err := Manifest(dataDir)
	if err != nil {
		return a, err
	}
	a.Files = len(before)
	if dryRun {
		return a, nil
	}
	if err := os.MkdirAll(filepath.Dir(a.Dest), 0o755); err != nil {
		return a, err
	}
	if err := rename(dataDir, a.Dest); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			return a, err
		}
		if err := copyTree(dataDir, a.Dest); err != nil {
			return a, fmt.Errorf("copy to %s: %w (the source is untouched)", a.Dest, err)
		}
		copyHook(a.Dest)
		after, err := Manifest(a.Dest)
		if err != nil {
			return a, err
		}
		if err := sameManifest(before, after); err != nil {
			return a, fmt.Errorf("the copy at %s does not match the source: %v (the source is untouched)", a.Dest, err)
		}
		if err := os.RemoveAll(dataDir); err != nil {
			return a, err
		}
	}
	after, err := Manifest(a.Dest)
	if err != nil {
		return a, err
	}
	if err := sameManifest(before, after); err != nil {
		return a, fmt.Errorf("the archive at %s does not match the source: %v", a.Dest, err)
	}
	return a, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case info.IsDir():
			return os.MkdirAll(to, info.Mode().Perm())
		case info.Mode().IsRegular():
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			return os.Chmod(to, info.Mode().Perm())
		}
		return nil
	})
}
