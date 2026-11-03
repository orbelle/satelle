//go:build clislow

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// sty_7bfa980d AC4: a gate whose reviewer binding lists model = "m1,m2", whose
// fake agent never answers on m1 and accepts on m2, passes on m2 — and the two
// read surfaces say so: `story route` credits the verdict to m2, and `story
// cost` lists m1's attempt (the model_failover row) and m2's verdict row. On
// code with no model list the binding's whole "m1,m2" reaches the agent as one
// model, the agent never answers, and the transition fails.
func TestSlowModelListVerdictIsCreditedToTheModelThatAnswered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a shell script")
	}
	repo := tempRepo(t)
	t.Chdir(repo)
	dir := t.TempDir()
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "readied", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[readied]
status = "plan"
reviewers = ["mm-review"]
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["readied"]
`)
	skills := filepath.Join(repo, ".satelle", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	rubric := "---\nname: mm-review\ntype: skill\ndescription: an LLM gate for the model-list test\n---\n\n" +
		"Judge the story.\n\nReturn JSON {\"decision\": \"accept\"|\"reject\", \"notes\": \"…\"}.\n"
	if err := os.WriteFile(filepath.Join(skills, "mm-review.md"), []byte(rubric), 0o644); err != nil {
		t.Fatal(err)
	}
	// A model starting m1 (m1 itself, or the whole unsplit "m1,m2") goes silent
	// until the idle bound stops it; any other model accepts.
	agent := filepath.Join(dir, "fake-reviewer")
	script := "#!/bin/sh\nm=\nwhile [ $# -gt 0 ]; do [ \"$1\" = --model ] && m=\"$2\"; shift; done\n" +
		"echo \"$m\" >> " + filepath.Join(dir, "runs.log") + "\ncat > /dev/null\n" +
		"case \"$m\" in m1*) exec sleep 20 ;; esac\n" +
		`echo '{"decision":"accept","notes":"answered"}'` + "\n"
	if err := os.WriteFile(agent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	agents := "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[reviewer]\nrole = \"reviewer\"\ninterface = \"command\"\ncommand = \"" + agent + " --model {model} -p {system}\"\n" +
		"tools = \"Read,Grep,Glob\"\nisolation = \"operator-attested\"\nmodel = \"m1,m2\"\n" +
		"idle_timeout = \"1s\"\nbusy_timeout = \"0\"\n"
	if err := os.WriteFile(filepath.Join(wfDir, config.AgentsConfigName), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(repo, ".satelle", config.AgentsConfigName))
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	out, err := runRoot(t, "story", "create", "--title", "fail over", "--body", "the first model does not answer",
		"--acceptance", "1. the second model answers", "--category", "chore")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("parse create: %v\n%s", err, out)
	}
	id := created["id"].(string)

	if out, err := runRoot(t, "story", "set", id, "--status", "plan"); err != nil {
		runs, _ := os.ReadFile(filepath.Join(dir, "runs.log"))
		t.Fatalf("the gate must pass on m2: %v\n%s\nruns:\n%s", err, out, runs)
	}
	runs, _ := os.ReadFile(filepath.Join(dir, "runs.log"))
	if got := strings.Fields(string(runs)); strings.Join(got, ",") != "m1,m2" {
		t.Fatalf("the agent must run once on m1 then once on m2:\n%s", runs)
	}

	route, err := runRoot(t, "story", "route", id)
	if err != nil {
		t.Fatalf("story route: %v\n%s", err, route)
	}
	var verdict string
	for _, ln := range strings.Split(route, "\n") {
		if strings.Contains(ln, "full output") && strings.Contains(ln, "--kind review_accept") {
			verdict = ln
		}
	}
	if !strings.Contains(verdict, "(model m2") || strings.Contains(verdict, "m1") {
		t.Fatalf("story route must credit the verdict to m2 and not m1: %q\n%s", verdict, route)
	}

	cost, err := runRoot(t, "story", "cost", id)
	if err != nil {
		t.Fatalf("story cost: %v\n%s", err, cost)
	}
	var m1Row, m2Row bool
	for _, ln := range strings.Split(cost, "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 || !strings.Contains(ln, "mm-review") {
			continue
		}
		switch {
		case strings.Contains(ln, " m1 "):
			m1Row = true
		case strings.Contains(ln, " m2 "):
			m2Row = true
		}
	}
	if !m1Row || !m2Row {
		t.Fatalf("story cost must list m1's attempt and m2's verdict under MODEL (m1=%v m2=%v):\n%s", m1Row, m2Row, cost)
	}
}
