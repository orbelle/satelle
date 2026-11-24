package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeSkillFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsFraming(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want bool
	}{
		"a framing principle":       {"---\ntype: principle\nframing: true\ndescription: d\n---\n\nbody\n", true},
		"a quoted marker":           {"---\ntype: principle\nframing: \"true\"\n---\n", true},
		"a skill with the marker":   {"---\ntype: skill\nframing: true\n---\n\nbody\n", false},
		"another value":             {"---\ntype: principle\nframing: yes\n---\n", false},
		"a principle with no key":   {"---\ntype: principle\n---\n", false},
		"the marker with no type":   {"---\nframing: true\n---\n", false},
		"no frontmatter":            {"type: principle\nframing: true\n", false},
		"the marker below the body": {"---\ntype: principle\n---\n\nframing: true\n", false},
	} {
		sk, err := ParseSkill("f", tc.text)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := sk.IsFraming(); got != tc.want {
			t.Errorf("%s: IsFraming %v, want %v", name, got, tc.want)
		}
	}
}

func TestLoadSkillsDirectories(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkillFile(t, dir, "flat.md", "---\nname: flat\ntype: skill\ndescription: single file\n---\n\nflat body\n")
	writeSkillFile(t, dir, "dir-check/SKILL.md", "---\nname: dir-check\ntype: check\ndescription: directory check\n---\n\n# Check\n\n```sh\nsh scripts/run.sh\n```\n")
	writeSkillFile(t, dir, "dir-check/scripts/run.sh", "echo ok\n")
	writeSkillFile(t, dir, "dir-prose/SKILL.md", "---\nname: dir-prose\ntype: skill\ndescription: directory prose\n---\n\nFollow the rubric.\n")
	writeSkillFile(t, dir, "empty-sub/notes.md", "---\nname: empty-sub\ntype: skill\n---\n\nnot a skill\n")
	writeSkillFile(t, dir, "testdata/SKILL.md", "---\nname: testdata\ntype: skill\n---\n\nignored\n")

	got, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d skills (%v), want 3", len(got), got)
	}
	flat := got["flat"]
	if flat.IsDir() || flat.Dir != "" || flat.Path != filepath.Join(dir, "flat.md") {
		t.Errorf("flat skill: %+v", flat)
	}
	dc := got["dir-check"]
	if !dc.IsDir() || dc.Name != "dir-check" || dc.Dir != filepath.Join(dir, "dir-check") || dc.Path != filepath.Join(dir, "dir-check", SkillFile) {
		t.Errorf("dir-check paths: %+v", dc)
	}
	if !dc.IsCheck() || dc.Meta[KeyDescription] != "directory check" || dc.Script != "sh scripts/run.sh\n" {
		t.Errorf("dir-check parsed fields: %+v", dc)
	}
	dp := got["dir-prose"]
	if !dp.IsDir() || dp.IsCheck() || dp.Dir != filepath.Join(dir, "dir-prose") || !strings.Contains(dp.Body, "Follow the rubric.") {
		t.Errorf("dir-prose: %+v", dp)
	}
	if _, ok := got["empty-sub"]; ok {
		t.Error("subdirectory without SKILL.md was loaded as a skill")
	}
	if _, ok := got["testdata"]; ok {
		t.Error("testdata directory was loaded as a skill")
	}
}

func TestLoadSkillsDuplicate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkillFile(t, dir, "dup.md", "---\nname: dup\ntype: skill\ndescription: file\n---\n\nfile\n")
	writeSkillFile(t, dir, "dup/SKILL.md", "---\nname: dup\ntype: skill\ndescription: dir\n---\n\ndir\n")

	got, err := LoadSkills(dir)
	if err == nil {
		t.Fatal("LoadSkills accepted duplicate skill")
	}
	msg := err.Error()
	for _, want := range []string{"dup", "skills/dup.md", "skills/dup/SKILL.md"} {
		if !strings.Contains(msg, want) {
			t.Errorf("duplicate error %q lacks %q", msg, want)
		}
	}
	if _, ok := got["dup"]; !ok {
		t.Error("duplicate skill was dropped from map instead of retained")
	}
}

func TestSkillDirPathProblemsMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkillFile(t, dir, "good/SKILL.md", "---\nname: good\ntype: check\ndescription: ok\n---\n\nUse `scripts/run.sh` with `assets/rules.txt` and `scripts/<file>`.\n\n```sh\nsh scripts/run.sh assets/rules.txt\n# scripts/commented.sh\n```\n")
	writeSkillFile(t, dir, "good/scripts/run.sh", "exit 0\n")
	writeSkillFile(t, dir, "good/assets/rules.txt", "ok\n")

	writeSkillFile(t, dir, "missing/SKILL.md", "---\nname: missing\ntype: check\ndescription: bad\n---\n\nRun `scripts/prose-missing.sh` and read `assets/prose-missing.txt`.\nIgnore `scripts/<placeholder>` and `scripts/` and `assets/`.\n\n```sh\nsh scripts/sh-missing.sh assets/sh-missing.txt\n```\n")

	writeSkillFile(t, dir, "flat.md", "---\nname: flat\ntype: check\ndescription: single file\n---\n\nRun `scripts/repo-level.sh`.\n\n```sh\nsh scripts/repo-level.sh\n```\n")

	got, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if probs := got["good"].Problems(); len(probs) != 0 {
		t.Errorf("good skill problems: %v", probs)
	}
	if probs := got["flat"].Problems(); len(probs) != 0 {
		t.Errorf("single-file skill problems: %v", probs)
	}
	probs := got["missing"].Problems()
	joined := errors.Join(probs...).Error()
	for _, want := range []string{
		"skill missing:",
		"scripts/prose-missing.sh",
		"assets/prose-missing.txt",
		"scripts/sh-missing.sh",
		"assets/sh-missing.txt",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing problems lack %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "<placeholder>") {
		t.Errorf("placeholder was reported:\n%s", joined)
	}
	if len(probs) != 4 {
		t.Errorf("got %d missing problems, want 4:\n%s", len(probs), joined)
	}
}

func TestSkillDirPathProblemsForeign(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeSkillFile(t, dir, "self-ok/SKILL.md", "---\nname: self-ok\ntype: check\ndescription: ok\n---\n\nAfter this, follow `.satelle/skills/other/SKILL.md` and run `scripts/own.sh`.\n\n```sh\nsh scripts/own.sh\n```\n")
	writeSkillFile(t, dir, "self-ok/scripts/own.sh", "exit 0\n")

	writeSkillFile(t, dir, "foreign/SKILL.md", "---\nname: foreign\ntype: check\ndescription: bad\n---\n\nSee `.satelle/scripts/shared.sh`, `.satelle/skills/other/scripts/run.sh`, `skills/other/assets/data.txt`, `../other/scripts/run.sh`, `scripts/../../other/scripts/run.sh`.\n\n```sh\ncat .satelle/skills/other/SKILL.md\n```\n")

	got, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if probs := got["self-ok"].Problems(); len(probs) != 0 {
		t.Errorf("self-ok skill problems: %v", probs)
	}
	probs := got["foreign"].Problems()
	joined := errors.Join(probs...).Error()
	for _, want := range []string{
		".satelle/scripts/shared.sh",
		".satelle/skills/other/scripts/run.sh",
		"skills/other/assets/data.txt",
		"../other/scripts/run.sh",
		"scripts/../../other/scripts/run.sh",
		".satelle/skills/other/SKILL.md",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("foreign problems lack %q:\n%s", want, joined)
		}
	}
	for _, p := range probs {
		if !strings.Contains(p.Error(), "skill foreign:") || !strings.Contains(p.Error(), "not its own") {
			t.Errorf("unexpected foreign error wording: %v", p)
		}
	}
	if len(probs) != 6 {
		t.Errorf("got %d foreign problems, want 6:\n%s", len(probs), joined)
	}
}

func TestShellBlockLinesReturnsShellFenceLinesWithFileLineNumbers(t *testing.T) {
	text := "---\nname: x\n---\n```sh\na 1\n```\n```toml\nb = 2\n```\n```bash\nc 3\n```\n````check\nd \"4 5\" # e\n````\n"
	got := ShellBlockLines(text)
	want := []ShellLine{{Line: 5, Words: []string{"a", "1"}}, {Line: 11, Words: []string{"c", "3"}}, {Line: 14, Words: []string{"d", "4 5"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ShellBlockLines = %+v, want %+v", got, want)
	}
}

func TestShellScriptLinesSkipsQuotedHeredocBodies(t *testing.T) {
	text := "cat <<'EOF'\nquoted body\nEOF\na 5\ncat <<-\"X\" <<\\Y\n\tq1\n\tX\nq2\nY\ncat <<Z\nopen body\nZ\necho a<<<b\nc 15"
	var got []int
	for _, l := range ShellScriptLines(text) {
		got = append(got, l.Line)
	}
	want := []int{1, 4, 5, 10, 11, 13, 14}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ShellScriptLines lines = %v, want %v", got, want)
	}
}

func TestShellBlockLinesSkipsAQuotedHeredocBodyInsideAFence(t *testing.T) {
	text := "```sh\ncat <<'EOF'\nbody\n```\n```sh\nb 6\n```\n"
	got := ShellBlockLines(text)
	want := []ShellLine{{Line: 2, Words: []string{"cat", "EOF"}}, {Line: 6, Words: []string{"b", "6"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ShellBlockLines = %+v, want %+v", got, want)
	}
}
