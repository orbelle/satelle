package cli

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

func update(t *testing.T, srv *fakeserver.Server, ref, version string, extra ...string) (string, error) {
	t.Helper()
	return run(t, "", append([]string{"pack", "update", ref, "--version", version, "--server", srv.URL}, extra...)...)
}

// reviewPack130 drops check.sh and adds review-lint/notes.md beside a
// changed review/SKILL.md.
func reviewPack130() []fakeserver.PackFile {
	files := reviewPack("1.3.0")
	files[0].Body = append(skillBody("review"), []byte("Now 1.3.0.\n")...)
	files = slices.Delete(files, 3, 4)
	return append(files, fakeserver.PackFile{Path: "review-lint/notes.md", Body: []byte("notes\n")})
}

// reviewPack130Edits changes review/SKILL.md, review-lint/SKILL.md and check.sh.
func reviewPack130Edits() []fakeserver.PackFile {
	files := reviewPack("1.3.0")
	files[0].Body = append(skillBody("review"), []byte("Now 1.3.0.\n")...)
	files[2].Body = append(skillBody("review-lint"), []byte("Lint 1.3.0.\n")...)
	files[3].Body = []byte("#!/bin/sh\necho 1.3.0\n")
	return files
}

func bundleTree(files []fakeserver.PackFile, dir string) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		if top, rest, _ := strings.Cut(f.Path, "/"); top == dir {
			out[filepath.FromSlash(rest)] = string(f.Body)
		}
	}
	return out
}

func recordedPack(t *testing.T, dir string) (string, string, int) {
	t.Helper()
	var rec struct {
		Pack []struct {
			Ref, Version, Digest string
			File                 []struct{ Path, SHA256 string }
		}
	}
	if _, err := toml.DecodeFile(filepath.Join(dir, ".satelle", "packs.toml"), &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Pack) != 1 {
		t.Fatalf("record %+v", rec)
	}
	return rec.Pack[0].Version, rec.Pack[0].Digest, len(rec.Pack[0].File)
}

func TestPackUpdateReplacesUneditedPack(t *testing.T) {
	dir, srv, _ := packRepo(t)
	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
	files := reviewPack130()
	digest := seedPack(srv, "acme/review", "1.3.0", files)

	out, err := update(t, srv, "acme/review", "1.3.0", "--session", "s-1")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	want := "updated acme/review 1.2.3 -> 1.3.0 (3 replaced, 1 added, 1 removed, 0 edited left for merging, digest " + digest[:12] + "); recorded in .satelle/packs.toml"
	if strings.TrimSpace(out) != want {
		t.Errorf("output %q, want %q", out, want)
	}
	skills := filepath.Join(dir, ".satelle", "skills")
	for _, d := range []string{"review", "review-lint"} {
		got := tree(t, filepath.Join(skills, d))
		delete(got, ".")
		if want := bundleTree(files, d); !maps.Equal(got, want) {
			t.Errorf("%s holds %v, want %v", d, got, want)
		}
	}
	if runtime.GOOS != "windows" {
		for _, f := range files {
			st, err := os.Stat(filepath.Join(skills, filepath.FromSlash(f.Path)))
			if err != nil {
				t.Fatal(err)
			}
			if (st.Mode()&0o111 != 0) != f.Executable {
				t.Errorf("%s mode %o", f.Path, st.Mode().Perm())
			}
		}
	}
	if v, d, n := recordedPack(t, dir); v != "1.3.0" || d != digest || n != 4 {
		t.Errorf("record %s %s %d", v, d, n)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".satelle", "pack-merge")); !os.IsNotExist(err) {
		t.Errorf("pack-merge exists: %v", err)
	}
	noStage(t, dir)
	must(t, "validate")

	record, _ := os.ReadFile(filepath.Join(dir, ".satelle", "packs.toml"))
	again, err := update(t, srv, "acme/review", "1.3.0", "--session", "s-1")
	if err != nil || strings.TrimSpace(again) != "acme/review@1.3.0 is already installed; nothing written" {
		t.Errorf("repeat update: %v\n%s", err, again)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, ".satelle", "packs.toml")); string(after) != string(record) {
		t.Error("a repeat update rewrote packs.toml")
	}

	digest14 := seedPack(srv, "acme/review", "1.4.0", reviewPack("1.4.0"))
	raw, err := update(t, srv, "acme/review", "1.4.0", "--session", "s-1", "--json")
	if err != nil {
		t.Fatalf("update --json: %v\n%s", err, raw)
	}
	var got struct {
		Ref, Name, Version, Digest, Record string
		FromVersion                        string `json:"from_version"`
		Skills, Replaced, Added, Removed   []string
		Edited                             []struct{ Path, Installed, New string }
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("json %v:\n%s", err, raw)
	}
	if got.Ref != "acme/review" || got.Name != "review" || got.FromVersion != "1.3.0" || got.Version != "1.4.0" || got.Digest != digest14 ||
		got.Record != ".satelle/packs.toml" || !slices.Equal(got.Skills, []string{"review", "review-lint"}) ||
		!slices.Equal(got.Added, []string{".satelle/skills/review-lint/scripts/check.sh"}) ||
		!slices.Equal(got.Removed, []string{".satelle/skills/review-lint/notes.md"}) || len(got.Replaced) != 3 || len(got.Edited) != 0 {
		t.Errorf("json %+v", got)
	}
}

