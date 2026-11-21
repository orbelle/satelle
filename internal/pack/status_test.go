package pack

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestStatusClassifiesRecordedFiles(t *testing.T) {
	empty, err := Status(t.TempDir(), updateNames)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("no packs.toml: %v, %v", empty, err)
	}

	data := installed12(t)
	skills := filepath.Join(data, "skills")
	if err := os.WriteFile(filepath.Join(skills, "review", "SKILL.md"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(skills, "review-lint", "scripts", "check.sh")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, data)
	sts, err := Status(data, updateNames)
	if err != nil {
		t.Fatal(err)
	}
	if len(sts) != 1 {
		t.Fatalf("status %+v", sts)
	}
	st := sts[0]
	if st.Ref != "acme/review" || st.Name != "review" || st.Version != "1.2.3" || st.Newest != "" || st.NewestError != "" ||
		!slices.Equal(st.Edited, []string{"review/SKILL.md"}) ||
		!slices.Equal(st.Missing, []string{"review-lint/scripts/check.sh"}) {
		t.Errorf("status %+v", st)
	}
	if after := snapshot(t, data); len(after) != len(before) {
		t.Error("status changed the data dir")
	}

	clean, err := Status(installed12(t), updateNames)
	if err != nil || len(clean) != 1 || clean[0].Edited == nil || clean[0].Missing == nil || len(clean[0].Edited)+len(clean[0].Missing) != 0 {
		t.Errorf("unedited pack: %+v, %v", clean, err)
	}
}

func TestStatusRefusesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	outside := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "target"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	relink := func(t *testing.T, at, to string) {
		t.Helper()
		if err := os.RemoveAll(at); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, at); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]func(t *testing.T, data string){
		"record": func(t *testing.T, data string) {
			dir := outside(t, "")
			b, _ := os.ReadFile(filepath.Join(data, "packs.toml"))
			if err := os.WriteFile(filepath.Join(dir, "target"), b, 0o644); err != nil {
				t.Fatal(err)
			}
			relink(t, filepath.Join(data, "packs.toml"), filepath.Join(dir, "target"))
		},
		"skills dir": func(t *testing.T, data string) {
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS(filepath.Join(data, "skills"))); err != nil {
				t.Fatal(err)
			}
			relink(t, filepath.Join(data, "skills"), dir)
		},
		"skill dir": func(t *testing.T, data string) {
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS(filepath.Join(data, "skills", "review"))); err != nil {
				t.Fatal(err)
			}
			relink(t, filepath.Join(data, "skills", "review"), dir)
		},
		"file": func(t *testing.T, data string) {
			dir := outside(t, "a different body\n")
			relink(t, filepath.Join(data, "skills", "review", "SKILL.md"), filepath.Join(dir, "target"))
		},
	}
	for name, link := range cases {
		t.Run(name, func(t *testing.T) {
			data := installed12(t)
			link(t, data)
			sts, err := Status(data, updateNames)
			if err == nil || !strings.Contains(err.Error(), "symlink is not allowed") {
				t.Fatalf("status %+v, err %v; want a symlink refusal", sts, err)
			}
		})
	}
}

func TestStatusRefusesUnplainRecord(t *testing.T) {
	const sum = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	cases := []struct{ ref, version, raw string }{
		{"-x/y", "1.0.0", "-x/y"},
		{"acme/re\tview", "1.0.0", "re\tview"},
		{"acme/$(id)", "1.0.0", "$(id)"},
		{"acme/review", "1.0\x1b[31m", "\x1b"},
		{"acme/review", "--version", "--version"},
	}
	for _, c := range cases {
		data := t.TempDir()
		rec := Record{Packs: []Installed{{Ref: c.ref, Name: "review", Version: c.version, Digest: sum,
			Skills: []string{"review"}, Files: []InstalledFile{{Path: "review/SKILL.md", SHA256: sum}}}}}
		b, err := rec.encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, "packs.toml"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = Status(data, updateNames)
		if err == nil {
			t.Errorf("%q %q: no error", c.ref, c.version)
			continue
		}
		if strings.Contains(err.Error(), c.raw) {
			t.Errorf("%q %q: error echoes the raw value: %v", c.ref, c.version, err)
		}
		if !strings.Contains(err.Error(), "is not a plain name") {
			t.Errorf("%q %q: error %v", c.ref, c.version, err)
		}
	}
}
