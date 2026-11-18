//go:build integration && unix

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const judgeDiffersSkill = "satelle-judge-differs-from-performer-check"

const judgeDiffersAgents = `
[agents.planner]
command = "claude"
model = "claude-x"
tools = ["Read", "Grep", "Glob"]

[agents.executor]
command = "session"

[agents.twin]
command = "claude"
model = "claude-y"
tools = ["Read", "Grep", "Glob"]

[agents.gem]
command = "cursor-agent"
model = "gemini-x"
tools = ["Read", "Grep", "Glob"]

[agents.gpt]
command = "cursor-agent"
model = "gpt-x"
tools = ["Read", "Grep", "Glob"]
`

func judgeDiffersFlow(gates, planDoing, doingDone, extra string) string {
	var b strings.Builder
	b.WriteString(`name = "default"
[states.plan]
start = true
performer = { agent = "planner", skill = "look" }
[states.doing]
performer = { agent = "executor", skill = "look" }
[states.held]
park = true
[states.done]
terminal = true
[states.cancelled]
terminal = true
`)
	b.WriteString(gates)
	b.WriteString("\n[[transition]]\nfrom = [\"plan\"]\nto = \"doing\"\n")
	if planDoing != "" {
		b.WriteString("groups = [{ gates = [" + planDoing + "] }]\n")
	}
	b.WriteString("\n[[transition]]\nfrom = [\"doing\"]\nto = \"done\"\n")
	if doingDone != "" {
		b.WriteString("groups = [{ gates = [" + doingDone + "] }]\n")
	}
	b.WriteString("\n[[transition]]\nfrom = [\"doing\"]\nto = \"held\"\n")
	b.WriteString(extra)
	return b.String()
}

