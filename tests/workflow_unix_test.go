//go:build integration && !windows

package tests

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeClaude puts a fake `claude` first on the sandbox's PATH. Each run
// records its argv in <dir>/argv.<model>.<pid> and answers with claude's JSON
// envelope whose result is a verdict: accept, or what <dir>/verdict.<model>
// holds.
func fakeClaude(t *testing.T, s *sandbox) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "claude"), `#!/bin/sh
model=none
prev=
for a in "$@"; do [ "$prev" = "--model" ] && model=$a; prev=$a; done
printf '%s\n' "$@" > "$FAKE_DIR/argv.$model.$$"
cat > /dev/null
v=accept
[ -f "$FAKE_DIR/verdict.$model" ] && v=$(cat "$FAKE_DIR/verdict.$model")
printf '{"type":"result","subtype":"success","is_error":false,"result":"{\\"verdict\\":\\"%s\\",\\"notes\\":\\"%s by %s\\"}","usage":{"input_tokens":11,"output_tokens":7}}\n' "$v" "$v" "$model"
`, 0o755)
	s.path = append(s.path, dir)
	s.env["FAKE_DIR"] = dir
	return dir
}

// argvs returns every recorded argv of the fake runs of model.
func argvs(t *testing.T, dir, model string) [][]string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "argv."+model+".*"))
	var out [][]string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"))
		os.Remove(f)
	}
	return out
}

// flag returns the value after name in argv.
func flag(argv []string, name string) string {
	if i := slices.Index(argv, name); i >= 0 && i+1 < len(argv) {
		return argv[i+1]
	}
	return ""
}

const pipelineAgents = `
[agents.maker]
command = "claude"
model = "mk-1"
tools = ["Read", "Edit", "Bash"]
[agents.judge-a]
command = "claude"
model = "ja-1"
tools = ["Read", "Grep"]
[agents.judge-b]
command = "claude"
model = "jb-1"
tools = ["Read"]
[agents.judge-c]
command = "claude"
model = "jc-1"
tools = ["Read", "Glob"]
[agents.judge-d]
command = "claude"
model = "jd-1"
tools = ["Read"]
`

const pipelineWorkflow = `name = "pipeline"
[states.queued]
start = true
[states.build]
performer = { agent = "maker", skill = "make" }
[states.paused]
park = true
resumes = true
[states.shipped]
terminal = true

[gates.lint]
skill = "has-ok"
[gates.docs]
skill = "judge"
agent = "judge-a"
paths = ["docs/**"]
[gates.review]
skill = "judge"
agent = "judge-a"
paths = ["src/**"]
carry = true
[gates.panel]
skill = "judge"
panel = ["judge-b", "judge-c"]
combine = "all"
[gates.strict]
skill = "judge"
agent = "judge-d"

[[transition]]
from = ["queued"]
to = "build"
[[transition]]
from = ["build"]
to = "shipped"
groups = [
  { mode = "sequential", gates = ["lint", "docs", "review"] },
  { mode = "parallel", gates = ["panel", "strict"] },
]
[[transition]]
from = ["*"]
to = "paused"
[[transition]]
from = ["paused"]
to = "@prior"
`

