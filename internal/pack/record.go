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

type Record struct {
	Packs []Installed `toml:"pack"`
}

type Installed struct {
	Ref     string          `toml:"ref"`
	Name    string          `toml:"name"`
	Version string          `toml:"version"`
	Digest  string          `toml:"digest"`
	Skills  []string        `toml:"skills"`
	Files   []InstalledFile `toml:"file"`
}

type InstalledFile struct {
	Path   string `toml:"path"`
	SHA256 string `toml:"sha256"`
}

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

func (in Installed) Validate() error {
	if err := ValidateSegment(in.Version); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	skills := map[string]bool{}
	for _, s := range in.Skills {
		if err := ValidateSegment(s); err != nil {
			return fmt.Errorf("skills: %w", err)
		}
		if skills[s] {
			return fmt.Errorf("skills: %q appears twice", s)
		}
		skills[s] = true
	}
	paths := map[string]bool{}
	for _, f := range in.Files {
		if err := ValidatePath(f.Path); err != nil {
			return fmt.Errorf("file: %w", err)
		}
		if paths[f.Path] {
			return fmt.Errorf("file %q appears twice", f.Path)
		}
		paths[f.Path] = true
		if top, _, _ := strings.Cut(f.Path, "/"); !skills[top] {
			return fmt.Errorf("file %q is not in a recorded skill directory", f.Path)
		}
		if !isSHA256(f.SHA256) {
			return fmt.Errorf("file %q: sha256 %q is not 64 lowercase hex characters", f.Path, f.SHA256)
		}
	}
	return nil
}

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (r Record) Find(ref string) (Installed, bool) {
	for _, p := range r.Packs {
		if p.Ref == ref {
			return p, true
		}
	}
	return Installed{}, false
}

func (r Record) Owner(dir string) (string, bool) {
	for _, p := range r.Packs {
		if slices.Contains(p.Skills, dir) {
			return p.Ref, true
		}
	}
	return "", false
}

func (r Record) With(in Installed) Record {
	out := r.Without(in.Ref)
	out.Packs = append(out.Packs, in)
	return out
}

func (r Record) Without(ref string) Record {
	out := Record{Packs: make([]Installed, 0, len(r.Packs))}
	for _, p := range r.Packs {
		if p.Ref != ref {
			out.Packs = append(out.Packs, p)
		}
	}
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

var renameRecord = os.Rename

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
