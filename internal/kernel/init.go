package kernel

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:template
var template embed.FS

// InitPlan is the template files Init would write under root (the .satelle
// directory), slash-relative and sorted: every template file that is absent,
// except the starter workflows when root already has a satelle.toml, since a
// configured repo keeps its own routes (sto_3cf973ae, absorbing sto_7e839979).
// satelle doctor reports the same list; it is the one rule for what init adds.
func InitPlan(root string) ([]string, error) {
	configured := exists(filepath.Join(root, SettingsFile))
	var out []string
	err := fs.WalkDir(template, "template", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "template/")
		if configured && path.Dir(rel) == WorkflowsDir {
			return nil
		}
		if exists(filepath.Join(root, filepath.FromSlash(rel))) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Init writes the starter configuration into root (the .satelle directory):
// exactly the files InitPlan names, leaving every file that already exists
// alone, then validates it.
func Init(root string) error {
	plan, err := InitPlan(root)
	if err != nil {
		return err
	}
	for _, rel := range plan {
		b, err := template.ReadFile("template/" + rel)
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
