package pack

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRecordRoundTripAndOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "packs.toml")
	empty, err := LoadRecord(path)
	if err != nil || len(empty.Packs) != 0 {
		t.Fatalf("missing record: %+v %v", empty, err)
	}

	b := Installed{Ref: "acme/b", Name: "b", Version: "1.0.0", Digest: "d2", Skills: []string{"b"},
		Files: []InstalledFile{{Path: "b/pack.toml", SHA256: "s2"}, {Path: "b/SKILL.md", SHA256: "s1"}}}
	a := Installed{Ref: "acme/a", Name: "a", Version: "2.0.0", Digest: "d1", Skills: []string{"a", "a-m"},
		Files: []InstalledFile{{Path: "a/SKILL.md", SHA256: "s3"}}}
	rec := Record{}.With(b).With(a)
	if err := writeRecord(dir, path, rec); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if err := writeRecord(dir, path, Record{}.With(a).With(b)); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Errorf("record output depends on insertion order:\n%s\n---\n%s", first, second)
	}
	for _, w := range []string{"[[pack]]", "[[pack.file]]", `ref = "acme/a"`, `sha256 = "s1"`} {
		if !strings.Contains(string(first), w) {
			t.Errorf("record lacks %q:\n%s", w, first)
		}
	}
	if strings.Index(string(first), "acme/a") > strings.Index(string(first), "acme/b") {
		t.Errorf("records are not sorted by ref:\n%s", first)
	}
	if strings.Index(string(first), "b/SKILL.md") > strings.Index(string(first), "b/pack.toml") {
		t.Errorf("files are not sorted by path:\n%s", first)
	}

	got, err := LoadRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rec.sorted()) {
		t.Errorf("round trip = %+v, want %+v", got, rec.sorted())
	}
	if owner, ok := got.Owner("a-m"); !ok || owner != "acme/a" {
		t.Errorf("Owner(a-m) = %q %v", owner, ok)
	}
	if _, ok := got.Owner("c"); ok {
		t.Error("Owner of an unrecorded dir")
	}
	if left := entries(t, dir); len(left) != 1 {
		t.Errorf("writeRecord left %v", left)
	}

	for name, body := range map[string]string{
		"malformed":   "[[pack]\n",
		"unknown key": "[[pack]]\nref = \"acme/a\"\ncolour = \"blue\"\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRecord(path); err == nil || !strings.Contains(err.Error(), "packs.toml") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
