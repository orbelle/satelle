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

func installedTwo(t *testing.T) string {
	t.Helper()
	data := installed12(t)
	if _, err := Install(data, "acme/other", verified(t, "1.0.0", otherFiles()), updateNames, allow); err != nil {
		t.Fatal(err)
	}
	return data
}

func otherFiles() []fakeserver.PackFile {
	return []fakeserver.PackFile{
		{Path: "other/SKILL.md", Body: []byte("---\nname: other\n---\nOther.\n")},
		{Path: "other/pack.toml", Body: []byte("name = \"other\"\nversion = \"1.0.0\"\nskills = [\"other\"]\n")},
	}
}

func edit(t *testing.T, data string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if err := os.WriteFile(filepath.Join(data, "skills", filepath.FromSlash(rel)), []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gone(t *testing.T, data string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if _, err := os.Lstat(filepath.Join(data, "skills", rel)); !os.IsNotExist(err) {
			t.Errorf("skills/%s still exists (%v)", rel, err)
		}
	}
}

var reviewPaths = []string{"review-lint/SKILL.md", "review-lint/scripts/check.sh", "review/SKILL.md", "review/pack.toml"}

func TestRemoveDeletesUneditedPack(t *testing.T) {
	data := installedTwo(t)
	before, err := LoadRecord(filepath.Join(data, "packs.toml"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := before.Find("acme/other")
	res, err := Remove(data, "acme/review", false, updateNames, allow)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if res.Version != "1.2.3" || res.Forced || !slices.Equal(res.Removed, reviewPaths) || len(res.Edited)+len(res.Missing)+len(res.Kept) != 0 ||
		!slices.Equal(res.Skills, []string{"review", "review-lint"}) {
		t.Errorf("result %+v", res)
	}
	gone(t, data, "review", "review-lint")
	if e := entries(t, filepath.Join(data, "skills")); !slices.Equal(e, []string{"other"}) {
		t.Errorf("skills holds %v", e)
	}
	after, err := LoadRecord(filepath.Join(data, "packs.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after.Find("acme/review"); ok {
		t.Error("acme/review is still recorded")
	}
	if got, ok := after.Find("acme/other"); !ok || !slices.Equal(got.Files, other.Files) || got.Digest != other.Digest {
		t.Errorf("acme/other entry changed: %+v", got)
	}
	if e := entries(t, data); !slices.Equal(e, []string{"packs.toml", "skills"}) {
		t.Errorf("data dir holds %v", e)
	}

	if _, err := Remove(data, "acme/other", false, updateNames, allow); err != nil {
		t.Fatal(err)
	}
	empty, err := LoadRecord(filepath.Join(data, "packs.toml"))
	if err != nil || len(empty.Packs) != 0 {
		t.Errorf("record after removing every pack: %+v %v", empty, err)
	}
	if _, err := Remove(data, "acme/review", false, updateNames, allow); err == nil || !strings.Contains(err.Error(), "acme/review is not installed") {
		t.Errorf("not installed: %v", err)
	}
}

func TestRemoveRefusesEditedFiles(t *testing.T) {
	data := installed12(t)
	edit(t, data, "review/SKILL.md", "review-lint/scripts/check.sh")
	before := snapshot(t, data)
	checked := false
	_, err := Remove(data, "acme/review", false, updateNames, func(string) error { checked = true; return nil })
	if err == nil {
		t.Fatal("Remove removed an edited pack")
	}
	base := filepath.Base(data)
	for _, w := range []string{base + "/skills/review/SKILL.md", base + "/skills/review-lint/scripts/check.sh", "nothing removed", "--force"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q lacks %q", err, w)
		}
	}
	if strings.Contains(err.Error(), "review/pack.toml") {
		t.Errorf("error names an unedited file: %v", err)
	}
	if checked {
		t.Error("check was called before the refusal")
	}
	if after := snapshot(t, data); !maps.Equal(before, after) {
		t.Error("a refused remove changed the data dir")
	}
}

func TestRemoveForceDeletesEditedFiles(t *testing.T) {
	data := installed12(t)
	edit(t, data, "review/SKILL.md", "review-lint/scripts/check.sh")
	if err := os.WriteFile(filepath.Join(data, "skills", "review-lint", "local.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(data, "skills", "review", "pack.toml")); err != nil {
		t.Fatal(err)
	}
	res, err := Remove(data, "acme/review", true, updateNames, allow)
	if err != nil {
		t.Fatalf("Remove --force: %v", err)
	}
	if !res.Forced || !slices.Equal(res.Edited, []string{"review-lint/scripts/check.sh", "review/SKILL.md"}) ||
		!slices.Equal(res.Removed, []string{"review-lint/SKILL.md", "review-lint/scripts/check.sh", "review/SKILL.md"}) ||
		!slices.Equal(res.Missing, []string{"review/pack.toml"}) || !slices.Equal(res.Kept, []string{"review-lint/local.md"}) {
		t.Errorf("result %+v", res)
	}
	gone(t, data, "review", "review-lint/SKILL.md", "review-lint/scripts")
	if got := snapshot(t, filepath.Join(data, "skills")); len(got) != 3 || !strings.HasSuffix(got[filepath.Join("review-lint", "local.md")], "local\n") {
		t.Errorf("skills holds %v, want only review-lint/local.md", got)
	}
	rec, err := LoadRecord(filepath.Join(data, "packs.toml"))
	if err != nil || len(rec.Packs) != 0 {
		t.Errorf("record %+v %v", rec, err)
	}
}

func TestRemoveRefusesUnsafeRecord(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for _, c := range []struct{ name, file string }{
		{"path with ..", `"review/../../README.md"`},
		{"path ../", `"../outside.md"`},
		{"absolute path", `"/etc/x"`},
		{"file outside skills", `"other/SKILL.md"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, ".satelle")
			if err := os.Mkdir(data, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := Install(data, "acme/review", verified(t, "1.2.3", versionedFiles("1.2.3")), updateNames, allow); err != nil {
				t.Fatal(err)
			}
			for _, s := range []string{filepath.Join(root, "README.md"), filepath.Join(root, "outside.md")} {
				if err := os.WriteFile(s, []byte("sentinel\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			record := "[[pack]]\nref = \"acme/review\"\nname = \"review\"\nversion = \"1.2.3\"\ndigest = \"d\"\nskills = [\"review\", \"review-lint\"]\n\n[[pack.file]]\npath = " +
				c.file + "\nsha256 = \"" + sum + "\"\n"
			if err := os.WriteFile(filepath.Join(data, "packs.toml"), []byte(record), 0o644); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, root)
			_, err := Remove(data, "acme/review", true, updateNames, allow)
			if err == nil || !strings.Contains(err.Error(), "packs.toml: pack acme/review") {
				t.Fatalf("Remove: %v", err)
			}
			if after := snapshot(t, root); !maps.Equal(before, after) {
				t.Error("a refused remove changed the tree")
			}
		})
	}
}

func TestRemoveRefusesSymlinkedSkillDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	data := installed12(t)
	outside := filepath.Join(t.TempDir(), "review-lint")
	lint := filepath.Join(data, "skills", "review-lint")
	if err := os.Rename(lint, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, lint); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	before, beforeData := snapshot(t, outside), snapshot(t, data)
	_, err := Remove(data, "acme/review", true, updateNames, allow)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Remove: %v", err)
	}
	if after := snapshot(t, outside); !maps.Equal(before, after) {
		t.Error("the symlink target changed")
	}
	if after := snapshot(t, data); !maps.Equal(beforeData, after) {
		t.Error("a refused remove changed the data dir")
	}
}

func TestRemoveChecksEveryWrittenPath(t *testing.T) {
	data := installed12(t)
	var seen []string
	if _, err := Remove(data, "acme/review", false, updateNames, func(p string) error { seen = append(seen, p); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, rel := range append([]string{"review", "review-lint"}, reviewPaths...) {
		if p := filepath.Join(data, "skills", filepath.FromSlash(rel)); !slices.Contains(seen, p) {
			t.Errorf("%s was not checked", rel)
		}
	}
	for _, p := range []string{filepath.Join(data, "skills"), filepath.Join(data, "packs.toml"), filepath.Join(data, updateNames.StagePrefix)} {
		if !slices.Contains(seen, p) {
			t.Errorf("%s was not checked", p)
		}
	}

	for _, rel := range append([]string{"packs.toml"}, reviewPaths...) {
		t.Run(rel, func(t *testing.T) {
			data := installed12(t)
			edit(t, data, "review/SKILL.md")
			deny := errors.New("denied by the gate")
			target := filepath.Join(data, "skills", filepath.FromSlash(rel))
			if rel == "packs.toml" {
				target = filepath.Join(data, rel)
			}
			before := snapshot(t, data)
			_, err := Remove(data, "acme/review", true, updateNames, func(p string) error {
				if p == target {
					return deny
				}
				return nil
			})
			if !errors.Is(err, deny) {
				t.Fatalf("Remove error %v, want the check's", err)
			}
			if after := snapshot(t, data); !maps.Equal(before, after) {
				t.Error("a denied remove changed the data dir")
			}
		})
	}
}

func TestRemoveRollsBackOnCommitFailure(t *testing.T) {
	data := installed12(t)
	edit(t, data, "review-lint/SKILL.md")
	before := snapshot(t, data)
	renameRecord = func(string, string) error { return errors.New("disk full") }
	t.Cleanup(func() { renameRecord = os.Rename })
	_, err := Remove(data, "acme/review", true, updateNames, allow)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Remove with a failing record commit: %v", err)
	}
	if after := snapshot(t, data); !maps.Equal(before, after) {
		t.Errorf("rollback left\n%v\nwant\n%v", after, before)
	}
}
