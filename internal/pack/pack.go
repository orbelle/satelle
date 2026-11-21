package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

type DigestEntry struct {
	Path   string
	SHA256 string
}

type verifiedFile struct {
	path       string
	sha256     string
	executable bool
	body       []byte
}

type Verified struct {
	name    string
	version string
	digest  string
	files   []verifiedFile
}

func (v Verified) Name() string    { return v.name }
func (v Verified) Version() string { return v.version }
func (v Verified) Digest() string  { return v.digest }

func (v Verified) Paths() []string {
	out := make([]string, len(v.files))
	for i, f := range v.files {
		out[i] = f.path
	}
	return out
}

func ValidatePath(p string) error {
	if p == "" {
		return fmt.Errorf("pack path %q is empty", p)
	}
	if strings.ContainsAny(p, "\x00\r\n") {
		return fmt.Errorf("pack path %q contains control character", p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("pack path %q is absolute", p)
	}
	if len(p) >= 2 && isASCIIAlpha(p[0]) && p[1] == ':' {
		return fmt.Errorf("pack path %q is absolute", p)
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("pack path %q must use '/' separators", p)
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Errorf("pack path %q has trailing '/'", p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return fmt.Errorf("pack path %q has empty segment", p)
		case ".":
			return fmt.Errorf("pack path %q contains '.' segment", p)
		case "..":
			return fmt.Errorf("pack path %q contains '..' segment", p)
		}
	}
	return nil
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func ComputeDigest(entries []DigestEntry) string {
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b DigestEntry) int {
		return strings.Compare(a.Path, b.Path)
	})
	h := sha256.New()
	for _, e := range sorted {
		h.Write([]byte(e.SHA256))
		h.Write([]byte("  "))
		h.Write([]byte(e.Path))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func Verify(bundle *satellepb.PackBundle) (Verified, error) {
	if bundle == nil || bundle.GetVersion() == nil || bundle.GetVersion().GetManifest() == nil {
		return Verified{}, errors.New("pack bundle is missing manifest")
	}
	manifest := bundle.GetVersion().GetManifest()
	if strings.TrimSpace(manifest.GetVersion()) == "" {
		return Verified{}, errors.New("pack manifest is missing version")
	}

	manifestFiles := make(map[string]*satellepb.PackFileRef, len(manifest.GetFiles()))
	manifestPaths := make([]string, 0, len(manifest.GetFiles()))
	for _, ref := range manifest.GetFiles() {
		if ref == nil {
			return Verified{}, errors.New("pack manifest contains nil file entry")
		}
		p := ref.GetPath()
		if err := ValidatePath(p); err != nil {
			return Verified{}, err
		}
		if _, dup := manifestFiles[p]; dup {
			return Verified{}, fmt.Errorf("duplicate pack path %q in manifest", p)
		}
		manifestFiles[p] = ref
		manifestPaths = append(manifestPaths, p)
	}

	bundleFiles := make(map[string]*satellepb.PackFile, len(bundle.GetFiles()))
	for _, f := range bundle.GetFiles() {
		if f == nil {
			return Verified{}, errors.New("pack bundle contains nil file entry")
		}
		p := f.GetPath()
		if err := ValidatePath(p); err != nil {
			return Verified{}, err
		}
		if _, dup := bundleFiles[p]; dup {
			return Verified{}, fmt.Errorf("duplicate pack path %q in bundle", p)
		}
		bundleFiles[p] = f
	}

	slices.Sort(manifestPaths)
	for _, p := range manifestPaths {
		for idx := strings.IndexByte(p, '/'); idx != -1; {
			dir := p[:idx]
			if _, exists := manifestFiles[dir]; exists {
				return Verified{}, fmt.Errorf("pack path collision between %q and %q", dir, p)
			}
			next := strings.IndexByte(p[idx+1:], '/')
			if next == -1 {
				break
			}
			idx += 1 + next
		}
	}

	for _, p := range manifestPaths {
		if _, ok := bundleFiles[p]; !ok {
			return Verified{}, fmt.Errorf("pack file %q listed in manifest is missing from bundle", p)
		}
	}
	var extra []string
	for p := range bundleFiles {
		if _, ok := manifestFiles[p]; !ok {
			extra = append(extra, p)
		}
	}
	if len(extra) > 0 {
		slices.Sort(extra)
		return Verified{}, fmt.Errorf("pack file %q in bundle is not in manifest", extra[0])
	}

	entries := make([]DigestEntry, 0, len(manifestPaths))
	files := make([]verifiedFile, 0, len(manifestPaths))
	for _, p := range manifestPaths {
		ref := manifestFiles[p]
		f := bundleFiles[p]
		body := f.GetBody()
		sum := sha256.Sum256(body)
		actual := hex.EncodeToString(sum[:])
		if f.GetSha256() != "" && f.GetSha256() != actual {
			return Verified{}, fmt.Errorf("pack file %q sha256 mismatch: file %s, computed %s", p, f.GetSha256(), actual)
		}
		if ref.GetSha256() != actual {
			return Verified{}, fmt.Errorf("pack file %q sha256 mismatch: manifest %s, computed %s", p, ref.GetSha256(), actual)
		}
		if ref.GetSize() < 0 || (ref.GetSize() != 0 && ref.GetSize() != int64(len(body))) {
			return Verified{}, fmt.Errorf("pack file %q size mismatch: manifest %d, actual %d", p, ref.GetSize(), len(body))
		}
		entries = append(entries, DigestEntry{Path: p, SHA256: actual})
		files = append(files, verifiedFile{
			path:       p,
			sha256:     actual,
			executable: ref.GetExecutable(),
			body:       slices.Clone(body),
		})
	}

	gotDigest := ComputeDigest(entries)
	if manifest.GetDigest() != gotDigest {
		return Verified{}, fmt.Errorf("pack digest mismatch: manifest %s, computed %s", manifest.GetDigest(), gotDigest)
	}

	return Verified{
		name:    manifest.GetName(),
		version: manifest.GetVersion(),
		digest:  gotDigest,
		files:   files,
	}, nil
}

func Write(dir string, v Verified) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("target directory is required")
	}
	if v.version == "" || v.digest == "" {
		return errors.New("pack is not verified")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve directory %q: %w", dir, err)
	}
	existed := false
	info, err := os.Lstat(absDir)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target directory %q: symlink is not allowed", dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("target directory %q: is not a directory", dir)
		}
		entries, rErr := os.ReadDir(absDir)
		if rErr != nil {
			return fmt.Errorf("read target directory %q: %w", dir, rErr)
		}
		if len(entries) > 0 {
			return fmt.Errorf("target directory %q: directory is not empty", dir)
		}
		existed = true
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(absDir), 0o755); err != nil {
			return fmt.Errorf("create parent directory for %q: %w", dir, err)
		}
	default:
		return fmt.Errorf("stat target directory %q: %w", dir, err)
	}

	stage, err := os.MkdirTemp(filepath.Dir(absDir), ".satelle-pack-*")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(stage)

	for _, f := range v.files {
		dest := filepath.Join(stage, filepath.FromSlash(f.path))
		rel, err := filepath.Rel(stage, dest)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("pack path %q escapes target directory", f.path)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create directory for %q: %w", f.path, err)
		}
		mode := os.FileMode(0o644)
		if f.executable {
			mode = 0o755
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return fmt.Errorf("create %q: %w", f.path, err)
		}
		_, wErr := out.Write(f.body)
		cErr := out.Close()
		if wErr != nil {
			return fmt.Errorf("write %q: %w", f.path, wErr)
		}
		if cErr != nil {
			return fmt.Errorf("close %q: %w", f.path, cErr)
		}
	}

	if !existed {
		if err := os.Chmod(stage, 0o755); err != nil {
			return fmt.Errorf("chmod staging directory: %w", err)
		}
		if err := os.Rename(stage, absDir); err != nil {
			return fmt.Errorf("commit pack to %q: %w", dir, err)
		}
		return nil
	}

	stagedEntries, err := os.ReadDir(stage)
	if err != nil {
		return fmt.Errorf("read staging directory: %w", err)
	}
	var moved []string
	for _, entry := range stagedEntries {
		src := filepath.Join(stage, entry.Name())
		dst := filepath.Join(absDir, entry.Name())
		if err := os.Rename(src, dst); err != nil {
			for _, m := range moved {
				_ = os.RemoveAll(m)
			}
			return fmt.Errorf("commit %q to %q: %w", entry.Name(), dir, err)
		}
		moved = append(moved, dst)
	}
	return nil
}
