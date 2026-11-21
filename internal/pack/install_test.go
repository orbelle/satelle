package pack

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

var testNames = Names{Skills: "skills", Record: "packs.toml", StagePrefix: ".satelle-pack-staging"}

const reviewPackToml = "name = \"review\"\nversion = \"1.2.3\"\nskills = [\"review-lint\"]\n"

func reviewFiles() []fakeserver.PackFile {
	return []fakeserver.PackFile{
		{Path: "review/SKILL.md", Body: []byte("---\nname: review\n---\nWire the review pack.\n")},
		{Path: "review/pack.toml", Body: []byte(reviewPackToml)},
		{Path: "review-lint/SKILL.md", Body: []byte("---\nname: review-lint\n---\nLint.\n")},
		{Path: "review-lint/scripts/check.sh", Body: []byte("#!/bin/sh\necho ok\n"), Executable: true},
	}
}

func verified(t *testing.T, version string, files []fakeserver.PackFile) Verified {
	t.Helper()
	bundle, _ := fakeserver.PackBundle("review", version, files)
	v, err := Verify(bundle)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return v
}

func allow(string) error { return nil }

func entries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestLayout(t *testing.T) {
	entry, dirs, err := Layout(verified(t, "1.2.3", reviewFiles()))
	if err != nil {
		t.Fatalf("Layout: %v", err)
	}
	if entry != "review" || !slices.Equal(dirs, []string{"review", "review-lint"}) {
		t.Errorf("entry %q dirs %v", entry, dirs)
	}

	with := func(extra ...fakeserver.PackFile) []fakeserver.PackFile { return append(reviewFiles(), extra...) }
	without := func(drop string, extra ...fakeserver.PackFile) []fakeserver.PackFile {
		var out []fakeserver.PackFile
		for _, f := range reviewFiles() {
			if f.Path != drop {
				out = append(out, f)
			}
		}
		return append(out, extra...)
	}
	for _, c := range []struct {
		name  string
		files []fakeserver.PackFile
		want  []string
	}{
		{"root SKILL.md", with(fakeserver.PackFile{Path: "SKILL.md", Body: []byte("x")}), []string{`"SKILL.md"`, "bundle root"}},
		{"no pack.toml", without("review/pack.toml"), []string{"pack.toml"}},
		{"two pack.toml", with(fakeserver.PackFile{Path: "review-lint/pack.toml", Body: []byte(reviewPackToml)}), []string{"review-lint/pack.toml", "more than one"}},
		{"extra top dir", with(fakeserver.PackFile{Path: "stray/SKILL.md", Body: []byte("x")}), []string{`"stray"`, "neither the entry"}},
		{"member missing SKILL.md", without("review-lint/SKILL.md"), []string{"review-lint", "does not exist"}},
		{"entry missing SKILL.md", without("review/SKILL.md"), []string{"review", "has no SKILL.md"}},
		{"invalid pack.toml", without("review/pack.toml", fakeserver.PackFile{Path: "review/pack.toml", Body: []byte("name = \"review\"\nskills = [\"review-lint\"]\n")}), []string{"review/pack.toml", "version is required"}},
		{"malformed pack.toml", without("review/pack.toml", fakeserver.PackFile{Path: "review/pack.toml", Body: []byte("name = [\n")}), []string{"review/pack.toml"}},
	} {
		_, _, err := Layout(verified(t, "1.2.3", c.files))
		if err == nil {
			t.Errorf("%s: Layout accepted it", c.name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q does not contain %q", c.name, err, w)
			}
		}
	}
}

func TestInstallWritesSkillsAndRecord(t *testing.T) {
	data := t.TempDir()
	v := verified(t, "1.2.3", reviewFiles())
	res, err := Install(data, "acme/review", v, testNames, allow)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.AlreadyInstalled || res.Files != 4 || !slices.Equal(res.Skills, []string{"review", "review-lint"}) || res.Digest != v.Digest() {
		t.Errorf("result %+v", res)
	}
	for _, f := range reviewFiles() {
		got, err := os.ReadFile(filepath.Join(data, "skills", filepath.FromSlash(f.Path)))
		if err != nil || string(got) != string(f.Body) {
			t.Errorf("%s = %q (%v)", f.Path, got, err)
		}
	}
	if got := entries(t, data); !slices.Equal(got, []string{"packs.toml", "skills"}) {
		t.Errorf("data dir holds %v", got)
	}
	rec, err := LoadRecord(filepath.Join(data, "packs.toml"))
	if err != nil {
		t.Fatal(err)
	}
	in, ok := rec.Find("acme/review")
	if !ok || in.Version != "1.2.3" || in.Digest != v.Digest() || len(in.Files) != 4 {
		t.Fatalf("record %+v", rec)
	}

	before, _ := os.ReadFile(filepath.Join(data, "packs.toml"))
	again, err := Install(data, "acme/review", v, testNames, allow)
	if err != nil || !again.AlreadyInstalled {
		t.Fatalf("second install: %+v %v", again, err)
	}
	after, _ := os.ReadFile(filepath.Join(data, "packs.toml"))
	if string(before) != string(after) {
		t.Error("an already-installed pack rewrote the record")
	}
	if _, err := Install(data, "acme/review", verified(t, "1.3.0", reviewFiles()), testNames, allow); err == nil || !strings.Contains(err.Error(), "1.2.3") {
		t.Errorf("another version: %v", err)
	}
}

