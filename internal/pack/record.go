package pack

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Record is .satelle/packs.toml: what satelle pack install placed, as published.
type Record struct {
	Packs []Installed `toml:"pack"`
}

// Installed is one installed pack: its ref, the version and digest it was
// installed at, the skill directories it placed, and each file's published sha256.
type Installed struct {
	Ref     string          `toml:"ref"`
	Name    string          `toml:"name"`
	Version string          `toml:"version"`
	Digest  string          `toml:"digest"`
	Skills  []string        `toml:"skills"`
	Files   []InstalledFile `toml:"file"`
}

// InstalledFile is one file of an installed pack, its path relative to the
// skills directory.
type InstalledFile struct {
	Path   string `toml:"path"`
	SHA256 string `toml:"sha256"`
}

// LoadRecord reads the record at path. A missing file is an empty record; a
// malformed one, or one with a key the record does not know, is an error naming path.
func LoadRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, nil
	}
	if err != nil {
		return Record{}, fmt.Errorf("%s: %w", path, err)
	}
	var r Record
	md, err := toml.Decode(string(data), &r)
	if err != nil {
		return Record{}, fmt.Errorf("%s: %w", path, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, len(un))
		for i, k := range un {
			keys[i] = k.String()
		}
		return Record{}, fmt.Errorf("%s: unknown key %s", path, strings.Join(keys, ", "))
	}
	return r, nil
}

// Find returns the entry installed under ref.
func (r Record) Find(ref string) (Installed, bool) {
	for _, p := range r.Packs {
		if p.Ref == ref {
			return p, true
		}
	}
	return Installed{}, false
}

// Owner returns the ref of the pack that placed the skill directory dir.
func (r Record) Owner(dir string) (string, bool) {
	for _, p := range r.Packs {
		if slices.Contains(p.Skills, dir) {
			return p.Ref, true
		}
	}
	return "", false
}

// With returns the record with in replacing any entry of the same ref.
func (r Record) With(in Installed) Record {
	out := Record{Packs: make([]Installed, 0, len(r.Packs)+1)}
	for _, p := range r.Packs {
		if p.Ref != in.Ref {
			out.Packs = append(out.Packs, p)
		}
	}
	out.Packs = append(out.Packs, in)
	return out
}

func (r Record) sorted() Record {
	out := Record{Packs: slices.Clone(r.Packs)}
	slices.SortFunc(out.Packs, func(a, b Installed) int { return strings.Compare(a.Ref, b.Ref) })
	for i := range out.Packs {
		files := slices.Clone(out.Packs[i].Files)
		slices.SortFunc(files, func(a, b InstalledFile) int { return strings.Compare(a.Path, b.Path) })
		out.Packs[i].Files = files
	}
	return out
}

func (r Record) encode() ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(r.sorted()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renameRecord commits a written record; a variable so a test can fail the commit.
var renameRecord = os.Rename

// writeRecord writes r to path through a temporary file in dir, renamed into place.
func writeRecord(dir, path string, r Record) error {
	data, err := r.encode()
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, ".packs-*.toml")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	name := tmp.Name()
	_, wErr := tmp.Write(data)
	cErr := tmp.Close()
	if err := errors.Join(wErr, cErr, os.Chmod(name, 0o644)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := renameRecord(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
