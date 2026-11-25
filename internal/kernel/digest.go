package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/pack"
)

// SubstrateDigest hashes every file under root the kernel can load, the
// *.local.* overrides included, and skips only the kernel's own record
// folders (runs/). Symlinks are followed as the kernel's reads follow them:
// a link to a file is hashed by its target's content under the link's path,
// a link to a directory is walked under the link's path. A cycle or a
// non-regular target is an error.
func SubstrateDigest(root string) (string, error) {
	var entries []pack.DigestEntry
	if err := digestDir(root, "", map[string]bool{}, &entries); err != nil {
		return "", err
	}
	return pack.ComputeDigest(entries), nil
}

func digestDir(dir, rel string, open map[string]bool, out *[]pack.DigestEntry) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if open[real] {
		return fmt.Errorf("substrate digest: %s links back into itself", dir)
	}
	open[real] = true
	defer delete(open, real)
	des, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, de := range des {
		name := de.Name()
		if rel == "" && name == runsDir {
			continue
		}
		p := filepath.Join(dir, name)
		r := filepath.ToSlash(filepath.Join(rel, name))
		st, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("substrate digest: %w", err)
		}
		switch {
		case st.IsDir():
			if err := digestDir(p, r, open, out); err != nil {
				return err
			}
		case st.Mode().IsRegular():
			sum, err := fileSHA256(p)
			if err != nil {
				return err
			}
			*out = append(*out, pack.DigestEntry{Path: r, SHA256: sum})
		default:
			return fmt.Errorf("substrate digest: %s is not a regular file or directory", p)
		}
	}
	return nil
}

func fileSHA256(p string) (string, error) {
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
