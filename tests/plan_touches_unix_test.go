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

const planTouchesSkill = ".satelle/skills/satelle-plan-touches-check.md"

func planTouchesScript(t *testing.T) string {
	t.Helper()
	body := repoText(t, planTouchesSkill)
	_, rest, ok := strings.Cut(body, "```check\n")
	if !ok {
		t.Fatalf("%s has no check block", planTouchesSkill)
	}
	script, _, ok := strings.Cut(rest, "\n```")
	if !ok {
		t.Fatalf("%s check block is not closed", planTouchesSkill)
	}
	p := filepath.Join(t.TempDir(), "check.sh")
	if err := os.WriteFile(p, []byte(script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func planTouchesStub(t *testing.T, plan string) string {
	t.Helper()
	bin := t.TempDir()
	planFile := filepath.Join(bin, "plan.md")
	if err := os.WriteFile(planFile, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nif [ \"$1 $2\" = \"story attachment\" ] && [ \"$4\" = plan ]; then cat '" + planFile + "'; exit 0; fi\necho \"unexpected satelle $*\" >&2\nexit 2\n"
	if err := os.WriteFile(filepath.Join(bin, "satelle"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func planTouchesRepo(t *testing.T) (dir, base string) {
	t.Helper()
	dir = t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	writeRel(t, dir, "web/a.go", "package web\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "base")
	return dir, gittest.Git(t, dir, "rev-parse", "HEAD")
}

func writeRel(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runPlanTouches(t *testing.T, script, stubDir, cwd string, env map[string]string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", script)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), gittest.Env()...)
	cmd.Env = append(cmd.Env, "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"), "SATELLE_ITEM=sto_test")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
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

const touchesPlan = "# Plan\n\n## Touches\n- `web/a.go`\n- web/gen/*.go\n- docs/\n\n## Risks\n- none\n"

func TestPlanTouchesRejectsATargetPathOutsideTouches(t *testing.T) {
	script := planTouchesScript(t)
	stub := planTouchesStub(t, touchesPlan)
	tgt, base := planTouchesRepo(t)
	ctl := t.TempDir()
	writeRel(t, tgt, "web/a.go", "package web\n\nfunc A() {}\n")
	writeRel(t, tgt, "web/gen/b.go", "package gen\n")
	writeRel(t, tgt, "docs/guide/x.md", "x\n")
	writeRel(t, tgt, "secret/out.go", "package secret\n")
	gittest.Git(t, tgt, "add", "-A")
	gittest.Git(t, tgt, "commit", "-q", "-m", "work")
	code, out := runPlanTouches(t, script, stub, ctl, map[string]string{"SATELLE_BASE": base, "SATELLE_WORKTREE": tgt})
	if code == 0 {
		t.Fatalf("an out-of-scope target path was accepted:\n%s", out)
	}
	if !strings.Contains(out, "secret/out.go") {
		t.Errorf("the rejection does not name secret/out.go:\n%s", out)
	}
	for _, in := range []string{"web/a.go", "web/gen/b.go", "docs/guide/x.md"} {
		if strings.Contains(out, "  "+in+"\n") {
			t.Errorf("the rejection names %s, which Touches covers:\n%s", in, out)
		}
	}
}

func TestPlanTouchesAcceptsTargetChangesWithinTouches(t *testing.T) {
	script := planTouchesScript(t)
	stub := planTouchesStub(t, touchesPlan)
	tgt, base := planTouchesRepo(t)
	writeRel(t, tgt, "web/a.go", "package web\n\nfunc A() {}\n")
	writeRel(t, tgt, "web/gen/b.go", "package gen\n")
	writeRel(t, tgt, "docs/guide/x.md", "x\n")
	gittest.Git(t, tgt, "add", "-A")
	gittest.Git(t, tgt, "commit", "-q", "-m", "work")
	code, out := runPlanTouches(t, script, stub, t.TempDir(), map[string]string{"SATELLE_BASE": base, "SATELLE_WORKTREE": tgt})
	if code != 0 {
		t.Fatalf("changes within Touches were refused (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "all 3 changed path(s)") {
		t.Errorf("accept notes do not count the 3 changed paths:\n%s", out)
	}
}

func TestPlanTouchesReadsTheWorkingDirectoryForASelfItem(t *testing.T) {
	script := planTouchesScript(t)
	stub := planTouchesStub(t, touchesPlan)
	repo, base := planTouchesRepo(t)
	writeRel(t, repo, "secret/out.go", "package secret\n")
	gittest.Git(t, repo, "add", "-A")
	gittest.Git(t, repo, "commit", "-q", "-m", "work")
	code, out := runPlanTouches(t, script, stub, repo, map[string]string{"SATELLE_BASE": base})
	if code == 0 || !strings.Contains(out, "secret/out.go") {
		t.Fatalf("a self item's out-of-scope path was not refused by name (exit %d):\n%s", code, out)
	}
}

func TestPlanTouchesWithNoChangesJudgesOnlyTheSection(t *testing.T) {
	script := planTouchesScript(t)
	stub := planTouchesStub(t, touchesPlan)
	repo, base := planTouchesRepo(t)
	if code, out := runPlanTouches(t, script, stub, repo, map[string]string{"SATELLE_BASE": base}); code != 0 {
		t.Fatalf("an unchanged item was refused (exit %d):\n%s", code, out)
	}
	if code, out := runPlanTouches(t, script, stub, repo, nil); code != 0 {
		t.Fatalf("an item with no base was refused (exit %d):\n%s", code, out)
	}
	empty := planTouchesStub(t, "# Plan\n\n## Touches\n\n## Risks\n- none\n")
	if code, out := runPlanTouches(t, script, empty, repo, map[string]string{"SATELLE_BASE": base}); code == 0 || !strings.Contains(out, "lists no path") {
		t.Fatalf("a Touches section with no path was accepted (exit %d):\n%s", code, out)
	}
}
