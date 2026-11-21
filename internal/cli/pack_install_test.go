package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
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

func skillBody(name string) []byte {
	return []byte("---\nname: " + name + "\ntype: skill\ndescription: " + name + " skill\n---\n\n" + name + ".\n")
}

func reviewPack(version string) []fakeserver.PackFile {
	return []fakeserver.PackFile{
		{Path: "review/SKILL.md", Body: skillBody("review")},
		{Path: "review/pack.toml", Body: []byte("name = \"review\"\nversion = \"" + version + "\"\nskills = [\"review-lint\"]\n")},
		{Path: "review-lint/SKILL.md", Body: skillBody("review-lint")},
		{Path: "review-lint/scripts/check.sh", Body: []byte("#!/bin/sh\necho ok\n"), Executable: true},
	}
}

func singlePack(name, version string) []fakeserver.PackFile {
	return []fakeserver.PackFile{
		{Path: name + "/SKILL.md", Body: skillBody(name)},
		{Path: name + "/pack.toml", Body: []byte("name = \"" + name + "\"\nversion = \"" + version + "\"\nskills = [\"" + name + "\"]\n")},
	}
}

// seedPack seeds ref at version with files and returns its digest.
func seedPack(srv *fakeserver.Server, ref, version string, files []fakeserver.PackFile, readers ...string) string {
	bundle, digest := fakeserver.PackBundle(strings.TrimPrefix(ref, "acme/"), version, files)
	srv.SeedPack(ref, version, bundle, readers...)
	return digest
}

// packRepo is a repo seated by session s-1 on a story in the tree-editing state
// st-build of writePlanWorkflow, with acme/review@1.2.3 seeded.
func packRepo(t *testing.T) (string, *fakeserver.Server, string) {
	t.Helper()
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	writePlanWorkflow(t, m.dir)
	id := createID(t, "story", "create", "--title", "install", "--workflow", "planned")
	must(t, "story", "set", id, "--status", "st-plan")
	must(t, "story", "set", id, "--status", "st-build")
	must(t, "story", "seat", id, "--session", "s-1")
	return m.dir, srv, seedPack(srv, "acme/review", "1.2.3", reviewPack("1.2.3"))
}

func install(t *testing.T, srv *fakeserver.Server, ref, version string, extra ...string) (string, error) {
	t.Helper()
	return run(t, "", append([]string{"pack", "install", ref, "--version", version, "--server", srv.URL}, extra...)...)
}

// tree lists every path under dir with each file's bytes, so a test can tell
// that nothing under it changed.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = string(b)
		} else {
			out[rel] = d.Type().String()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}

func noStage(t *testing.T, dir string) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(dir, ".satelle", packStagePrefix+"*"))
	if len(left) > 0 {
		t.Errorf("staging left behind: %v", left)
	}
}

