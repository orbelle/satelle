//go:build integration && unix

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/integration"
)

const externalAgents = `
[agents.planner]
command = "claude"
model = "pl-1"
tools = ["Read", "Grep", "Glob"]
[agents.coder]
command = "claude"
model = "cd-1"
tools = ["Read", "Grep", "Glob", "Edit", "Write", "Bash"]
[agents.reviewer]
command = "claude"
model = "rv-1"
tools = ["Read", "Grep", "Glob"]
`

const externalWorkflow = `name = "ext"
[states.backlog]
start = true
[states.plan]
performer = { agent = "planner", skill = "plan" }
edits = "attachments"
[states.code]
performer = { agent = "coder", skill = "code" }
[states.done]
terminal = true

[gates.built]
skill = "built"
[gates.code-ac]
skill = "judge"
agent = "reviewer"

[[transition]]
from = ["backlog"]
to = "plan"
[[transition]]
from = ["plan"]
to = "code"
[[transition]]
from = ["code"]
to = "done"
groups = [
  { mode = "sequential", gates = ["built"] },
  { mode = "sequential", gates = ["code-ac"] },
]
`

const externalPlan = "Add web/greet.go with Greet."

func externalClaude(t *testing.T, s *sandbox) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "claude"), `#!/bin/sh
model=none
prev=
for a in "$@"; do [ "$prev" = "--model" ] && model=$a; prev=$a; done
pwd -P > "$FAKE_DIR/cwd.$model"
cat > "$FAKE_DIR/prompt.$model"
case $model in
pl-1) r='{\"artifact\":{\"name\":\"plan\",\"type\":\"plan\",\"body\":\"`+externalPlan+`\"}}' ;;
cd-1) mkdir -p web && printf 'package web\n\nfunc Greet() string { return "hi" }\n' > web/greet.go && r='coded' ;;
*) r='{\"verdict\":\"accept\",\"notes\":\"accept by '"$model"'\"}' ;;
esac
printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","usage":{"input_tokens":11,"output_tokens":7}}\n' "$r"
`, 0o755)
	s.path = append(s.path, dir)
	s.env["FAKE_DIR"] = dir
	return dir
}

func fakeFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("the scripted agent left no %s: %v", name, err)
	}
	return string(b)
}

