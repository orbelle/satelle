//go:build !windows

package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	write(t, filepath.Join(fixture, WorkflowsDir, "flow.toml"), "name = \"flow\"\n")
	write(t, filepath.Join(fixture, "flow.json"), `{
"file": `+strconv.Quote(filepath.Join(fixture, WorkflowsDir, "flow.toml"))+`,
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

func judgeFlow(file string) string {
	f := ""
	if file != "" {
		f = `"file": ` + strconv.Quote(file) + `,`
	}
	return `{` + f + `
"states": {"work": {"Performer": {"Agent": "doer"}}, "done": {"Terminal": true}},
"transitions": [{"From": ["work"], "To": "done", "Groups": [{"Gates": ["look"]}]}],
"gates": {"look": {"Agent": "judge"}}
}`
}

func link(t *testing.T, target, at string) {
	t.Helper()
	for _, d := range []string{target, filepath.Dir(at)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}

func TestJudgeDiffersReadsPartBindings(t *testing.T) {
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
status) printf '{"root":"%s","workflows":%s}' "$FIXTURE_ROOT" "$FIXTURE_WORKFLOWS" ;;
workflow) cat "$FIXTURE_ROOT/shows/$(printf '%s' "$3" | tr / _).json" ;;
esac
`)
	if err := os.Chmod(filepath.Join(bin, "satelle"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	const shared = "[agents.doer]\ncommand = \"claude\"\nmodel = \"claude-x\"\n[agents.judge]\ncommand = \"cursor-agent\"\nmodel = \"gpt-x\"\n[agents.claudej]\ncommand = \"claude\"\nmodel = \"claude-y\"\n"
	const crossed = "[agents.doer]\ncommand = \"cursor-agent\"\nmodel = \"gpt-d\"\n[agents.judge]\ncommand = \"claude\"\nmodel = \"claude-j\"\nfallback = \"claudej\"\n[agents.claudej]\ncommand = \"claude\"\nmodel = \"claude-y\"\n"
	bothClaude := strings.Replace(shared, "cursor-agent\"\nmodel = \"gpt-x", "claude\"\nmodel = \"claude-j", 1)
	partJudge := func(model, extra string) string {
		return "[agents.judge]\ncommand = \"cursor-agent\"\nmodel = \"" + model + "\"\n" + extra
	}
	type tc struct {
		name      string
		workflows []string
		wfiles    map[string]string
		shared    string
		files     map[string]string
		link      bool
		setup     func(t *testing.T, fixture string)
		code      int
		want      []string
		not       []string
	}
	two := []string{"flow", "x/flow"}
	run := func(t *testing.T, c tc) string {
		t.Helper()
		fixture := t.TempDir()
		t.Setenv("FIXTURE_ROOT", fixture)
		if c.setup != nil {
			c.setup(t, fixture)
		}
		wfs := c.workflows
		if wfs == nil {
			wfs = two
		}
		js := make([]string, len(wfs))
		for i, w := range wfs {
			js[i] = strconv.Quote(w)
			rel, ok := c.wfiles[w]
			if !ok {
				rel = WorkflowsDir + "/" + w + ".toml"
				if i := strings.Index(w, "/"); i > 0 {
					rel = PartsDir + "/" + w[:i] + "/" + WorkflowsDir + "/" + w[i+1:] + ".toml"
				}
			}
			file := ""
			if rel != "" {
				file = filepath.Join(fixture, filepath.FromSlash(rel))
				write(t, file, "name = \"flow\"\n")
			}
			write(t, filepath.Join(fixture, "shows", strings.ReplaceAll(w, "/", "_")+".json"), judgeFlow(file))
		}
		t.Setenv("FIXTURE_WORKFLOWS", "["+strings.Join(js, ",")+"]")
		sh := c.shared
		if sh == "" {
			sh = shared
		}
		write(t, filepath.Join(fixture, AgentsFile), sh)
		for rel, body := range c.files {
			write(t, filepath.Join(fixture, filepath.FromSlash(rel)), body)
		}
		if c.link {
			outside := filepath.Join(t.TempDir(), "agents.toml")
			write(t, outside, partJudge("gemini-z", ""))
			if err := os.MkdirAll(filepath.Join(fixture, PartsDir, "x"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(fixture, PartsDir, "x", AgentsFile)); err != nil {
				t.Fatal(err)
			}
		}
		code, out, err := gate.RunCheckCode(context.Background(), script, fixture, gate.CheckEnv{}, 0)
		if err != nil || code != c.code {
			t.Fatalf("exit %d err %v, want %d:\n%s", code, err, c.code, out)
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("output lacks %q:\n%s", w, out)
			}
		}
		for _, w := range c.not {
			if strings.Contains(out, w) {
				t.Errorf("output has %q:\n%s", w, out)
			}
		}
		return out
	}
	for _, c := range []tc{
		{name: "a same-family part judge", files: map[string]string{"repos/x/agents.toml": partJudge("claude-z", "")}, code: 1,
			want: []string{"workflow x/flow: work -> done", "judge (claude)", "1 gate(s)"}, not: []string{"workflow flow:"}},
		{name: "b same-family part fallback", files: map[string]string{"repos/x/agents.toml": partJudge("gpt-z", "fallback = \"claudej\"\n")}, code: 1,
			want: []string{"workflow x/flow", "fallback claudej (claude)"}},
		{name: "c other-family part judge", files: map[string]string{"repos/x/agents.toml": partJudge("gemini-z", "")}, code: 0,
			want: []string{"2 gates compared"}},
		{name: "d part file is a symlink", link: true, code: 2, want: []string{"repos/x/agents.toml"}},
		{name: "f empty local before a part file", files: map[string]string{"agents.local.toml": "", "repos/x/agents.toml": partJudge("claude-z", "")}, code: 1,
			want: []string{"workflow x/flow", "judge (claude)"}, not: []string{"workflow flow:"}},
		{name: "g empty part file before another", workflows: []string{"flow", "x/flow", "y/flow"},
			files: map[string]string{"repos/x/agents.toml": "", "repos/y/agents.toml": partJudge("claude-z", "")}, code: 1,
			want: []string{"workflow y/flow", "judge (claude)", "1 gate(s)"}, not: []string{"workflow x/flow", "workflow flow:"}},
		{name: "h a part file awk cannot read", files: map[string]string{"repos/x/agents.toml": "[agents.judge]\ncommand = \"claude\"\nmodel = \"\"\"\nclaude\n\"\"\"\n"}, code: 2,
			want: []string{"repos/x/agents.toml:3:"}},
		{name: "i part binding replaces the shared one whole", shared: crossed, files: map[string]string{"repos/x/agents.toml": partJudge("gpt-z", "")}, code: 1,
			want: []string{"workflow x/flow", "judge (gpt)", "1 gate(s)"}, not: []string{"workflow flow:", "claudej"}},
		{name: "q local override applies over a part binding", shared: crossed,
			files: map[string]string{"agents.local.toml": "[agents.judge]\nmodel = \"gpt-local\"\n", "repos/x/agents.toml": partJudge("gemini-z", "")}, code: 1,
			want: []string{"workflow x/flow", "judge (gpt)"}},
		{name: "j shared file named like a part workflow", workflows: []string{"x/delivery"}, wfiles: map[string]string{"x/delivery": "workflows/delivery.toml"},
			shared: bothClaude,
			files:  map[string]string{"repos/x/agents.toml": partJudge("gpt-z", "")}, code: 1,
			want: []string{"workflow x/delivery", "judge (claude)", "1 gate(s)"}},
		{name: "k a part file reads its agents.toml", workflows: []string{"x/flow"},
			shared: bothClaude,
			files:  map[string]string{"repos/x/agents.toml": partJudge("gemini-z", "")}, code: 0,
			want: []string{"1 gates compared"}},
		{name: "l file outside both directories", wfiles: map[string]string{"x/flow": "elsewhere/flow.toml"}, code: 2,
			want: []string{"workflow x/flow", "in neither"}},
		{name: "m upper-case part directory", wfiles: map[string]string{"x/flow": "repos/Upper/workflows/flow.toml"},
			files: map[string]string{"repos/Upper/agents.toml": partJudge("claude-z", "")}, code: 2,
			want: []string{"workflow x/flow", "in neither"}},
		{name: "m nested part directory", wfiles: map[string]string{"x/flow": "repos/a/b/workflows/flow.toml"},
			files: map[string]string{"repos/a/b/agents.toml": partJudge("claude-z", "")}, code: 2,
			want: []string{"workflow x/flow", "in neither"}},
		{name: "n no file", wfiles: map[string]string{"x/flow": ""}, code: 2,
			want: []string{"workflow x/flow", ".file string"}},
		{name: "o shared workflows links into a part", workflows: []string{"flow"},
			setup: func(t *testing.T, fixture string) {
				link(t, filepath.Join(fixture, PartsDir, "x", WorkflowsDir), filepath.Join(fixture, WorkflowsDir))
			},
			files: map[string]string{"repos/x/agents.toml": partJudge("claude-z", "")}, code: 2,
			want: []string{WorkflowsDir + " is a symbolic link"}},
		{name: "p part workflows links to the shared directory",
			setup: func(t *testing.T, fixture string) {
				link(t, filepath.Join(fixture, WorkflowsDir), filepath.Join(fixture, PartsDir, "x", WorkflowsDir))
			},
			files: map[string]string{"repos/x/agents.toml": partJudge("claude-z", "")}, code: 2,
			want: []string{PartsDir + "/x/" + WorkflowsDir + " is a symbolic link"}},
	} {
		t.Run(c.name, func(t *testing.T) { run(t, c) })
	}
	t.Run("e no part file", func(t *testing.T) {
		withPart := run(t, tc{code: 0, want: []string{"2 gates compared"}})
		shared := run(t, tc{wfiles: map[string]string{"x/flow": "workflows/xflow.toml"}, code: 0})
		if withPart != shared {
			t.Errorf("a part with no agents.toml reads other than the shared catalog:\n%s\n---\n%s", withPart, shared)
		}
	})
}
