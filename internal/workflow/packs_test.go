package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestLoadPacksValid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkillFile(t, dir, "my-pack/SKILL.md", "---\nname: my-pack\ntype: skill\ndescription: wiring guide\n---\n\nWiring guide.\n")
	writeSkillFile(t, dir, "my-pack/pack.toml", `
name = "my-pack"
version = "1.2.3"
skills = ["my-pack", "member-a", "member-b"]

[needs]
scripts = ["sh", "git"]
agents = ["reviewer"]
network = ["api.example.com"]
secrets = ["DEPLOY_TOKEN"]
`)
	writeSkillFile(t, dir, "minimal-pack/SKILL.md", "---\nname: minimal-pack\ntype: skill\ndescription: minimal wiring guide\n---\n\nMinimal.\n")
	writeSkillFile(t, dir, "minimal-pack/pack.toml", `
name = "minimal"
version = "0.1.0"
skills = ["member-a"]
`)
	writeSkillFile(t, dir, "member-a/SKILL.md", "---\nname: member-a\ntype: skill\ndescription: member a\n---\n\nA.\n")
	writeSkillFile(t, dir, "member-b/SKILL.md", "---\nname: member-b\ntype: check\ndescription: member b\n---\n\n```sh\nexit 0\n```\n")
	writeSkillFile(t, dir, "testdata/SKILL.md", "---\nname: testdata\ntype: skill\ndescription: ignored\n---\n\nIgnored.\n")
	writeSkillFile(t, dir, "testdata/pack.toml", "this is not valid toml = [\n")

	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	packs, unknown, err := LoadPacks(dir)
	if err != nil {
		t.Fatalf("LoadPacks: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown keys = %+v, want none", unknown)
	}
	if len(packs) != 2 {
		t.Fatalf("loaded %d packs (%+v), want 2", len(packs), packs)
	}

	pk := packs["my-pack"]
	if pk.File != filepath.Join(dir, "my-pack", PackFile) {
		t.Errorf("file = %q", pk.File)
	}
	if pk.Name != "my-pack" || pk.Version != "1.2.3" {
		t.Errorf("name/version = %q / %q", pk.Name, pk.Version)
	}
	if !slices.Equal(pk.Skills, []string{"my-pack", "member-a", "member-b"}) {
		t.Errorf("skills = %v", pk.Skills)
	}
	if !slices.Equal(pk.Needs.Scripts, []string{"sh", "git"}) ||
		!slices.Equal(pk.Needs.Agents, []string{"reviewer"}) ||
		!slices.Equal(pk.Needs.Network, []string{"api.example.com"}) ||
		!slices.Equal(pk.Needs.Secrets, []string{"DEPLOY_TOKEN"}) {
		t.Errorf("needs = %+v", pk.Needs)
	}

	lookup := func(name string) (Skill, bool) { s, ok := skills[name]; return s, ok }
	if errs := pk.Validate(lookup); len(errs) != 0 {
		t.Errorf("my-pack Validate: %v", errs)
	}
	min := packs["minimal-pack"]
	if errs := min.Validate(lookup); len(errs) != 0 {
		t.Errorf("minimal-pack Validate: %v", errs)
	}
	if _, ok := packs["testdata"]; ok {
		t.Error("testdata/pack.toml was loaded")
	}
}

func TestLoadPacksUnknownKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkillFile(t, dir, "my-pack/SKILL.md", "---\nname: my-pack\ntype: skill\ndescription: wiring\n---\n\nGuide.\n")
	writeSkillFile(t, dir, "my-pack/pack.toml", `
name = "my-pack"
version = "1.0.0"
skills = ["member-a"]
description = "extra top level"
future_flag = true

[needs]
scripts = ["sh"]
workflows = ["delivery"]

[extra]
level = 2
`)
	writeSkillFile(t, dir, "member-a/SKILL.md", "---\nname: member-a\ntype: skill\ndescription: a\n---\n\nA.\n")

	packs, unknown, err := LoadPacks(dir)
	if err != nil {
		t.Fatalf("LoadPacks: %v", err)
	}
	if len(packs) != 1 {
		t.Fatalf("loaded %d packs, want 1", len(packs))
	}
	packPath := filepath.Join(dir, "my-pack", PackFile)
	want := []UnknownKey{
		{File: packPath, Key: "description", Value: `"extra top level"`},
		{File: packPath, Key: "future_flag", Value: "true"},
		{File: packPath, Table: "extra", Key: "level", Value: "2"},
		{File: packPath, Table: "needs", Key: "workflows", Value: `["delivery"]`},
	}
	if !slices.Equal(unknown, want) {
		t.Fatalf("unknown keys:\ngot:  %+v\nwant: %+v", unknown, want)
	}
}

