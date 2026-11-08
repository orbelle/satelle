//go:build integration && unix

package tests

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

// triggerScript is the check script of the repo skill name, as the binary
// parses it.
func triggerScript(t *testing.T, name string) string {
	t.Helper()
	sk, err := workflow.ParseSkill(name, repoText(t, ".satelle/skills/"+name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !sk.IsCheck() || !strings.HasPrefix(sk.Script, "#!") {
		t.Fatalf("%s is not a check skill with a #! script", name)
	}
	return sk.Script
}

// triggerSandbox is an initialised repo with a committed baseline holding
// .satelle/skills/guide.md (a skill of a few dozen words) and internal/x.go.
func triggerSandbox(t *testing.T) *sandbox {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "git", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the trigger skills and is not on PATH", tool)
		}
	}
	s := newSandbox(t)
	s.env["SATELLE_SESSION"] = "trigger-it"
	s.must("init")
	s.write(".satelle/skills/guide.md", "---\nname: guide\n---\n\n# Guide\n\nRead the card, then the plan, then write the code and its test.\n")
	s.write("internal/x.go", "package x\n\nfunc Foo() {}\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "baseline")
	return s
}

// fireTrigger creates a story, commits files (path -> body) after it, and runs the
// trigger script on it as the gate runs it: SATELLE_ITEM in the environment
// and the transition payload on stdin. It returns the story id and the result.
func fireTrigger(t *testing.T, s *sandbox, script string, files map[string]string) (string, result) {
	t.Helper()
	id := s.create("story", "--title", "change").ID
	if !strings.HasPrefix(id, "sto_") {
		t.Fatalf("story id %q is not sto_", id)
	}
	for p, body := range files {
		s.write(p, body)
	}
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "change for "+id)
	bin := filepath.Join(t.TempDir(), "trigger")
	writeFile(t, bin, script, 0o755)
	s.env["SATELLE_ITEM"] = id
	defer delete(s.env, "SATELLE_ITEM")
	r := s.exec(bin, `{"id":"`+id+`","from":"in_progress","to":"review","base":""}`)
	return id, r
}

// Each change trigger exits 0 on a story whose diff touches the paths its gate
// guards and 1 on one whose diff does not; neither exits 2 (a broken trigger,
// which runs the gate anyway).
func TestChangeTriggersReadTheStoryDiff(t *testing.T) {
	t.Parallel()
	cases := []struct {
		skill string
		files map[string]string
		want  int
		why   string
	}{
		{"satelle-workflow-change-trigger", map[string]string{"internal/x.go": "package x\n\nfunc Foo() { bar() }\n\nfunc bar() {}\n"}, 1, "code only"},
		{"satelle-workflow-change-trigger", map[string]string{"internal/config/substrate/workflows/delivery.toml": "name = \"delivery\"\n"}, 0, "an embedded route"},
		{"satelle-workflow-change-trigger", map[string]string{".satelle/workflows/extra.toml": extraRoute}, 0, "a repo route"},
		{"satelle-instruction-change-trigger", map[string]string{"internal/x.go": "package x\n\nfunc Foo() { bar() }\n\nfunc bar() {}\n"}, 1, "code only"},
		{"satelle-instruction-change-trigger", map[string]string{".satelle/skills/guide.md": "---\nname: guide\n---\n\n# Guide\n\nRead the card, then the plan, then write the code and its test!\n"}, 1, "punctuation only"},
		{"satelle-instruction-change-trigger", map[string]string{".satelle/skills/guide.md": "---\nname: guide\n---\n\n# Guide\n\nRead the card and every linked principle first, then write the code and its test.\n"}, 0, "a rewritten skill"},
		{"satelle-instruction-change-trigger", map[string]string{".satelle/principles/new.md": "# New\n\nA new principle.\n"}, 0, "a new principle"},
	}
	for _, c := range cases {
		t.Run(c.skill+"/"+strings.ReplaceAll(c.why, " ", "-"), func(t *testing.T) {
			t.Parallel()
			s := triggerSandbox(t)
			id, r := fireTrigger(t, s, triggerScript(t, c.skill), c.files)
			if r.code != c.want {
				t.Fatalf("%s on %s (%s): exit %d, want %d\n%s%s", c.skill, id, c.why, r.code, c.want, r.out, r.err)
			}
			if c.want == 1 && strings.TrimSpace(r.out) == "" {
				t.Errorf("%s skipped %s with no output to note", c.skill, id)
			}
		})
	}
}

// extraRoute is a valid route file, so the sandbox's configuration loads.
const extraRoute = `name = "extra"
[states.defined]
start = true
[states.retired]
terminal = true
[[transition]]
from = ["defined"]
to = "retired"
`
