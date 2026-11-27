//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gittest"
)

const (
	missingAgent = "claude"
	missingTwo   = "grok"
)

func fakeAgent(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	writeExec(t, bin, "#!/bin/sh\n"+body)
	return bin
}

func answeringAgent(t *testing.T) string {
	t.Helper()
	return fakeAgent(t, "cat > \"$0.stdin\"\necho x >> \"$0.count\"\necho '{\"result\":\"ok\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}'\n")
}

func writeProbeWorkflow(t *testing.T, dir, coder, judge string) {
	t.Helper()
	path := []string{}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(p, missingAgent)); err != nil {
			if _, err := os.Stat(filepath.Join(p, missingTwo)); err != nil {
				path = append(path, p)
			}
		}
	}
	t.Setenv("PATH", strings.Join(path, string(os.PathListSeparator)))
	agents := filepath.Join(dir, ".satelle", "agents.toml")
	shipped, err := os.ReadFile(agents)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, agents, string(shipped)+"\n[agents.probe-coder]\ncommand = \""+coder+"\"\ntools = [\"Read\", \"Edit\", \"Write\", \"Bash\"]\n\n[agents.probe-judge]\ncommand = \""+judge+"\"\ntools = [\"Read\", \"Grep\", \"Glob\"]\n")
	writeFile(t, filepath.Join(dir, ".satelle", "workflows", "probe.toml"), `name = "probe"
[states.open]
start = true
[states.work]
performer = { agent = "probe-coder", skill = "probe-work" }
[states.done]
terminal = true
[gates.review]
skill = "probe-review"
agent = "probe-judge"
[gates.again]
skill = "probe-review"
agent = "probe-judge"
[[transition]]
from = ["open"]
to = "work"
[[transition]]
from = ["work"]
to = "done"
groups = [{ mode = "parallel", gates = ["review", "again"] }]
`)
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "probe-work.md"), "---\nname: probe-work\n---\nDo the work.\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "probe-review.md"), "---\nname: probe-review\n---\nReview it.\n")
	commitAll(t, dir, "probe workflow")
}

func fileText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func agentIssues(rep doctorReport) []doctorIssue {
	var out []doctorIssue
	for _, is := range rep.Issues {
		if is.Check == "agents" {
			out = append(out, is)
		}
	}
	return out
}

func assertUnchanged(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	if d := sameDigest(before, digest(t, dir)); len(d) > 0 {
		t.Errorf("doctor changed the repo: %v", d)
	}
	if st := gittest.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("git status not clean:\n%s", st)
	}
}

func TestDoctorReportsAMissingBindingAndChangesNothing(t *testing.T) {
	f := newDoctorFixture(t)
	writeProbeWorkflow(t, f.dir, answeringAgent(t), missingAgent)
	before := digest(t, f.dir)
	rep, text, err := doctorJSON(t)
	if ExitCodeOf(err) != 1 {
		t.Fatalf("exit %d, want 1 (%v)\n%s", ExitCodeOf(err), err, text)
	}
	assertUnchanged(t, f.dir, before)
	if len(agentIssues(rep)) != len(rep.Issues) {
		t.Errorf("issues beyond the agents check: %+v", rep.Issues)
	}
	var judge *doctorIssue
	for _, is := range agentIssues(rep) {
		if strings.Contains(is.Problem, "binding probe-judge ") {
			judge = &is
		}
	}
	if judge == nil {
		t.Fatalf("no agents issue for probe-judge in %+v", rep.Issues)
	}
	for _, w := range []string{"is missing on this machine", "does not resolve", "executable file not found", "workflow probe gate review (judge)"} {
		if !strings.Contains(judge.Problem, w) {
			t.Errorf("problem %q lacks %q", judge.Problem, w)
		}
	}
	if !strings.Contains(judge.Fix, "agents.local.toml") || !strings.Contains(judge.Fix, "install "+missingAgent) {
		t.Errorf("fix %q", judge.Fix)
	}
	if judge.Resolve != "" {
		t.Errorf("a missing binding is resolvable: %+v", judge)
	}
	if !strings.Contains(text, "agents: binding probe-judge ") {
		t.Errorf("text output lacks the issue:\n%s", text)
	}
	if !slices.ContainsFunc(rep.Notes, func(n string) bool { return strings.HasPrefix(n, "agents: probe-coder ok (") }) {
		t.Errorf("the answering performer is not ok: %v", rep.Notes)
	}
}

func TestDoctorReportsAMissingPathAndAMissingPerformer(t *testing.T) {
	f := newDoctorFixture(t)
	judge := answeringAgent(t) + " --ext /nonexistent/x.ts"
	writeProbeWorkflow(t, f.dir, missingAgent, judge)
	rep, _, err := doctorJSON(t)
	if ExitCodeOf(err) != 1 {
		t.Fatalf("exit %d, want 1 (%v)", ExitCodeOf(err), err)
	}
	issues := agentIssues(rep)
	if !slices.ContainsFunc(issues, func(is doctorIssue) bool {
		return strings.Contains(is.Problem, "binding probe-judge ") && strings.Contains(is.Problem, `path "/nonexistent/x.ts" its command names does not exist`)
	}) {
		t.Errorf("no missing-path issue in %+v", issues)
	}
	if !slices.ContainsFunc(issues, func(is doctorIssue) bool {
		return strings.Contains(is.Problem, "binding probe-coder ") && strings.Contains(is.Problem, "workflow probe state work (performer)")
	}) {
		t.Errorf("no missing-performer issue in %+v", issues)
	}
}

