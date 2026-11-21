package pack

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

var updateNames = Names{Skills: "skills", Record: "packs.toml", StagePrefix: ".satelle-pack-staging", MergeDir: "pack-merge"}

func versionedFiles(version string) []fakeserver.PackFile {
	files := reviewFiles()
	files[1].Body = []byte("name = \"review\"\nversion = \"" + version + "\"\nskills = [\"review-lint\"]\n")
	return files
}

func nextFiles() []fakeserver.PackFile {
	files := versionedFiles("1.3.0")
	files[0].Body = []byte("---\nname: review\n---\nWire the review pack, 1.3.0.\n")
	files = slices.Delete(files, 3, 4)
	return append(files, fakeserver.PackFile{Path: "review-lint/notes.md", Body: []byte("notes\n")})
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		v := info.Mode().String()
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += " " + string(b)
		}
		out[rel] = v
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return out
}

func installed12(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	if _, err := Install(data, "acme/review", verified(t, "1.2.3", versionedFiles("1.2.3")), updateNames, allow); err != nil {
		t.Fatal(err)
	}
	return data
}

func neverFetched(t *testing.T) func() (Verified, error) {
	return func() (Verified, error) {
		t.Error("the installed version was fetched")
		return Verified{}, errors.New("not wanted")
	}
}

func TestUpdateReplacesUneditedFiles(t *testing.T) {
	data := installed12(t)
	next := verified(t, "1.3.0", nextFiles())
	res, err := Update(data, "acme/review", next, neverFetched(t), updateNames, allow)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.FromVersion != "1.2.3" || res.Version != "1.3.0" || len(res.Edited) != 0 ||
		!slices.Equal(res.Added, []string{"review-lint/notes.md"}) ||
		!slices.Equal(res.Removed, []string{"review-lint/scripts/check.sh"}) || len(res.Replaced) != 3 {
		t.Errorf("result %+v", res)
	}

	fresh := t.TempDir()
	if _, err := Install(fresh, "acme/review", next, updateNames, allow); err != nil {
		t.Fatal(err)
	}
	if got, want := snapshot(t, filepath.Join(data, "skills")), snapshot(t, filepath.Join(fresh, "skills")); !maps.Equal(got, want) {
		t.Errorf("skills after update\n%v\nwant a fresh 1.3.0 install\n%v", got, want)
	}
	got, _ := os.ReadFile(filepath.Join(data, "packs.toml"))
	want, _ := os.ReadFile(filepath.Join(fresh, "packs.toml"))
	if string(got) != string(want) {
		t.Errorf("packs.toml\n%s\nwant\n%s", got, want)
	}
	if e := entries(t, data); !slices.Equal(e, []string{"packs.toml", "skills"}) {
		t.Errorf("data dir holds %v", e)
	}

	again, err := Update(data, "acme/review", next, neverFetched(t), updateNames, allow)
	if err != nil || !again.AlreadyInstalled {
		t.Errorf("repeat update: %+v %v", again, err)
	}
	if after, _ := os.ReadFile(filepath.Join(data, "packs.toml")); string(after) != string(got) {
		t.Error("a repeat update rewrote packs.toml")
	}
	if _, err := Update(data, "acme/review", verified(t, "1.3.0", versionedFiles("1.3.0")), neverFetched(t), updateNames, allow); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("same version, another digest: %v", err)
	}
	if _, err := Update(data, "acme/other", next, neverFetched(t), updateNames, allow); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("not installed: %v", err)
	}
}

func TestUpdateLeavesEditedFiles(t *testing.T) {
	data := installed12(t)
	lint := filepath.Join(data, "skills", "review-lint", "SKILL.md")
	if err := os.WriteFile(lint, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lint, 0o600); err != nil {
		t.Fatal(err)
	}
	old := verified(t, "1.2.3", versionedFiles("1.2.3"))
	fetched := 0
	res, err := Update(data, "acme/review", verified(t, "1.3.0", nextFiles()), func() (Verified, error) { fetched++; return old, nil }, updateNames, allow)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if fetched != 1 {
		t.Errorf("installed fetched %d times", fetched)
	}
	want := []EditedFile{{Path: "review-lint/SKILL.md",
		Installed: filepath.Base(data) + "/pack-merge/acme/review/1.2.3/review-lint/SKILL.md",
		New:       filepath.Base(data) + "/pack-merge/acme/review/1.3.0/review-lint/SKILL.md"}}
	if !slices.Equal(res.Edited, want) {
		t.Errorf("edited %+v, want %+v", res.Edited, want)
	}
	if b, _ := os.ReadFile(lint); string(b) != "mine\n" {
		t.Errorf("the edited file changed: %q", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(lint); st.Mode().Perm() != 0o600 {
			t.Errorf("the edited file's mode changed: %o", st.Mode().Perm())
		}
	}
	merge := filepath.Join(data, "pack-merge", "acme", "review")
	if got := snapshot(t, merge); len(got) != 7 {
		t.Errorf("merge copies %v", got)
	}
}

func TestUpdateRefusesUnsafeRecord(t *testing.T) {
	sum := strings.Repeat("a", 64)
	entry := func(version, skills, file, sha string) string {
		return "[[pack]]\nref = \"acme/review\"\nname = \"review\"\nversion = " + version + "\ndigest = \"d\"\nskills = " + skills +
			"\n\n[[pack.file]]\npath = " + file + "\nsha256 = \"" + sha + "\"\n"
	}
	ok := `["review", "review-lint"]`
	for _, c := range []struct{ name, record string }{
		{"path with ..", entry(`"1.2.3"`, ok, `"review/../../README.md"`, sum)},
		{"path ../", entry(`"1.2.3"`, ok, `"../outside.md"`, sum)},
		{"absolute path", entry(`"1.2.3"`, ok, `"/etc/x"`, sum)},
		{"drive path", entry(`"1.2.3"`, ok, `"C:/x"`, sum)},
		{"backslash path", entry(`"1.2.3"`, ok, `'review\..\..\x'`, sum)},
		{"file outside skills", entry(`"1.2.3"`, ok, `"other/SKILL.md"`, sum)},
		{"skill ..", entry(`"1.2.3"`, `[".."]`, `"review/SKILL.md"`, sum)},
		{"skill a/b", entry(`"1.2.3"`, `["a/b"]`, `"review/SKILL.md"`, sum)},
		{"skill a\\b", entry(`"1.2.3"`, `['a\b']`, `"review/SKILL.md"`, sum)},
		{"version ../x", entry(`"../x"`, ok, `"review/SKILL.md"`, sum)},
		{"version ..\\x", entry(`'..\x'`, ok, `"review/SKILL.md"`, sum)},
		{"version C:x", entry(`"C:x"`, ok, `"review/SKILL.md"`, sum)},
		{"version .", entry(`"."`, ok, `"review/SKILL.md"`, sum)},
		{"malformed sha256", entry(`"1.2.3"`, ok, `"review/SKILL.md"`, "ABC")},
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
			if err := os.WriteFile(filepath.Join(data, "packs.toml"), []byte(c.record), 0o644); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, root)
			checked := false
			_, err := Update(data, "acme/review", verified(t, "1.3.0", nextFiles()), neverFetched(t), updateNames, func(string) error { checked = true; return nil })
			if err == nil || !strings.Contains(err.Error(), "packs.toml: pack acme/review") {
				t.Fatalf("Update: %v", err)
			}
			if checked {
				t.Error("check was called")
			}
			if after := snapshot(t, root); !maps.Equal(before, after) {
				t.Error("a refused update changed the tree")
			}
		})
	}
}

