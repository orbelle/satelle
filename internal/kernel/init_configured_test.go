package kernel

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func treeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			m[p] = "dir"
			return nil
		}
		b, _ := os.ReadFile(p)
		m[p] = string(b)
		return nil
	})
	return m
}

func TestInitWritesNothingIntoAConfiguredRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, SettingsFile), "[workflows]\ndefault = \"delivery\"\n")
	write(t, filepath.Join(root, WorkflowsDir, "delivery.toml"), "name = \"delivery\"\n\n[states.backlog]\nstart = true\n\n[states.done]\nterminal = true\n\n[[transition]]\nfrom = [\"backlog\"]\nto = \"done\"\n")
	before := treeDigest(t, root)
	plan, _ := InitPlan(root)
	_ = Init(root)
	after := treeDigest(t, root)
	if len(before) != len(after) {
		t.Fatalf("init changed a configured root: %d entries before, %d after", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("init changed %s", k)
		}
	}
	if !slices.Contains(plan, SessionFile) || !slices.Contains(plan, AgentsFile) {
		t.Errorf("InitPlan lacks the missing files: %v", plan)
	}
	if _, err := os.Stat(filepath.Join(root, SkillsDir)); !os.IsNotExist(err) {
		t.Errorf("init made skills/ in a configured root: %v", err)
	}
}

func TestShippedIsTheTemplateSource(t *testing.T) {
	disk := map[string][]byte{}
	var listing []string
	err := filepath.WalkDir("template", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel("template", p)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		disk[rel] = b
		listing = append(listing, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(listing)
	got, err := ShippedFiles()
	if err != nil || !slices.Equal(got, listing) {
		t.Fatalf("ShippedFiles = %v (%v), want %v", got, err, listing)
	}
	for _, rel := range listing {
		if b, err := Shipped(rel); err != nil || string(b) != string(disk[rel]) {
			t.Errorf("Shipped(%s) differs from the source (%v)", rel, err)
		}
	}
	root := filepath.Join(t.TempDir(), ".satelle")
	if plan, _ := InitPlan(root); !slices.Equal(plan, listing) {
		t.Errorf("InitPlan of an empty root = %v, want %v", plan, listing)
	}
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	for _, rel := range listing {
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); err != nil || string(b) != string(disk[rel]) {
			t.Errorf("first init wrote %s unlike the source (%v)", rel, err)
		}
	}
	for _, bad := range []string{"skills/nope.md", "../x", "/etc/passwd", ""} {
		if _, err := Shipped(bad); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Shipped(%q) = %v, want fs.ErrNotExist", bad, err)
		}
	}
}
