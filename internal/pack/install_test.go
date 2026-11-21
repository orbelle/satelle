package pack

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

var testNames = Names{Skills: "skills", Record: "packs.toml", StagePrefix: ".satelle-pack-staging", Validate: acceptAll}

func acceptAll(string) error { return nil }

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

func noStaging(t *testing.T, data string) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(data, testNames.StagePrefix+"*")); len(left) > 0 {
		t.Errorf("staging left behind: %v", left)
	}
}

func noSymlinks(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			t.Errorf("the prospect holds a symlink at %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInstallRefusesSymlinkedSkillsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	data := t.TempDir()
	elsewhere := filepath.Join(data, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(data, "skills")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	called := false
	names := testNames
	names.Validate = func(string) error { called = true; return nil }
	_, err := Install(data, "acme/review", verified(t, "1.2.3", reviewFiles()), names, allow)
	if err == nil || !strings.Contains(err.Error(), "symlink is not allowed") {
		t.Fatalf("Install: %v", err)
	}
	if called {
		t.Error("the configuration was validated over a symlinked skills dir")
	}
	if e := entries(t, elsewhere); len(e) != 0 {
		t.Errorf("the symlink target holds %v", e)
	}
	if _, err := os.Lstat(filepath.Join(data, "packs.toml")); !os.IsNotExist(err) {
		t.Errorf("packs.toml exists (%v)", err)
	}
	noStaging(t, data)
}

func TestInstallProspectHoldsNoSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	outside := filepath.Join(root, "outside")
	skills := filepath.Join(data, "skills")
	for _, d := range []string{filepath.Join(outside, "subdir"), filepath.Join(skills, "other")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "ext.md"), []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "subdir", "note.md"), []byte("note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "other", "SKILL.md"), []byte("---\nname: other\n---\nOther.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		filepath.Join(skills, "ext.md"):        filepath.Join("..", "..", "outside", "ext.md"),
		filepath.Join(skills, "other", "sub"):  filepath.Join("..", "..", "..", "outside", "subdir"),
		filepath.Join(skills, "other", "loop"): "..",
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
	}
	before := snapshot(t, outside)
	names := testNames
	validated := false
	names.Validate = func(dir string) error {
		validated = true
		noSymlinks(t, dir)
		if b, err := os.ReadFile(filepath.Join(dir, "ext.md")); err != nil || string(b) != "external\n" {
			t.Errorf("prospect ext.md = %q (%v)", b, err)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "other", "sub", "note.md")); err != nil || string(b) != "note\n" {
			t.Errorf("prospect other/sub/note.md = %q (%v)", b, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "review-lint", "SKILL.md")); err != nil {
			t.Errorf("prospect review-lint/SKILL.md: %v", err)
		}
		return nil
	}
	if _, err := Install(data, "acme/review", verified(t, "1.2.3", reviewFiles()), names, allow); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !validated {
		t.Error("the configuration was not validated")
	}
	if after := snapshot(t, outside); !maps.Equal(before, after) {
		t.Errorf("outside changed:\nbefore %v\nafter  %v", before, after)
	}
	noStaging(t, data)
}

func TestInstallRefusedByProspectValidation(t *testing.T) {
	data := t.TempDir()
	names := testNames
	names.Validate = func(string) error { return errors.New("workflow adopt: skill \"missing\" is not in skills/") }
	_, err := Install(data, "acme/review", verified(t, "1.2.3", reviewFiles()), names, allow)
	if err == nil || !strings.Contains(err.Error(), "invalid; nothing was written") || !strings.Contains(err.Error(), `"missing"`) {
		t.Fatalf("Install: %v", err)
	}
	if e := entries(t, data); len(e) != 0 {
		t.Errorf("data dir holds %v", e)
	}
}

func TestInstallRefusesWithoutValidator(t *testing.T) {
	data := t.TempDir()
	names := testNames
	names.Validate = nil
	_, err := Install(data, "acme/review", verified(t, "1.2.3", reviewFiles()), names, allow)
	if err == nil || !strings.Contains(err.Error(), "no configuration validator") {
		t.Fatalf("Install: %v", err)
	}
	if e := entries(t, data); len(e) != 0 {
		t.Errorf("the data dir holds %v", e)
	}
}
