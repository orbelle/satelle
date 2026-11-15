package filebundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/config"
)

const Format = 1

type Entry struct {
	Path    string
	Content string
}

type bundle struct {
	Format int    `json:"format"`
	Files  []file `json:"files"`
}

type file struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

var ErrMalformed = errors.New("malformed bundle")

type MalformedError struct{ Err error }

func (e *MalformedError) Error() string        { return e.Err.Error() }
func (e *MalformedError) Unwrap() error        { return e.Err }
func (e *MalformedError) Is(target error) bool { return target == ErrMalformed }

type FormatError struct{ Got, Want int }

func (e *FormatError) Error() string {
	return fmt.Sprintf("unknown bundle format %d (this satelle reads %d)", e.Got, e.Want)
}

func Encode(entries []Entry) ([]byte, error) {
	b := bundle{Format: Format, Files: make([]file, 0, len(entries))}
	for _, e := range entries {
		if config.LocalOnlyPath(e.Path) {
			return nil, fmt.Errorf("%s: a .local file never enters a bundle", e.Path)
		}
		if !utf8.ValidString(e.Content) {
			return nil, fmt.Errorf("%s: not UTF-8 text", e.Path)
		}
		b.Files = append(b.Files, file{Path: e.Path, Content: e.Content})
	}
	return json.Marshal(b)
}

func Decode(data []byte, roots []string) ([]Entry, error) {
	var b bundle
	if !utf8.Valid(data) {
		return nil, &MalformedError{Err: errors.New("content is not UTF-8 text")}
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, &MalformedError{Err: err}
	}
	if b.Format != Format {
		return nil, &FormatError{Got: b.Format, Want: Format}
	}
	out := make([]Entry, 0, len(b.Files))
	for _, f := range b.Files {
		if err := CheckPath(f.Path, roots); err != nil {
			return nil, err
		}
		out = append(out, Entry{Path: f.Path, Content: f.Content})
	}
	return out, nil
}

func CheckPath(p string, roots []string) error {
	if p == "" || path.IsAbs(p) || strings.Contains(p, "\\") || path.Clean(p) != p {
		return fmt.Errorf("bundle path %q is not a plain relative path", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("bundle path %q escapes the bundle directory", p)
		}
	}
	if config.LocalOnlyPath(p) {
		return fmt.Errorf("bundle path %q is a .local file, which never travels", p)
	}
	if len(roots) == 0 {
		return nil
	}
	for _, root := range roots {
		if p == root || strings.HasPrefix(p, root+"/") {
			return nil
		}
	}
	return fmt.Errorf("bundle path %q is outside the bundle's roots", p)
}

func Write(dir string, entries []Entry, overwrite bool, owned func(rel string) bool) error {
	keep := map[string]bool{}
	for _, e := range entries {
		if err := CheckPath(e.Path, nil); err != nil {
			return err
		}
		keep[e.Path] = true
	}
	if existing, err := os.ReadDir(dir); err == nil && len(existing) > 0 && !overwrite {
		return fmt.Errorf("%s is not empty — pass --force to overwrite it", dir)
	}
	if overwrite && owned != nil {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if keep[rel] || config.LocalOnlyPath(rel) || !owned(rel) {
				return nil
			}
			return os.Remove(p)
		})
		if err != nil {
			return err
		}
	}
	for _, e := range entries {
		dest := filepath.Join(dir, filepath.FromSlash(e.Path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(e.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