func TestUpdateRefusesUnsafeVersion(t *testing.T) {
	for _, v := range []string{"../x", `..\x`, "C:x", "a/b"} {
		t.Run("next "+v, func(t *testing.T) {
			data := installed12(t)
			before := snapshot(t, data)
			if _, err := Update(data, "acme/review", verified(t, v, nextFiles()), neverFetched(t), updateNames, allow); err == nil {
				t.Fatal("Update accepted it")
			}
			if after := snapshot(t, data); !maps.Equal(before, after) {
				t.Error("a refused update changed the data dir")
			}
		})
		t.Run("installed "+v, func(t *testing.T) {
			data := installed12(t)
			if err := os.WriteFile(filepath.Join(data, "skills", "review", "SKILL.md"), []byte("mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, data)
			bad := verified(t, v, versionedFiles("1.2.3"))
			if _, err := Update(data, "acme/review", verified(t, "1.3.0", nextFiles()), func() (Verified, error) { return bad, nil }, updateNames, allow); err == nil {
				t.Fatal("Update accepted it")
			}
			if after := snapshot(t, data); !maps.Equal(before, after) {
				t.Error("a refused update changed the data dir")
			}
		})
	}
}

func TestUpdateRefusesWhenInstalledVersionNoLongerVerifies(t *testing.T) {
	data := installed12(t)
	if err := os.WriteFile(filepath.Join(data, "skills", "review", "SKILL.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, data)
	files := versionedFiles("1.2.3")
	files[2].Body = []byte("republished\n")
	other := verified(t, "1.2.3", files)
	_, err := Update(data, "acme/review", verified(t, "1.3.0", nextFiles()), func() (Verified, error) { return other, nil }, updateNames, allow)
	if err == nil || !strings.Contains(err.Error(), "no longer verifies") {
		t.Fatalf("Update: %v", err)
	}
	if after := snapshot(t, data); !maps.Equal(before, after) {
		t.Error("a refused update changed the data dir")
	}
}

func TestUpdateRefusesSymlinkedSkillDir(t *testing.T) {
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
	_, err := Update(data, "acme/review", verified(t, "1.3.0", nextFiles()), neverFetched(t), updateNames, allow)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Update: %v", err)
	}
	if after := snapshot(t, outside); !maps.Equal(before, after) {
		t.Error("the symlink target changed")
	}
	if after := snapshot(t, data); !maps.Equal(beforeData, after) {
		t.Error("a refused update changed the data dir")
	}
}

func TestUpdateRollsBackOnCommitFailure(t *testing.T) {
	data := installed12(t)
	if err := os.WriteFile(filepath.Join(data, "skills", "review-lint", "SKILL.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, data)
	renameRecord = func(string, string) error { return errors.New("disk full") }
	t.Cleanup(func() { renameRecord = os.Rename })
	old := verified(t, "1.2.3", versionedFiles("1.2.3"))
	_, err := Update(data, "acme/review", verified(t, "1.3.0", nextFiles()), func() (Verified, error) { return old, nil }, updateNames, allow)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Update with a failing record commit: %v", err)
	}
	if after := snapshot(t, data); !maps.Equal(before, after) {
		t.Errorf("rollback left\n%v\nwant\n%v", after, before)
	}
}

func TestUpdateChecksEveryWrittenPath(t *testing.T) {
	data := installed12(t)
	deny := errors.New("denied by the gate")
	target := filepath.Join(data, "skills", "review-lint", "notes.md")
	before := snapshot(t, data)
	_, err := Update(data, "acme/review", verified(t, "1.3.0", nextFiles()), neverFetched(t), updateNames, func(p string) error {
		if p == target {
			return deny
		}
		return nil
	})
	if !errors.Is(err, deny) {
		t.Fatalf("Update error %v, want the check's", err)
	}
	if after := snapshot(t, data); !maps.Equal(before, after) {
		t.Error("a denied update changed the data dir")
	}
}
