//go:build !windows

package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestJudgeDiffersChecksFallbacks(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	skills, err := workflow.LoadSkills(filepath.Join(root, SkillsDir))
	if err != nil {
		t.Fatal(err)
	}
	script := skills["satelle-judge-differs-from-performer-check"].Script
	bin := t.TempDir()
	write(t, filepath.Join(bin, "satelle"), `#!/bin/sh
case "$1" in
status) printf '{"root":"%s","workflows":["flow"]}' "$FIXTURE_ROOT" ;;
workflow) cat "$FIXTURE_ROOT/flow.json" ;;
esac
`)
	if err := os.Chmod(filepath.Join(bin, "satelle"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	fixture := t.TempDir()
	t.Setenv("FIXTURE_ROOT", fixture)
	write(t, filepath.Join(fixture, "flow.json"), `{
"states": {"work": {"Performer": {"Agent": "doer"}}, "done": {"Terminal": true}},
"transitions": [{"From": ["work"], "To": "done", "Groups": [{"Gates": ["look"]}]}],
"gates": {"look": {"Agent": "judge"}}
}`)
	const agents = `
[agents.doer]
command = "claude"
model = "claude-x"
[agents.judge]
command = "cursor-agent"
model = "gpt-x"
fallback = "%s"
[agents.claudej]
command = "claude"
model = "claude-y"
[agents.gemj]
command = "cursor-agent"
model = "gemini-x"
`
	for _, c := range []struct {
		fallback string
		code     int
		want     string
	}{
		{"claudej", 1, "judge (gpt, fallback claudej (claude))"},
		{"gemj", 0, "1 gates compared"},
	} {
		write(t, filepath.Join(fixture, "agents.toml"), strings.Replace(agents, "%s", c.fallback, 1))
		code, out, err := gate.RunCheckCode(context.Background(), script, fixture, gate.CheckEnv{}, 0)
		if err != nil || code != c.code || !strings.Contains(out, c.want) {
			t.Fatalf("fallback %s: exit %d err %v, want exit %d with %q:\n%s", c.fallback, code, err, c.code, c.want, out)
		}
	}
}