func TestJudgeDiffersCheck(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by %s and is not on PATH", tool, judgeDiffersSkill)
		}
	}
	s := newSandbox(t)
	s.must("init")
	rel := ".satelle/skills/" + judgeDiffersSkill + ".md"
	installed := readSandbox(t, s, rel)
	if template := repoText(t, "internal/kernel/template/skills/"+judgeDiffersSkill+".md"); installed != template {
		t.Fatal("satelle init's copy of the check differs from the template")
	}
	if repo := repoText(t, rel); installed != repo {
		t.Fatal("this repo's copy of the check differs from the template")
	}
	skills, err := workflow.LoadSkills(filepath.Join(s.dir, ".satelle", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	script := skills[judgeDiffersSkill].Script
	if !skills[judgeDiffersSkill].IsCheck() || !strings.HasPrefix(script, "#!/usr/bin/env bash\n") {
		t.Fatalf("the check does not load as a bash check: %q", script)
	}
	argv, _, err := gate.ScriptInterpreter(script)
	if err != nil {
		t.Fatal(err)
	}
	scriptFile := filepath.Join(t.TempDir(), "check")
	writeFile(t, scriptFile, script, 0o755)
	s.write(".satelle/skills/look.md", "Judge the change.\n")

	twin2 := "\n[agents.twin2]\ncommand = \"claude\"\nmodel = \"sonnet-z\"\ntools = [\"Read\", \"Grep\", \"Glob\"]\n"
	mystery := "\n[agents.mystery]\ncommand = \"cursor-agent\"\nmodel = \"mystery-1\"\ntools = [\"Read\", \"Grep\", \"Glob\"]\n"
	grokNoModel := "\n[agents.grokj]\ncommand = \"grok\"\ntools = [\"Read\", \"Grep\", \"Glob\"]\n"
	look := func(agent string) string { return "[gates.look]\nskill = \"look\"\nagent = \"" + agent + "\"\n" }
	reviews := map[string]string{
		"tier panel all of the performer's family": "\n[review]\ntier_tag = \"risk\"\n\n[review.panels.look]\nlow = [\"gem\"]\nhigh = [\"twin\", \"twin2\"]\n",
		"tier panel with another family":           "\n[review]\ntier_tag = \"risk\"\n\n[review.panels.look]\nhigh = [\"twin\", \"gpt\"]\n",
		"unreadable tier panel":                    "\n[review]\ntier_tag = \"risk\"\n\n[review.panels.look]\nhigh = [\n  \"twin\",\n  \"gpt\",\n]\n",
	}
	baseToml := readSandbox(t, s, ".satelle/satelle.toml")
	star := "\n[[transition]]\nfrom = [\"*\"]\nto = \"cancelled\"\ngroups = [{ gates = [\"look\"] }]\n"
	explicit := "\n[[transition]]\nfrom = [\"plan\"]\nto = \"cancelled\"\n"
	for _, c := range []struct {
		name, agents, flow, local string
		code                      int
		want, not                 []string
	}{
		{"passing config", judgeDiffersAgents, judgeDiffersFlow(look("gem"), `"look"`, "", ""), "", 0,
			[]string{"1 workflows, 3 transitions, 1 gates compared"}, nil},
		{"same-family single judge", judgeDiffersAgents, judgeDiffersFlow(look("twin"), `"look"`, "", ""), "", 1,
			[]string{"gate look", "plan -> doing", "performer planner (claude", "twin (claude)"}, nil},
		{"panel with one other-family member", judgeDiffersAgents,
			judgeDiffersFlow("[gates.pnl]\nskill = \"look\"\npanel = [\"twin\", \"gpt\"]\ncombine = \"all\"\n", `"pnl"`, "", ""), "", 0,
			[]string{"1 gates compared"}, nil},
		{"all-same-family panel", judgeDiffersAgents + twin2,
			judgeDiffersFlow("[gates.pnl]\nskill = \"look\"\npanel = [\"twin\", \"twin2\"]\ncombine = \"all\"\n", `"pnl"`, "", ""), "", 1,
			[]string{"gate pnl", "twin (claude), twin2 (claude)"}, nil},
		{"session performer", judgeDiffersAgents, judgeDiffersFlow(look("twin"), "", `"look"`, ""), "", 0,
			[]string{"performer executor is the session", "not compared", "0 gates compared"}, nil},
		{"unknown judge", judgeDiffersAgents + mystery, judgeDiffersFlow(look("mystery"), `"look"`, "", ""), "", 1,
			[]string{"mystery (unknown)", "FAMILIES"}, nil},
		{"unknown performer", strings.Replace(judgeDiffersAgents, "command = \"claude\"\nmodel = \"claude-x\"", "command = \"cursor-agent\"\nmodel = \"mystery-1\"", 1),
			judgeDiffersFlow(look("gem"), `"look"`, "", ""), "", 1,
			[]string{"performer planner (model mystery-1) names no known family"}, nil},
		{"judge with no model", judgeDiffersAgents + grokNoModel, judgeDiffersFlow(look("grokj"), `"look"`, "", ""), "", 0,
			[]string{"1 gates compared"}, nil},
		{"star transition", judgeDiffersAgents, judgeDiffersFlow(look("twin"), "", "", star), "", 1,
			[]string{"plan -> cancelled: gate look"}, []string{"held ->", "done ->", "cancelled ->"}},
		{"star transition with an explicit one", judgeDiffersAgents, judgeDiffersFlow(look("twin"), "", "", explicit+star), "", 0,
			nil, []string{"plan -> cancelled"}},
		{"local override", judgeDiffersAgents, judgeDiffersFlow(look("gem"), `"look"`, "", ""), "[agents.gem]\nmodel = \"claude-z\"\n", 1,
			[]string{"gate look", "gem (claude)"}, nil},
		{"tier panel all of the performer's family", judgeDiffersAgents + twin2, judgeDiffersFlow(look("gem"), `"look"`, "", ""), "", 1,
			[]string{"gate look (tier high)", "twin (claude), twin2 (claude)", "1 gate(s) have no judge"}, []string{"gate look:"}},
		{"tier panel with another family", judgeDiffersAgents, judgeDiffersFlow(look("gem"), `"look"`, "", ""), "", 0,
			[]string{"2 gates compared"}, nil},
		{"unreadable tier panel", judgeDiffersAgents, judgeDiffersFlow(look("gem"), `"look"`, "", ""), "", 2,
			[]string{"satelle.toml:", "cannot read the line high ="}, nil},
		{"unreadable model", strings.Replace(judgeDiffersAgents, `model = "claude-x"`, `model = 'claude-x'`, 1),
			judgeDiffersFlow(look("gem"), `"look"`, "", ""), "", 2,
			[]string{"agents.toml:4:", "cannot read the model value"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			s.write(".satelle/agents.toml", c.agents)
			s.write(".satelle/satelle.toml", baseToml+reviews[c.name])
			s.write(".satelle/workflows/default.toml", c.flow)
			local := filepath.Join(s.dir, ".satelle", "agents.local.toml")
			_ = os.Remove(local)
			if c.local != "" {
				s.write(".satelle/agents.local.toml", c.local)
			}
			if r := s.run("validate"); r.code != 0 {
				t.Fatalf("validate: exit %d\n%s%s", r.code, r.out, r.err)
			}
			r := s.exec(argv[0], "", append(argv[1:], scriptFile)...)
			out := r.out + r.err
			if r.code != c.code {
				t.Fatalf("exit %d, want %d\n%s", r.code, c.code, out)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, n := range c.not {
				if strings.Contains(out, n) {
					t.Errorf("output names %q:\n%s", n, out)
				}
			}
		})
	}
	_ = os.Remove(filepath.Join(s.dir, ".satelle", "agents.local.toml"))

	t.Run("no jq", func(t *testing.T) {
		bash, err := exec.LookPath("bash")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		if err := os.Symlink(bash, filepath.Join(bin, "bash")); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(filepath.Join(bin, "bash"), scriptFile)
		cmd.Dir = s.dir
		for _, kv := range s.environ() {
			if !strings.HasPrefix(kv, "PATH=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "PATH="+bin)
		out, err := cmd.CombinedOutput()
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 || !strings.Contains(string(out), "jq is not installed") {
			t.Fatalf("with no jq on PATH: %v\n%s", err, out)
		}
	})
}