func TestPackValidateProblems(t *testing.T) {
	skills := map[string]Skill{
		"dir-a": {Name: "dir-a", Dir: "/repo/.satelle/skills/dir-a", Path: "/repo/.satelle/skills/dir-a/SKILL.md"},
		"flat":  {Name: "flat", Path: "/repo/.satelle/skills/flat.md"},
	}
	lookup := func(name string) (Skill, bool) { s, ok := skills[name]; return s, ok }
	file := "/repo/.satelle/skills/my-pack/pack.toml"

	cases := []struct {
		name  string
		pack  Pack
		wants []string
	}{
		{
			name:  "missing name",
			pack:  Pack{File: file, Version: "1.0.0", Skills: []string{"dir-a"}},
			wants: []string{file, "name is required"},
		},
		{
			name:  "blank name",
			pack:  Pack{File: file, Name: "   ", Version: "1.0.0", Skills: []string{"dir-a"}},
			wants: []string{file, "name is required"},
		},
		{
			name:  "missing version",
			pack:  Pack{File: file, Name: "p", Skills: []string{"dir-a"}},
			wants: []string{file, "version is required"},
		},
		{
			name:  "missing skills",
			pack:  Pack{File: file, Name: "p", Version: "1.0.0"},
			wants: []string{file, "skills is required"},
		},
		{
			name:  "empty entry in skills",
			pack:  Pack{File: file, Name: "p", Version: "1.0.0", Skills: []string{"dir-a", " "}},
			wants: []string{file, "skills contains an empty skill name"},
		},
		{
			name:  "traversal entry in skills",
			pack:  Pack{File: file, Name: "p", Version: "1.0.0", Skills: []string{"../other", "a/b", `a\b`, ".", "..", " dir-a"}},
			wants: []string{file, `../other`, `a/b`, `a\\b`, `"."`, `".."`, `" dir-a"`, "not a valid skill directory name"},
		},
		{
			name:  "duplicate skill in skills",
			pack:  Pack{File: file, Name: "p", Version: "1.0.0", Skills: []string{"dir-a", "dir-a"}},
			wants: []string{file, `skills lists "dir-a" more than once`},
		},
		{
			name:  "missing member skill",
			pack:  Pack{File: file, Name: "p", Version: "1.0.0", Skills: []string{"ghost"}},
			wants: []string{file, `member skill "ghost" does not exist`},
		},
		{
			name:  "single-file member skill",
			pack:  Pack{File: file, Name: "p", Version: "1.0.0", Skills: []string{"flat"}},
			wants: []string{file, `member skill "flat" is a single-file skill`, "skills/flat.md", "skills/flat/SKILL.md"},
		},
		{
			name: "empty entries in needs",
			pack: Pack{
				File: file, Name: "p", Version: "1.0.0", Skills: []string{"dir-a"},
				Needs: PackNeeds{
					Scripts: []string{"sh", ""},
					Agents:  []string{" "},
					Network: []string{"example.com", "\t"},
					Secrets: []string{""},
				},
			},
			wants: []string{file, "needs.scripts[1] is empty", "needs.agents[0] is empty", "needs.network[1] is empty", "needs.secrets[0] is empty"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.pack.Validate(lookup)
			if len(errs) == 0 {
				t.Fatalf("Validate returned no errors")
			}
			joined := errors.Join(errs...).Error()
			for _, want := range tc.wants {
				if !strings.Contains(joined, want) {
					t.Errorf("errors lack %q:\n%s", want, joined)
				}
			}
		})
	}
}