func setLocked(t *testing.T, dir, locked string) {
	t.Helper()
	p := filepath.Join(dir, ".satelle", "satelle.toml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), `["locked/**"]`, locked, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPackInstallWritesSkillsAndRecord(t *testing.T) {
	dir, srv, digest := packRepo(t)
	out, err := install(t, srv, "acme/review", "1.2.3", "--session", "s-1")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	want := "installed acme/review@1.2.3 (2 skill(s), 4 file(s), digest " + digest[:12] + ") into .satelle/skills: review, review-lint; recorded in .satelle/packs.toml"
	if strings.TrimSpace(out) != want {
		t.Errorf("output %q, want %q", out, want)
	}
	for _, f := range reviewPack("1.2.3") {
		p := filepath.Join(dir, ".satelle", "skills", filepath.FromSlash(f.Path))
		b, err := os.ReadFile(p)
		if err != nil || string(b) != string(f.Body) {
			t.Errorf("%s = %q (%v)", f.Path, b, err)
		}
		if runtime.GOOS != "windows" {
			st, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if (st.Mode()&0o111 != 0) != f.Executable {
				t.Errorf("%s mode %o", f.Path, st.Mode().Perm())
			}
		}
	}
	noStage(t, dir)

	recordPath := filepath.Join(dir, ".satelle", "packs.toml")
	var rec struct {
		Pack []struct {
			Ref, Version, Digest string
			Skills               []string
			File                 []struct{ Path, SHA256 string }
		}
	}
	if _, err := toml.DecodeFile(recordPath, &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Pack) != 1 {
		t.Fatalf("record %+v", rec)
	}
	p := rec.Pack[0]
	if p.Ref != "acme/review" || p.Version != "1.2.3" || p.Digest != digest || !slices.Equal(p.Skills, []string{"review", "review-lint"}) || len(p.File) != 4 {
		t.Errorf("record entry %+v", p)
	}
	bodies := map[string][]byte{}
	for _, f := range reviewPack("1.2.3") {
		bodies[f.Path] = f.Body
	}
	for _, f := range p.File {
		sum := sha256.Sum256(bodies[f.Path])
		if f.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("record sha256 of %s = %s", f.Path, f.SHA256)
		}
	}

	must(t, "validate")

	before, _ := os.ReadFile(recordPath)
	again, err := install(t, srv, "acme/review", "1.2.3", "--session", "s-1")
	if err != nil || !strings.Contains(again, "already installed") {
		t.Errorf("second install: %v\n%s", err, again)
	}
	if after, _ := os.ReadFile(recordPath); string(after) != string(before) {
		t.Error("an already-installed pack rewrote packs.toml")
	}

	docsDigest := seedPack(srv, "acme/docs", "0.1.0", singlePack("docs", "0.1.0"))
	raw, err := install(t, srv, "acme/docs", "0.1.0", "--session", "s-1", "--json")
	if err != nil {
		t.Fatalf("install --json: %v\n%s", err, raw)
	}
	var got struct {
		Ref, Name, Version, Digest, Record string
		Skills                             []string
		Files                              int
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("json %v:\n%s", err, raw)
	}
	if got.Ref != "acme/docs" || got.Name != "docs" || got.Version != "0.1.0" || got.Digest != docsDigest ||
		!slices.Equal(got.Skills, []string{"docs"}) || got.Files != 2 || got.Record != ".satelle/packs.toml" {
		t.Errorf("json %+v", got)
	}
	if b, _ := os.ReadFile(recordPath); !strings.Contains(string(b), "acme/review") || !strings.Contains(string(b), "acme/docs") {
		t.Errorf("packs.toml after the second pack:\n%s", b)
	}
}

func TestPackInstallRequiresTreeSeat(t *testing.T) {
	dir, srv, _ := packRepo(t)
	plan := createID(t, "story", "create", "--title", "planning", "--workflow", "planned")
	must(t, "story", "set", plan, "--status", "st-plan")
	must(t, "story", "seat", plan, "--session", "s-3")
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"unseated", []string{"--session", "s-2"}, "Seat one with"},
		{"attachments-only seat", []string{"--session", "s-3"}, "produces an attachment"},
		{"no session", nil, "no session id"},
	} {
		_, err := install(t, srv, "acme/review", "1.2.3", c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		for _, p := range []string{"skills/review", "packs.toml"} {
			if _, err := os.Lstat(filepath.Join(dir, ".satelle", p)); !os.IsNotExist(err) {
				t.Errorf("%s: .satelle/%s exists (%v)", c.name, p, err)
			}
		}
		noStage(t, dir)
	}
}

func TestPackInstallGateCanonicalAndLocked(t *testing.T) {
	refused := func(t *testing.T, dir string, err error, want ...string) {
		t.Helper()
		if err == nil {
			t.Fatal("install was allowed")
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("error %q does not contain %q", err, w)
			}
		}
		for _, p := range []string{"skills/review", "skills/review-lint"} {
			if _, err := os.Lstat(filepath.Join(dir, ".satelle", p)); !os.IsNotExist(err) {
				t.Errorf(".satelle/%s exists (%v)", p, err)
			}
		}
		noStage(t, dir)
	}

	t.Run("symlinked skills", func(t *testing.T) {
		dir, srv, _ := packRepo(t)
		target := t.TempDir()
		skills := filepath.Join(dir, ".satelle", "skills")
		ents, err := os.ReadDir(skills)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if err := os.Rename(filepath.Join(skills, e.Name()), filepath.Join(target, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(skills); err != nil {
			t.Fatal(err)
		}
		before := tree(t, target)
		if err := os.Symlink(target, skills); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		_, err = install(t, srv, "acme/review", "1.2.3", "--session", "s-1")
		if err == nil || !strings.Contains(err.Error(), "outside the worktree") {
			t.Fatalf("install through a symlinked skills dir: %v", err)
		}
		if after := tree(t, target); !maps.Equal(before, after) {
			t.Error("the symlink target changed")
		}
		noStage(t, dir)
	})

	t.Run("symlinked packs.toml", func(t *testing.T) {
		dir, srv, _ := packRepo(t)
		target := filepath.Join(t.TempDir(), "elsewhere.toml")
		if err := os.WriteFile(target, []byte("# theirs\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, ".satelle", "packs.toml")); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		_, err := install(t, srv, "acme/review", "1.2.3", "--session", "s-1")
		if err == nil || !strings.Contains(err.Error(), "outside the worktree") {
			t.Fatalf("install through a symlinked packs.toml: %v", err)
		}
		if b, _ := os.ReadFile(target); string(b) != "# theirs\n" {
			t.Errorf("the symlink target changed: %q", b)
		}
		for _, p := range []string{"skills/review", "skills/review-lint"} {
			if _, err := os.Lstat(filepath.Join(dir, ".satelle", p)); !os.IsNotExist(err) {
				t.Errorf(".satelle/%s exists (%v)", p, err)
			}
		}
		noStage(t, dir)
	})

	t.Run("locked script inside the bundle", func(t *testing.T) {
		dir, srv, _ := packRepo(t)
		setLocked(t, dir, `[".satelle/skills/*/scripts/*.sh"]`)
		_, err := install(t, srv, "acme/review", "1.2.3", "--session", "s-1")
		refused(t, dir, err, ".satelle/skills/review-lint/scripts/check.sh", "locked path")
		if _, err := os.Lstat(filepath.Join(dir, ".satelle", "packs.toml")); !os.IsNotExist(err) {
			t.Errorf("packs.toml written: %v", err)
		}
	})

	t.Run("slash-free lock on pack.toml", func(t *testing.T) {
		dir, srv, _ := packRepo(t)
		setLocked(t, dir, `["pack.toml"]`)
		_, err := install(t, srv, "acme/review", "1.2.3", "--session", "s-1")
		refused(t, dir, err, ".satelle/skills/review/pack.toml", "locked path")
	})

	t.Run("locked .satelle", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		m := newMachine(t)
		writeGatedWorkflow(t, m.dir)
		id := createID(t, "story", "create", "--title", "gated", "--workflow", "gated")
		must(t, "story", "set", id, "--status", "st-two")
		must(t, "story", "seat", id, "--session", "s-1")
		seedPack(srv, "acme/review", "1.2.3", reviewPack("1.2.3"))
		_, err := install(t, srv, "acme/review", "1.2.3", "--session", "s-1")
		refused(t, m.dir, err, "locked path")
	})
}

func TestPackInstallRefusesExistingUnrecordedSkill(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		record bool
	}{
		{"hand-written skill directory", map[string]string{"skills/review-lint/SKILL.md": string(skillBody("review-lint"))}, false},
		{"single-file skill", map[string]string{"skills/review-lint.md": string(skillBody("review-lint"))}, false},
		{"recorded by another pack", map[string]string{
			"skills/review-lint/SKILL.md": string(skillBody("review-lint")),
			"packs.toml":                  "[[pack]]\nref = \"acme/other\"\nname = \"other\"\nversion = \"1.0.0\"\ndigest = \"d\"\nskills = [\"review-lint\"]\n",
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, srv, _ := packRepo(t)
			for rel, body := range c.files {
				p := filepath.Join(dir, ".satelle", filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := install(t, srv, "acme/review", "1.2.3", "--session", "s-1")
			if err == nil || !strings.Contains(err.Error(), ".satelle/skills/review-lint") || !strings.Contains(err.Error(), "not recorded") {
				t.Fatalf("install over an existing skill: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, ".satelle", "skills", "review")); !os.IsNotExist(err) {
				t.Errorf("the entry directory was written: %v", err)
			}
			for rel, body := range c.files {
				if b, _ := os.ReadFile(filepath.Join(dir, ".satelle", filepath.FromSlash(rel))); string(b) != body {
					t.Errorf("%s changed: %q", rel, b)
				}
			}
			if _, err := os.Lstat(filepath.Join(dir, ".satelle", "packs.toml")); !c.record && !os.IsNotExist(err) {
				t.Errorf("packs.toml written: %v", err)
			}
			noStage(t, dir)
		})
	}

	t.Run("another version of an installed pack", func(t *testing.T) {
		dir, srv, _ := packRepo(t)
		must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
		seedPack(srv, "acme/review", "1.3.0", reviewPack("1.3.0"))
		before := tree(t, filepath.Join(dir, ".satelle"))
		_, err := install(t, srv, "acme/review", "1.3.0", "--session", "s-1")
		if err == nil || !strings.Contains(err.Error(), "1.2.3") {
			t.Fatalf("install of another version: %v", err)
		}
		if after := tree(t, filepath.Join(dir, ".satelle")); !maps.Equal(before, after) {
			t.Error("a refused install changed .satelle")
		}
	})
}

func TestPackInstallWritesNothingOnFailure(t *testing.T) {
	dir, srv, _ := packRepo(t)
	seedPack(srv, "acme/base", "1.0.0", singlePack("base", "1.0.0"))
	must(t, "pack", "install", "acme/base", "--version", "1.0.0", "--server", srv.URL, "--session", "s-1")
	skills := filepath.Join(dir, ".satelle", "skills")
	before := tree(t, skills)
	record, err := os.ReadFile(filepath.Join(dir, ".satelle", "packs.toml"))
	if err != nil {
		t.Fatal(err)
	}

	tampered, _ := fakeserver.PackBundle("review", "1.2.3", reviewPack("1.2.3"))
	tampered.Files[3].Body = []byte("#!/bin/sh\necho evil\n")
	srv.SeedPack("acme/tampered", "1.2.3", tampered)
	badDigest, _ := fakeserver.PackBundle("review", "1.2.3", reviewPack("1.2.3"))
	badDigest.Version.Manifest.Digest = strings.Repeat("a", 64)
	srv.SeedPack("acme/baddigest", "1.2.3", badDigest)
	seedPack(srv, "acme/unsafe", "1.2.3", append(reviewPack("1.2.3"), fakeserver.PackFile{Path: "../escape/SKILL.md", Body: []byte("x")}))
	wrongVersion, _ := fakeserver.PackBundle("review", "2.0.0", reviewPack("2.0.0"))
	srv.SeedPack("acme/review", "1.2.4", wrongVersion)
	seedPack(srv, "acme/private", "1.2.3", reviewPack("1.2.3"), "tok-other")

	for _, c := range []struct {
		name, ref, version string
		want               []string
	}{
		{"tampered file", "acme/tampered", "1.2.3", []string{"review-lint/scripts/check.sh", "sha256"}},
		{"bad digest", "acme/baddigest", "1.2.3", []string{"digest"}},
		{"unsafe path", "acme/unsafe", "1.2.3", []string{"../escape/SKILL.md"}},
		{"version mismatch", "acme/review", "1.2.4", []string{"2.0.0", "1.2.4"}},
		{"not readable", "acme/private", "1.2.3", []string{"not found"}},
		{"unknown ref", "acme/nothing", "1.2.3", []string{"not found"}},
	} {
		_, err := install(t, srv, c.ref, c.version, "--session", "s-1")
		if err == nil {
			t.Errorf("%s: install succeeded", c.name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q does not contain %q", c.name, err, w)
			}
		}
		if after := tree(t, skills); !maps.Equal(before, after) {
			t.Errorf("%s: .satelle/skills changed", c.name)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, ".satelle", "packs.toml")); string(b) != string(record) {
			t.Errorf("%s: packs.toml changed", c.name)
		}
		noStage(t, dir)
	}
}