func TestInstallChecksEveryPathBeforeWriting(t *testing.T) {
	data := t.TempDir()
	var seen []string
	deny := errors.New("denied by the gate")
	check := func(p string) error {
		seen = append(seen, p)
		if p == filepath.Join(data, "skills", "review-lint", "scripts", "check.sh") {
			return deny
		}
		return nil
	}
	if _, err := Install(data, "acme/review", verified(t, "1.2.3", reviewFiles()), testNames, check); !errors.Is(err, deny) {
		t.Fatalf("Install error %v, want the check's", err)
	}
	if got := entries(t, data); len(got) != 0 {
		t.Errorf("a denied install wrote %v", got)
	}

	seen = nil
	if _, err := Install(data, "acme/review", verified(t, "1.2.3", reviewFiles()), testNames, func(p string) error { seen = append(seen, p); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(data, "skills"),
		filepath.Join(data, "skills", "review"),
		filepath.Join(data, "skills", "review-lint"),
		filepath.Join(data, ".satelle-pack-staging"),
		filepath.Join(data, "packs.toml"),
	}
	for _, f := range reviewFiles() {
		want = append(want, filepath.Join(data, "skills", filepath.FromSlash(f.Path)))
	}
	for _, w := range want {
		if !slices.Contains(seen, w) {
			t.Errorf("check was not called with %s (got %v)", w, seen)
		}
	}
}

func TestInstallRefusesExistingUnrecordedDir(t *testing.T) {
	for _, c := range []struct {
		name, path string
		dir        bool
	}{
		{"skill directory", "review-lint/SKILL.md", true},
		{"single-file skill", "review-lint.md", false},
	} {
		data := t.TempDir()
		p := filepath.Join(data, "skills", filepath.FromSlash(c.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Install(data, "acme/review", verified(t, "1.2.3", reviewFiles()), testNames, allow)
		if err == nil || !strings.Contains(err.Error(), "skills/"+strings.TrimSuffix(c.path, "/SKILL.md")) || !strings.Contains(err.Error(), "not recorded") {
			t.Errorf("%s: %v", c.name, err)
		}
		if got := entries(t, data); !slices.Equal(got, []string{"skills"}) {
			t.Errorf("%s: data dir holds %v", c.name, got)
		}
		if got := entries(t, filepath.Join(data, "skills")); len(got) != 1 {
			t.Errorf("%s: skills holds %v", c.name, got)
		}
	}
}

func TestInstallRollsBackOnCommitFailure(t *testing.T) {
	data := t.TempDir()
	if _, err := Install(data, "acme/base", verified(t, "1.0.0", []fakeserver.PackFile{
		{Path: "base/SKILL.md", Body: []byte("base\n")},
		{Path: "base/pack.toml", Body: []byte("name = \"base\"\nversion = \"1.0.0\"\nskills = [\"base\"]\n")},
	}), testNames, allow); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(data, "packs.toml"))
	if err != nil {
		t.Fatal(err)
	}
	renameRecord = func(string, string) error { return errors.New("disk full") }
	t.Cleanup(func() { renameRecord = os.Rename })
	if _, err := Install(data, "acme/review", verified(t, "1.2.3", reviewFiles()), testNames, allow); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Install with a failing record commit: %v", err)
	}
	if got := entries(t, filepath.Join(data, "skills")); !slices.Equal(got, []string{"base"}) {
		t.Errorf("skills after rollback: %v", got)
	}
	if got := entries(t, data); !slices.Equal(got, []string{"packs.toml", "skills"}) {
		t.Errorf("data dir after rollback: %v", got)
	}
	after, _ := os.ReadFile(filepath.Join(data, "packs.toml"))
	if string(before) != string(after) {
		t.Error("the record changed on a failed commit")
	}
}
