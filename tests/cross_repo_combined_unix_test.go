//go:build integration && unix

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

const combinedSkill = ".satelle/skills/satelle-cross-repo-combined-check.md"

func combinedScript(t *testing.T) string {
	t.Helper()
	body := repoText(t, combinedSkill)
	_, rest, ok := strings.Cut(body, "```check\n")
	if !ok {
		t.Fatalf("%s has no check block", combinedSkill)
	}
	script, _, ok := strings.Cut(rest, "\n```\n")
	if !ok {
		t.Fatalf("%s check block is not closed", combinedSkill)
	}
	p := filepath.Join(t.TempDir(), "check.sh")
	if err := os.WriteFile(p, []byte(script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

type combinedFix struct {
	web, base, head, stub string
}

func newCombinedFix(t *testing.T) *combinedFix {
	t.Helper()
	f := &combinedFix{web: t.TempDir(), stub: t.TempDir()}
	gittest.Git(t, f.web, "init", "-q", "-b", "main")
	combinedWrite(t, f.web, "web/a.go", "package web\n")
	gittest.Git(t, f.web, "add", "-A")
	gittest.Git(t, f.web, "commit", "-q", "-m", "base")
	f.base = gittest.Git(t, f.web, "rev-parse", "HEAD")
	combinedWrite(t, f.web, "web/a.go", "package web\n\nfunc A() int { return 1 }\n")
	gittest.Git(t, f.web, "add", "-A")
	gittest.Git(t, f.web, "commit", "-q", "-m", "child work")
	f.head = gittest.Git(t, f.web, "rev-parse", "HEAD")
	f.reply("story_list", `[{"id":"sto_c1","repo":"web","status":"done"}]`)
	f.reply("story_diff_sto_c1", `{"base":"`+f.base+`","head":"`+f.head+`","frozen":true,"paths":["web/a.go"]}`)
	f.reply("repo_list", `{"repos":[{"name":"self","local":{"path":"/nonexistent"}},{"name":"web","local":{"path":"`+f.web+`"}}]}`)
	stub := "#!/bin/sh\nd='" + f.stub + "'\n" +
		"case \"$1 $2\" in\n" +
		"  'story attachment') cat \"$d/combined\"; exit $? ;;\n" +
		"  'story list') cat \"$d/story_list\" ;;\n" +
		"  'story diff') cat \"$d/story_diff_$3\" ;;\n" +
		"  'repo list') cat \"$d/repo_list\" ;;\n" +
		"  *) echo \"unexpected satelle $*\" >&2; exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(f.stub, "satelle"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func combinedWrite(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *combinedFix) reply(name, body string) {
	if err := os.WriteFile(filepath.Join(f.stub, name), []byte(body), 0o600); err != nil {
		panic(err)
	}
}

func (f *combinedFix) diff(t *testing.T) string {
	t.Helper()
	return gittest.Git(t, f.web, "diff", "--no-ext-diff", "--no-textconv", f.base, f.head)
}

func (f *combinedFix) attachment(body string) string {
	return "## Children\n- sto_c1 web " + f.base + ".." + f.head + "\n\n## sto_c1\n" + body + "\n"
}

func (f *combinedFix) run(t *testing.T, combined string) (int, string) {
	t.Helper()
	f.reply("combined", combined)
	cmd := exec.Command("bash", combinedScript(t))
	cmd.Dir = t.TempDir()
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SATELLE_") && !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, gittest.Env()...)
	cmd.Env = append(cmd.Env, "PATH="+f.stub+string(os.PathListSeparator)+os.Getenv("PATH"), "SATELLE_ITEM=sto_parent")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	}
	t.Fatalf("run check: %v\n%s", err, out)
	return -1, ""
}

func TestCombinedCheckAcceptsTheRealDiff(t *testing.T) {
	f := newCombinedFix(t)
	if code, out := f.run(t, f.attachment(f.diff(t))); code != 0 {
		t.Fatalf("a combined change carrying the real diff was refused (exit %d):\n%s", code, out)
	}
	if code, out := f.run(t, f.attachment("```diff\n"+f.diff(t)+"\n```\n\n")); code != 0 {
		t.Fatalf("a fenced real diff was refused (exit %d):\n%s", code, out)
	}
}

func TestCombinedCheckRejectsABodyThatDiffersFromTheFrozenChange(t *testing.T) {
	f := newCombinedFix(t)
	forged := strings.Replace(f.diff(t), "return 1", "return 2", 1)
	if forged == f.diff(t) {
		t.Fatal("the fixture diff does not contain the line the test alters")
	}
	code, out := f.run(t, f.attachment(forged))
	if code == 0 {
		t.Fatalf("a body that differs from the frozen change was accepted:\n%s", out)
	}
	if !strings.Contains(out, "sto_c1") || !strings.Contains(out, "differs") {
		t.Errorf("the rejection does not name sto_c1 as differing:\n%s", out)
	}
	if code, out := f.run(t, f.attachment("anything at all")); code == 0 {
		t.Fatalf("an arbitrary body under a matching header was accepted:\n%s", out)
	}
}

func TestCombinedCheckRejectsAMissingSection(t *testing.T) {
	f := newCombinedFix(t)
	code, out := f.run(t, "## Children\n- sto_c1 web "+f.base+".."+f.head+"\n")
	if code == 0 || !strings.Contains(out, "no \"## sto_c1\" section") {
		t.Fatalf("a combined change without the child's section was not refused by name (exit %d):\n%s", code, out)
	}
}
