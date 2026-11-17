package kernel

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

//go:embed all:template
var template embed.FS

func ShippedFiles() ([]string, error) {
	var out []string
	err := fs.WalkDir(template, "template", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out = append(out, strings.TrimPrefix(p, "template/"))
		return nil
	})
	sort.Strings(out)
	return out, err
}

func Shipped(rel string) ([]byte, error) {
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == "." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return nil, fmt.Errorf("%s: %w", rel, fs.ErrNotExist)
	}
	b, err := template.ReadFile("template/" + rel)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, fs.ErrNotExist)
	}
	return b, nil
}

func Configured(root string) bool { return exists(filepath.Join(root, SettingsFile)) }

func InitPlan(root string) ([]string, error) {
	configured := Configured(root)
	var removed []string
	if configured {
		removed = InitPolicy(root).Removed
	}
	files, err := ShippedFiles()
	var out []string
	for _, rel := range files {
		if configured && (path.Dir(rel) == WorkflowsDir || slices.Contains(removed, rel)) {
			continue
		}
		if exists(filepath.Join(root, filepath.FromSlash(rel))) {
			continue
		}
		out = append(out, rel)
	}
	return out, err
}

func Init(root string) error {
	if Configured(root) {
		return errors.Join(Validate(root)...)
	}
	plan, err := InitPlan(root)
	if err != nil {
		return err
	}
	for _, rel := range plan {
		b, err := Shipped(rel)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(root, SkillsDir), 0o755); err != nil {
		return err
	}
	return errors.Join(Validate(root)...)
}