func TestDoctorReportsABindingThatDoesNotAnswer(t *testing.T) {
	cases := []struct {
		name, body, want string
		timeout          time.Duration
	}{
		{"signed out", "echo 'Not logged in' >&2\nexit 1\n", "Not logged in", 0},
		{"hangs", "sleep 5\n", "context deadline exceeded", 300 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.timeout > 0 {
				prev := agentProbeTimeout
				agentProbeTimeout = tc.timeout
				t.Cleanup(func() { agentProbeTimeout = prev })
			}
			f := newDoctorFixture(t)
			writeProbeWorkflow(t, f.dir, answeringAgent(t), fakeAgent(t, tc.body))
			before := digest(t, f.dir)
			rep, _, err := doctorJSON(t)
			if ExitCodeOf(err) != 1 {
				t.Fatalf("exit %d, want 1 (%v)", ExitCodeOf(err), err)
			}
			assertUnchanged(t, f.dir, before)
			var judge *doctorIssue
			for _, is := range agentIssues(rep) {
				if strings.Contains(is.Problem, "binding probe-judge ") {
					judge = &is
				}
			}
			if judge == nil {
				t.Fatalf("no agents issue for probe-judge in %+v", rep.Issues)
			}
			for _, w := range []string{"does not answer a probe", tc.want, "workflow probe gate review (judge)"} {
				if !strings.Contains(judge.Problem, w) {
					t.Errorf("problem %q lacks %q", judge.Problem, w)
				}
			}
			if !strings.Contains(judge.Fix, "/claude once by hand") {
				t.Errorf("fix %q", judge.Fix)
			}
		})
	}
}

func TestDoctorReportsEveryAnsweringBindingOk(t *testing.T) {
	f := newDoctorFixture(t)
	coder, judge := answeringAgent(t), answeringAgent(t)
	writeProbeWorkflow(t, f.dir, coder, judge)
	rep, text, err := doctorJSON(t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, text)
	}
	if is := agentIssues(rep); len(is) > 0 {
		t.Fatalf("agents issues %+v", is)
	}
	for _, n := range []string{"agents: probe-judge ok (", "agents: probe-coder ok ("} {
		if !slices.ContainsFunc(rep.Notes, func(s string) bool { return strings.HasPrefix(s, n) }) {
			t.Errorf("no note %q in %v", n, rep.Notes)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "no issues") {
		t.Errorf("text does not end no issues:\n%s", text)
	}
	if in := fileText(t, coder+".stdin"); !strings.Contains(in, "Reply with the single word ok.") {
		t.Errorf("probe prompt not on stdin: %q", in)
	}
	if n := strings.Count(fileText(t, judge+".count"), "x"); n != 2 {
		t.Errorf("the judge, used by two gates, ran %d times over two doctor runs, want once a run (2)", n)
	}

	for _, p := range []string{coder + ".stdin", coder + ".count"} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, "", "doctor", "--no-probe")
	if err != nil {
		t.Fatalf("doctor --no-probe: %v\n%s", err, out)
	}
	if _, err := os.Stat(coder + ".stdin"); !os.IsNotExist(err) {
		t.Errorf("--no-probe started the agent")
	}
	if !strings.Contains(out, "agents: probe-coder resolves ("+coder+"); not probed (--no-probe)") {
		t.Errorf("no not-probed note:\n%s", out)
	}
}

func TestSessionStartWarnsAboutAMissingBindingWithoutProbing(t *testing.T) {
	f := newDoctorFixture(t)
	marker := filepath.Join(t.TempDir(), "probed")
	ran := fakeAgent(t, "touch "+marker+"\necho '{\"result\":\"ok\"}'\n")
	writeProbeWorkflow(t, f.dir, ran, missingAgent)
	start := time.Now()
	block, notice := sessionDoctor(f.dir, "claude")
	if d := time.Since(start); d > doctorHookBudget+time.Second {
		t.Errorf("session doctor took %s", d)
	}
	var lines []string
	for _, l := range strings.Split(block, "\n") {
		if strings.Contains(l, "probe-") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "  agents (") || !strings.Contains(lines[0], "probe-judge ("+missingAgent+")") {
		t.Fatalf("block lines naming a binding %q, want one agents line naming probe-judge:\n%s", lines, block)
	}
	if !strings.Contains(notice, "probe-judge") {
		t.Errorf("notice does not name the binding:\n%s", notice)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("session start probed an agent")
	}
	ctx, _, _ := hookContextText(t, f.dir)
	if !strings.Contains(ctx, "probe-judge") {
		t.Errorf("hook context lacks the warning:\n%s", ctx)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("the session-start hook probed an agent")
	}

	agents := filepath.Join(f.dir, ".satelle", "agents.toml")
	b, _ := os.ReadFile(agents)
	writeFile(t, agents, strings.Replace(string(b), "[agents.probe-judge]\ncommand = \""+missingAgent+"\"", "[agents.probe-judge]\ncommand = \""+missingTwo+"\"", 1))
	writeFile(t, agents, strings.Replace(fileText(t, agents), "[agents.probe-coder]\ncommand = \""+ran+"\"", "[agents.probe-coder]\ncommand = \""+missingAgent+"\"", 1))
	commitAll(t, f.dir, "two missing")
	block, _ = sessionDoctor(f.dir, "claude")
	lines = nil
	for _, l := range strings.Split(block, "\n") {
		if strings.Contains(l, "probe-") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "probe-coder ("+missingAgent+")") || !strings.Contains(lines[0], "probe-judge ("+missingTwo+")") {
		t.Fatalf("two missing bindings, want one agents line naming both: %q\n%s", lines, block)
	}
}