func TestLoadPacksMissingEntrySkillAndSymlink(t *testing.T) {
	t.Run("missing entry SKILL.md", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "skills")
		writeSkillFile(t, dir, "orphan/pack.toml", "name = \"orphan\"\nversion = \"1.0.0\"\nskills = [\"a\"]\n")
		packs, _, err := LoadPacks(dir)
		if err == nil {
			t.Fatal("LoadPacks accepted pack.toml without SKILL.md")
		}
		if len(packs) != 0 {
			t.Errorf("packs = %+v, want empty", packs)
		}
		msg := err.Error()
		for _, want := range []string{filepath.Join("orphan", PackFile), "skills/orphan", SkillFile} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q lacks %q", msg, want)
			}
		}
	})

	t.Run("pack.toml is a directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "skills")
		writeSkillFile(t, dir, "dirpack/SKILL.md", "---\nname: dirpack\ntype: skill\n---\n\nGuide.\n")
		if err := os.MkdirAll(filepath.Join(dir, "dirpack", PackFile), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, err := LoadPacks(dir)
		if err == nil || !strings.Contains(err.Error(), "is not a regular file") || !strings.Contains(err.Error(), filepath.Join("dirpack", PackFile)) {
			t.Fatalf("LoadPacks on directory pack.toml: %v", err)
		}
	})

	t.Run("syntax and type errors", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "skills")
		writeSkillFile(t, dir, "bad-syntax/SKILL.md", "---\nname: bad-syntax\ntype: skill\n---\n\nGuide.\n")
		writeSkillFile(t, dir, "bad-syntax/pack.toml", "name = [\n")
		writeSkillFile(t, dir, "bad-type/SKILL.md", "---\nname: bad-type\ntype: skill\n---\n\nGuide.\n")
		writeSkillFile(t, dir, "bad-type/pack.toml", "name = \"t\"\nversion = \"1.0.0\"\nskills = \"not-a-list\"\n")

		_, _, err := LoadPacks(dir)
		if err == nil {
			t.Fatal("LoadPacks accepted invalid TOML")
		}
		msg := err.Error()
		for _, want := range []string{filepath.Join("bad-syntax", PackFile), filepath.Join("bad-type", PackFile)} {
			if !strings.Contains(msg, want) {
				t.Errorf("error lacks %q:\n%s", want, msg)
			}
		}
	})

	t.Run("symlinked pack.toml refused and symlinked subdir skipped", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks require privileges on windows")
		}
		outside := t.TempDir()
		secretFile := filepath.Join(outside, "secret.toml")
		if err := os.WriteFile(secretFile, []byte("leaked = \"top-secret\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		outsideDir := filepath.Join(outside, "ext-pack")
		writeSkillFile(t, outside, "ext-pack/SKILL.md", "---\nname: ext-pack\ntype: skill\n---\n\nExt.\n")
		writeSkillFile(t, outside, "ext-pack/pack.toml", "name = \"ext\"\nversion = \"1.0.0\"\nskills = [\"ext-pack\"]\nleaked = \"outside-dir\"\n")

		dir := filepath.Join(t.TempDir(), "skills")
		writeSkillFile(t, dir, "sym-pack/SKILL.md", "---\nname: sym-pack\ntype: skill\n---\n\nGuide.\n")
		if err := os.Symlink(secretFile, filepath.Join(dir, "sym-pack", PackFile)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideDir, filepath.Join(dir, "ext-pack")); err != nil {
			t.Fatal(err)
		}

		packs, unknown, err := LoadPacks(dir)
		if err == nil {
			t.Fatal("LoadPacks accepted symlinked pack.toml")
		}
		msg := err.Error()
		if !strings.Contains(msg, filepath.Join("sym-pack", PackFile)) || !strings.Contains(msg, "symlink is not allowed") {
			t.Errorf("unexpected symlink error: %s", msg)
		}
		if strings.Contains(msg, "top-secret") || strings.Contains(msg, "outside-dir") {
			t.Errorf("symlink target contents leaked in error: %s", msg)
		}
		if len(packs) != 0 || len(unknown) != 0 {
			t.Errorf("packs=%+v unknown=%+v, want empty", packs, unknown)
		}
	})
}

func TestParsePackMatchesLoadPacks(t *testing.T) {
	text := `
name = "my-pack"
version = "1.2.3"
skills = ["member-a"]
extra = "kept for validate"

[needs]
scripts = ["sh"]
`
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkillFile(t, dir, "my-pack/SKILL.md", "---\nname: my-pack\ntype: skill\ndescription: d\n---\n\nD.\n")
	writeSkillFile(t, dir, "my-pack/pack.toml", text)
	packs, _, err := LoadPacks(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded := packs["my-pack"]
	parsed, err := ParsePack(loaded.File, []byte(text))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	if parsed.File != loaded.File || parsed.Name != loaded.Name || parsed.Version != loaded.Version ||
		!slices.Equal(parsed.Skills, loaded.Skills) || !slices.Equal(parsed.Needs.Scripts, loaded.Needs.Scripts) {
		t.Errorf("ParsePack = %+v, LoadPacks = %+v", parsed, loaded)
	}
	if _, err := ParsePack("review/pack.toml", []byte("name = [\n")); err == nil || !strings.Contains(err.Error(), "review/pack.toml") {
		t.Errorf("malformed pack.toml: %v", err)
	}
}