func TestPackUpdateLeavesEditedFiles(t *testing.T) {
	dir, srv, _ := packRepo(t)
	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
	skills := filepath.Join(dir, ".satelle", "skills")
	lint := filepath.Join(skills, "review-lint", "SKILL.md")
	if err := os.WriteFile(lint, []byte("my lint\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lint, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(skills, "review-lint", "scripts", "check.sh")); err != nil {
		t.Fatal(err)
	}
	files := reviewPack130Edits()
	seedPack(srv, "acme/review", "1.3.0", files)

	out, err := update(t, srv, "acme/review", "1.3.0", "--session", "s-1")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	for _, w := range []string{
		"(2 replaced, 0 added, 0 removed, 2 edited left for merging",
		"edited .satelle/skills/review-lint/SKILL.md: installed .satelle/pack-merge/acme/review/1.2.3/review-lint/SKILL.md, new .satelle/pack-merge/acme/review/1.3.0/review-lint/SKILL.md\n",
		"edited .satelle/skills/review-lint/scripts/check.sh: installed .satelle/pack-merge/acme/review/1.2.3/review-lint/scripts/check.sh, new .satelle/pack-merge/acme/review/1.3.0/review-lint/scripts/check.sh\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if b, _ := os.ReadFile(lint); string(b) != "my lint\n" {
		t.Errorf("the edited file changed: %q", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(lint); st.Mode().Perm() != 0o600 {
			t.Errorf("the edited file's mode changed: %o", st.Mode().Perm())
		}
	}
	if _, err := os.Lstat(filepath.Join(skills, "review-lint", "scripts", "check.sh")); !os.IsNotExist(err) {
		t.Errorf("the deleted check.sh came back: %v", err)
	}
	for _, f := range files[:2] {
		if b, _ := os.ReadFile(filepath.Join(skills, filepath.FromSlash(f.Path))); string(b) != string(f.Body) {
			t.Errorf("%s = %q, want 1.3.0's", f.Path, b)
		}
	}
	merge := filepath.Join(dir, ".satelle", "pack-merge", "acme", "review")
	old := reviewPack("1.2.3")
	wantCopies := map[string]string{
		"1.2.3/review-lint/SKILL.md":         string(old[2].Body),
		"1.2.3/review-lint/scripts/check.sh": string(old[3].Body),
		"1.3.0/review-lint/SKILL.md":         string(files[2].Body),
		"1.3.0/review-lint/scripts/check.sh": string(files[3].Body),
	}
	got := map[string]string{}
	for k, v := range tree(t, merge) {
		if !strings.HasPrefix(v, "d") {
			got[filepath.ToSlash(k)] = v
		}
	}
	if !maps.Equal(got, wantCopies) {
		t.Errorf("merge copies %v, want %v", got, wantCopies)
	}
	if v, _, _ := recordedPack(t, dir); v != "1.3.0" {
		t.Errorf("record version %s", v)
	}
	noStage(t, dir)

	t.Run("json and a local file at a newly published path", func(t *testing.T) {
		if err := os.RemoveAll(filepath.Join(dir, ".satelle", "pack-merge")); err != nil {
			t.Fatal(err)
		}
		notes := filepath.Join(skills, "review-lint", "notes.md")
		if err := os.WriteFile(notes, []byte("my notes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		next := append(reviewPack130Edits(), fakeserver.PackFile{Path: "review-lint/notes.md", Body: []byte("notes\n")})
		next[1].Body = []byte("name = \"review\"\nversion = \"1.4.0\"\nskills = [\"review-lint\"]\n")
		seedPack(srv, "acme/review", "1.4.0", next)
		raw, err := update(t, srv, "acme/review", "1.4.0", "--session", "s-1", "--json")
		if err != nil {
			t.Fatalf("update --json: %v\n%s", err, raw)
		}
		var res struct {
			Edited []struct{ Path, Installed, New string }
		}
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			t.Fatalf("json %v:\n%s", err, raw)
		}
		i := slices.IndexFunc(res.Edited, func(e struct{ Path, Installed, New string }) bool {
			return e.Path == ".satelle/skills/review-lint/notes.md"
		})
		if i < 0 || res.Edited[i].Installed != "" || res.Edited[i].New != ".satelle/pack-merge/acme/review/1.4.0/review-lint/notes.md" {
			t.Fatalf("edited %+v", res.Edited)
		}
		if b, _ := os.ReadFile(notes); string(b) != "my notes\n" {
			t.Errorf("the local notes.md changed: %q", b)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, ".satelle", "pack-merge", "acme", "review", "1.4.0", "review-lint", "notes.md")); string(b) != "notes\n" {
			t.Errorf("new copy of notes.md = %q", b)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".satelle", "pack-merge", "acme", "review", "1.3.0", "review-lint", "notes.md")); !os.IsNotExist(err) {
			t.Errorf("an installed copy of notes.md was written: %v", err)
		}

		human, err := run(t, "", "pack", "update", "acme/review", "--version", "1.3.0", "--server", srv.URL, "--session", "s-1")
		if err == nil || !strings.Contains(err.Error(), "merge copies from an earlier update remain at .satelle/pack-merge/acme/review") {
			t.Errorf("update over leftover merge copies: %v\n%s", err, human)
		}
	})
}

func TestPackUpdateRefusals(t *testing.T) {
	setup := func(t *testing.T) (string, *fakeserver.Server) {
		dir, srv, _ := packRepo(t)
		must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
		seedPack(srv, "acme/review", "1.3.0", reviewPack130Edits())
		return dir, srv
	}
	refused := func(t *testing.T, dir string, before map[string]string, err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("update: %v, want %q", err, want)
		}
		if after := tree(t, filepath.Join(dir, ".satelle")); !maps.Equal(before, after) {
			t.Error("a refused update changed .satelle")
		}
		noStage(t, dir)
	}

	t.Run("not installed", func(t *testing.T) {
		dir, srv, _ := packRepo(t)
		seedPack(srv, "acme/review", "1.3.0", reviewPack130Edits())
		before := tree(t, filepath.Join(dir, ".satelle"))
		_, err := update(t, srv, "acme/review", "1.3.0", "--session", "s-1")
		refused(t, dir, before, err, "is not installed; use satelle pack install")
	})

	t.Run("no tree seat", func(t *testing.T) {
		dir, srv := setup(t)
		before := tree(t, filepath.Join(dir, ".satelle"))
		_, err := update(t, srv, "acme/review", "1.3.0", "--session", "s-2")
		refused(t, dir, before, err, "Seat one with")
	})

	t.Run("locked replaced file", func(t *testing.T) {
		dir, srv := setup(t)
		setLocked(t, dir, `[".satelle/skills/review/SKILL.md"]`)
		before := tree(t, filepath.Join(dir, ".satelle"))
		_, err := update(t, srv, "acme/review", "1.3.0", "--session", "s-1")
		refused(t, dir, before, err, "locked path")
	})

	t.Run("new member directory exists unrecorded", func(t *testing.T) {
		dir, srv := setup(t)
		files := append(reviewPack("1.5.0"), fakeserver.PackFile{Path: "review-extra/SKILL.md", Body: skillBody("review-extra")})
		files[1].Body = []byte("name = \"review\"\nversion = \"1.5.0\"\nskills = [\"review-lint\", \"review-extra\"]\n")
		seedPack(srv, "acme/review", "1.5.0", files)
		mine := filepath.Join(dir, ".satelle", "skills", "review-extra")
		if err := os.MkdirAll(mine, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), skillBody("review-extra"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := tree(t, filepath.Join(dir, ".satelle"))
		_, err := update(t, srv, "acme/review", "1.5.0", "--session", "s-1")
		refused(t, dir, before, err, "not recorded")
	})

	t.Run("tampered new bundle", func(t *testing.T) {
		dir, srv := setup(t)
		tampered, _ := fakeserver.PackBundle("review", "1.6.0", reviewPack("1.6.0"))
		tampered.Files[3].Body = []byte("#!/bin/sh\necho evil\n")
		srv.SeedPack("acme/review", "1.6.0", tampered)
		before := tree(t, filepath.Join(dir, ".satelle"))
		_, err := update(t, srv, "acme/review", "1.6.0", "--session", "s-1")
		refused(t, dir, before, err, "sha256")
	})

	t.Run("leftover merge copies with edits", func(t *testing.T) {
		dir, srv := setup(t)
		if err := os.WriteFile(filepath.Join(dir, ".satelle", "skills", "review", "SKILL.md"), []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, ".satelle", "pack-merge", "acme", "review"), 0o755); err != nil {
			t.Fatal(err)
		}
		before := tree(t, filepath.Join(dir, ".satelle"))
		_, err := update(t, srv, "acme/review", "1.3.0", "--session", "s-1")
		refused(t, dir, before, err, "merge copies from an earlier update remain")
	})
}