// A user-defined workflow end to end: a dispatched performer, gates that
// dispatch a fake claude, a check skill, fail-fast, parallel groups,
// relevance, carry, a panel, park and @prior resume, and every verdict on
// the ledger.
func TestUserWorkflowDispatchesAFakeAgent(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	fake := fakeClaude(t, s)
	s.must("init")
	s.write(".satelle/agents.toml", pipelineAgents)
	s.write(".satelle/workflows/pipeline.toml", pipelineWorkflow)
	s.write(".satelle/skills/has-ok.md", "---\ntype: check\n---\n```sh\ntest -f ok.txt || { echo ok.txt is missing; exit 1; }\n```\n")
	s.write(".satelle/skills/make.md", "---\nname: make\n---\nBuild it.\n")
	s.write(".satelle/skills/judge.md", "---\nname: judge\n---\nJudge it.\n")
	s.write("src/main.go", "package main\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "config")
	s.must("validate")

	id := s.create("story", "--title", "pipe", "--workflow", "pipeline").ID
	s.must("story", "set", id, "--status", "build")

	// The performer is dispatched with the status verbs denied.
	if out := s.must("story", "perform", id); !strings.Contains(out, "by mk-1") {
		t.Errorf("perform printed %q", out)
	}
	runs := argvs(t, fake, "mk-1")
	if len(runs) != 1 {
		t.Fatalf("performer runs = %d", len(runs))
	}
	if deny := flag(runs[0], "--disallowedTools"); !strings.Contains(deny, "Bash(satelle story set:*)") || !strings.Contains(deny, "Bash(satelle task set:*)") {
		t.Errorf("performer argv lacks the status-verb deny: %q", runs[0])
	}
	if got := flag(runs[0], "--append-system-prompt"); !strings.Contains(got, "Build it.") {
		t.Errorf("performer system prompt = %q", got)
	}
	if len(s.ledger(id, "performer.run")) != 1 {
		t.Error("no performer.run on the ledger")
	}

	// Round 1: the check rejects first and fail-fast stops the round.
	r := s.run("story", "set", id, "--status", "shipped")
	if r.code != 2 || !strings.Contains(r.err, "gate lint") || !strings.Contains(r.err, "ok.txt is missing") {
		t.Fatalf("round 1: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := len(argvs(t, fake, "ja-1")); n != 0 {
		t.Errorf("fail-fast still dispatched judge-a %d time(s)", n)
	}
	if g := s.gateRuns(id); len(g) != 1 || g[0].Gate != "lint" || g[0].Result != "reject" {
		t.Errorf("round 1 gate runs = %+v", g)
	}

	// Round 2: the check passes, docs is irrelevant (skipped), review judges
	// the src change and accepts, and the
	// parallel group runs both gates though one panel member rejects.
	s.write("ok.txt", "")
	s.write("src/main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(fake, "verdict.jc-1"), "reject", 0o644)
	r = s.run("story", "set", id, "--status", "shipped")
	if r.code != 2 || !strings.Contains(r.err, "gate panel") || !strings.Contains(r.err, "reject by jc-1") {
		t.Fatalf("round 2: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if strings.Contains(r.err, "gate strict") {
		t.Errorf("the accepting gate is reported as rejecting:\n%s", r.err)
	}
	for _, model := range []string{"ja-1", "jb-1", "jc-1", "jd-1"} {
		runs := argvs(t, fake, model)
		if len(runs) != 1 {
			t.Fatalf("judge %s ran %d time(s) in round 2", model, len(runs))
		}
		a := runs[0]
		// Judges are read-only: offered and allowed only read tools.
		for _, f := range []string{"--tools", "--allowedTools"} {
			for _, tool := range strings.Split(flag(a, f), ",") {
				if tool != "Read" && tool != "Grep" && tool != "Glob" {
					t.Errorf("judge %s %s grants %q: %q", model, f, tool, a)
				}
			}
		}
		if slices.Contains(a, "--disallowedTools") && strings.Contains(flag(a, "--disallowedTools"), "satelle story set") {
			t.Errorf("judge %s carries the performer deny", model)
		}
	}
	want := map[string]string{"lint": "accept", "docs": "skipped", "review": "accept", "panel": "reject", "strict": "accept"}
	round2 := s.gateRuns(id)[1:]
	if len(round2) != len(want) {
		t.Fatalf("round 2 gate runs = %+v", round2)
	}
	for _, g := range round2 {
		if want[g.Gate] != g.Result {
			t.Errorf("gate %s = %s, want %s (%s)", g.Gate, g.Result, want[g.Gate], g.Notes)
		}
		if (g.Gate == "review" || g.Gate == "strict") && (g.TokensIn != 11 || g.TokensOut != 7 || g.Binding == "") {
			t.Errorf("gate %s recorded tokens %d/%d binding %q", g.Gate, g.TokensIn, g.TokensOut, g.Binding)
		}
	}
	if got := s.get(id); got.Status != "build" {
		t.Fatalf("a rejected move moved the item to %q", got.Status)
	}

	// Park, refuse a resume elsewhere, resume to the parked-from state.
	s.must("story", "set", id, "--status", "paused")
	if r := s.run("story", "set", id, "--status", "queued"); r.code == 0 {
		t.Error("a parked item resumed to a state it was not parked from")
	}
	if out := s.must("story", "set", id, "--status", "build"); !strings.Contains(out, "paused -> build") {
		t.Errorf("resume: %s", out)
	}

	// Round 3: review's accept is carried, docs now matters and runs, the
	// panel accepts, and the item reaches the terminal state.
	if err := os.Remove(filepath.Join(fake, "verdict.jc-1")); err != nil {
		t.Fatal(err)
	}
	s.write("docs/guide.md", "# guide\n")
	s.must("story", "set", id, "--status", "shipped")
	if got := s.get(id); got.Status != "shipped" {
		t.Fatalf("status %q, want shipped", got.Status)
	}
	if n := len(argvs(t, fake, "ja-1")); n != 1 {
		t.Errorf("judge-a ran %d time(s) in round 3, want 1 (docs only; review carried)", n)
	}
	want = map[string]string{"lint": "accept", "docs": "accept", "review": "carried", "panel": "accept", "strict": "accept"}
	all := s.gateRuns(id)
	round3 := all[len(all)-len(want):]
	for _, g := range round3 {
		if want[g.Gate] != g.Result {
			t.Errorf("round 3 gate %s = %s, want %s (%s)", g.Gate, g.Result, want[g.Gate], g.Notes)
		}
	}
	if route := s.must("story", "route", id); !strings.Contains(route, "▶ shipped") || !strings.Contains(route, "carried") {
		t.Errorf("route:\n%s", route)
	}
	s.must("ledger", "verify")
}
