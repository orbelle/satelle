package kernel

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed template
var template embed.FS

// Init writes the starter configuration into root (the .satelle directory),
// leaving every file that already exists alone, then validates it.
func Init(root string) error {
	err := fs.WalkDir(template, "template", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("template", path)
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if _, err := os.Stat(dst); err == nil {
			return nil
		}
		b, err := template.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, SkillsDir), 0o755); err != nil {
		return err
	}
	return errors.Join(Validate(root)...)
}