func externalTarget(t *testing.T, origin string) string {
	t.Helper()
	dir := outsideTempDir(t)
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Git(t, dir, "remote", "add", "origin", origin)
	writeFile(t, filepath.Join(dir, "web", "a.go"), "package web\n", 0o644)
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func noSatelleFiles(t *testing.T, dir string) {
	t.Helper()
	for _, p := range append([]string{".satelle"}, integration.ProjectFiles()...) {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p))); err == nil {
			t.Errorf("target %s holds %s", dir, p)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

func TestExternalControlPlaneDrivesATargetStory(t *testing.T) {
	t.Parallel()
	v, err := gitfacts.ParseVersion(gittest.Git(t, "", "version"))
	if err != nil {
		t.Fatal(err)
	}
	if !v.AtLeast(2, 44) {
		t.Skipf("git %s is older than 2.44, below which a target checkout is refused by design", v.Raw)
	}
	s := newSandboxOutsideTemp(t)
	fake := externalClaude(t, s)
	a := externalTarget(t, "https://github.com/o/a")
	b := externalTarget(t, "https://github.com/o/b")
	noSatelleFiles(t, a)
	noSatelleFiles(t, b)

	s.must("init")
	s.write(".satelle/agents.toml", externalAgents)
	s.write(".satelle/workflows/ext.toml", externalWorkflow)
	s.write(".satelle/skills/plan.md", "---\nname: plan\n---\nPlan it.\n")
	s.write(".satelle/skills/code.md", "---\nname: code\n---\nCode it.\n")
	s.write(".satelle/skills/judge.md", "---\nname: judge\n---\nJudge it.\n")
	s.write(".satelle/skills/built.md", "---\ntype: check\n---\n```sh\ncd \"$SATELLE_WORKTREE\" || { echo no target worktree; exit 1; }\ntest ! -e .satelle || { echo the worktree is the control repository; exit 1; }\ntest -f web/greet.go || { echo web/greet.go is missing; exit 1; }\n```\n")
	settings, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/satelle.toml", string(settings)+"\n[dispatch]\ntarget_env = [\"FAKE_DIR\"]\n")
	s.write(".satelle/repos.toml", "[repos.a]\norigin = \"https://github.com/o/a\"\n[repos.b]\norigin = \"https://github.com/o/b\"\n")
	s.write(".satelle/repos.local.toml", "[repos.a]\npath = \""+a+"\"\n[repos.b]\npath = \""+b+"\"\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "control")
	s.mustConfigValid()
	ctlHead := s.git("rev-parse", "HEAD")
	base := gittest.Git(t, a, "rev-parse", "HEAD")

	id := s.create("story", "--title", "greet", "--workflow", "ext", "--repo", "a").ID
	s.must("story", "seat", id, "--session", "s-1")
	s.must("story", "set", id, "--status", "plan")
	s.must("story", "perform", id)
	if got := strings.TrimSpace(s.must("story", "attachment", id, "plan")); got != externalPlan {
		t.Fatalf("plan attachment = %q, want %q", got, externalPlan)
	}
	if cwd := strings.TrimSpace(fakeFile(t, fake, "cwd.pl-1")); cwd == a || cwd == s.dir {
		t.Errorf("the read-only planner ran in %s, want a scratch folder", cwd)
	}

	s.must("story", "set", id, "--status", "code")
	s.must("story", "perform", id)
	if cwd := strings.TrimSpace(fakeFile(t, fake, "cwd.cd-1")); cwd != a {
		t.Fatalf("the coder ran in %s, want target %s", cwd, a)
	}

	if deny, why := s.gate("s-1", "Write", filepath.Join(a, "web", "x.go"), ""); deny {
		t.Errorf("an edit in the seated target was refused: %s", why)
	}
	for _, c := range []struct{ tool, file, command string }{
		{"Write", filepath.Join(b, "web", "x.go"), ""},
		{"Bash", "", "touch " + filepath.Join(b, "x")},
	} {
		deny, why := s.gate("s-1", c.tool, c.file, c.command)
		if !deny || !strings.Contains(why, "repo a at "+a) || !strings.Contains(why, id) {
			t.Errorf("%s in the second target: deny = %v, want a refusal naming repo a at %s and %s: %s", c.tool, deny, a, id, why)
		}
	}

	gittest.Git(t, a, "add", "-A")
	gittest.Git(t, a, "commit", "-q", "-m", "greet")
	head := gittest.Git(t, a, "rev-parse", "HEAD")
	s.must("story", "set", id, "--status", "done")
	if got := s.get(id); got.Status != "done" {
		t.Fatalf("story is %q, want done", got.Status)
	}

	want := []string{"web/greet.go"}
	runs := s.gateRuns(id)
	if len(runs) != 2 {
		t.Fatalf("gate runs = %+v", runs)
	}
	for _, g := range runs {
		if g.Result != "accept" || !slices.Equal(g.Changed, want) {
			t.Errorf("gate %s: %s, changed %v, want accept over %v (%s)", g.Gate, g.Result, g.Changed, want, g.Notes)
		}
	}
	judged := false
	for _, e := range s.ledger(id, "gate.run") {
		var p struct {
			Gate   string
			Change *struct{ Base, Head string }
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Gate != "code-ac" {
			continue
		}
		judged = true
		if p.Change == nil || p.Change.Base != base || p.Change.Head != head {
			t.Errorf("gate %s recorded change %+v, want target base %s head %s", p.Gate, p.Change, base, head)
		}
	}
	if !judged {
		t.Error("no code-ac verdict on the ledger")
	}
	prompt := fakeFile(t, fake, "prompt.rv-1")
	for _, w := range []string{"Base commit: " + base, "Changed paths:\nweb/greet.go\n", "+func Greet() string { return \"hi\" }"} {
		if !strings.Contains(prompt, w) {
			t.Errorf("the reviewer's prompt lacks %q:\n%s", w, prompt)
		}
	}
	var diff struct {
		Base, Head string
		Frozen     bool
		Paths      []string
	}
	s.mustJSON(&diff, "story", "diff", id)
	if diff.Base != base || diff.Head != head || !diff.Frozen || !slices.Equal(diff.Paths, want) {
		t.Errorf("frozen diff %+v, want base %s head %s over %v", diff, base, head, want)
	}

	noSatelleFiles(t, a)
	noSatelleFiles(t, b)
	if got := strings.Fields(gittest.Git(t, a, "diff", "--name-only", base, head)); !slices.Equal(got, want) {
		t.Errorf("target changed %v, want %v", got, want)
	}
	if st := gittest.Git(t, a, "status", "--porcelain", "--untracked-files=all", "--ignored"); st != "" {
		t.Errorf("target holds files beyond the coder's commit:\n%s", st)
	}
	if st := gittest.Git(t, b, "status", "--porcelain", "--untracked-files=all", "--ignored"); st != "" {
		t.Errorf("second target changed:\n%s", st)
	}
	if got := s.git("rev-parse", "HEAD"); got != ctlHead {
		t.Errorf("control HEAD moved from %s to %s", ctlHead, got)
	}
}
