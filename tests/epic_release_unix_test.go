//go:build integration && unix

package tests

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The epic process as the agent-goals principle writes it: with two children
// approved the readiness block says fire, release step 0 fires one run over
// both, a child the run sends back keeps the epic underway until it is driven
// to approved and released by a second fire, and the epic then closes
// through its own gates.
func TestEpicClosesAfterTheReleaseRun(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the epic and release blocks and is not on PATH", tool)
		}
	}
	const principle = ".satelle/principles/satelle-agent-goals.md"
	const sendBackSentence = "Drive that child again to `approved` before the next fire."

	// The principle's epic section: the release-driven close, none of the
	// pre-1.0 wave, readiness-round, waiting-step, epic-base or trunk text.
	b, err := os.ReadFile(filepath.Join("..", principle))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(b), "## Driving an epic")
	if !ok {
		t.Fatalf("%s has no ## Driving an epic", principle)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, gone := range []string{"story wave", "story ready", "waiting step", "epic base", "trunk"} {
		if strings.Contains(section, gone) {
			t.Errorf("## Driving an epic still says %q", gone)
		}
	}
	for _, want := range []string{".satelle/skills/release.md", "release-sendback", sendBackSentence} {
		if !strings.Contains(section, want) {
			t.Errorf("## Driving an epic lacks %q", want)
		}
	}

	s := newSandbox(t)
	fake := fakeClaude(t, s)
	// The epic's judges answer with reasons that name the record, as the
	// epic gates' notes and reference rules require.
	writeFile(t, filepath.Join(fake, "notes.ra-1"), "every child is closed: read attachments/ and the children of the epic", 0o644)
	s.env["SATELLE_SESSION"] = "release-it"
	origin := realDir(t, t.TempDir())
	s.exec("git", "", "init", "-q", "--bare", origin)
	s.git("remote", "add", "origin", origin)
	s.git("push", "-q", "origin", "HEAD:refs/heads/main")
	s.must("init")

	// The queue and epic rules exactly as this repo's settings carry them.
	settings, err := os.ReadFile("../.satelle/satelle.toml")
	if err != nil {
		t.Fatal(err)
	}
	queueRule := regexp.MustCompile(`(?m)^\[\[workflows\.rule\]\]\nworkflow = "standing"\nkind = "queue"\n`).FindString(string(settings))
	epicRule := regexp.MustCompile(`(?m)^\[\[workflows\.rule\]\]\nworkflow = "epic"\ntag = "epic"\n`).FindString(string(settings))
	if queueRule == "" || epicRule == "" {
		t.Fatalf(".satelle/satelle.toml lacks the queue or epic rule (queue %q, epic %q)", queueRule, epicRule)
	}
	s.write(".satelle/satelle.toml", `[workflows]
default = "flow"
[[workflows.rule]]
workflow = "runbook"
kind = "execution"
[[workflows.rule]]
workflow = "standing"
kind = "task"
`+queueRule+epicRule)
	repoFile := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	s.write(".satelle/workflows/epic.toml", repoFile(".satelle/workflows/epic.toml"))
	s.write(".satelle/skills/satelle-children-closed-check.md", repoFile(".satelle/skills/satelle-children-closed-check.md"))
	for _, name := range []string{"satelle-verdict-notes-check", "satelle-verdict-reference-check"} {
		s.write(".satelle/skills/"+name+".md", repoFile(".satelle/skills/"+name+".md"))
	}
	for _, name := range []string{"satelle-story-done-review", "satelle-story-blocked-review", "satelle-story-cancel-review"} {
		s.write(".satelle/skills/"+name+".md", "---\nname: "+name+"\n---\nJudge it.\n")
	}
	s.write(".satelle/agents.toml", `[agents.reviewer-alt]
command = "claude"
model = "ra-1"
tools = ["Read", "Grep", "Glob"]
`)
	s.write(".satelle/workflows/runbook.toml", `name = "runbook"
[states.backlog]
start = true
[states.in_progress]
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "done"
`)
	s.write(".satelle/workflows/standing.toml", `name = "standing"
[states.defined]
start = true
[states.retired]
terminal = true
[[transition]]
from = ["defined"]
to = "retired"
`)
	// A plain flow until the task and queue the real one names exist.
	s.write(".satelle/workflows/flow.toml", `name = "flow"
[states.backlog]
start = true
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "done"
`)
	task := s.create("task", "--title", "Release approved items")
	q := s.create("queue", "--title", "release")
	runCheck := strings.Replace(repoFile(".satelle/skills/satelle-release-run-check.md"), "\ntask=tas_42301d36\n", "\ntask="+task.ID+"\n", 1)
	if !strings.Contains(runCheck, "\ntask="+task.ID+"\n") {
		t.Fatal(".satelle/skills/satelle-release-run-check.md has no task=tas_42301d36 line")
	}
	s.write(".satelle/skills/satelle-release-run-check.md", runCheck)
	s.write(".satelle/workflows/flow.toml", `name = "flow"
[states.backlog]
start = true
[states.in_progress]
[states.approved]
enqueue = ["release"]
[states.done]
terminal = true
[gates.release-run]
skill = "satelle-release-run-check"
[[transition]]
from = ["backlog"]
to = "approved"
[[transition]]
from = ["approved"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "approved"
[[transition]]
from = ["approved"]
to = "done"
groups = [{ mode = "sequential", gates = ["release-run"] }]
`)

	// The queue starts empty: only the epic's children ever enter approved.
	releaseTOML := repoReleaseQueue(t)
	releaseTOML = strings.Replace(releaseTOML, `id = "que_65bc4edc"`, `id = "`+q.ID+`"`, 1)
	releaseTOML = strings.Replace(releaseTOML, `task = "tas_42301d36"`, `task = "`+task.ID+`"`, 1)
	s.write(".satelle/queues/release.toml", releaseTOML)
	s.must("validate")

	epic := s.create("story", "--title", "epic", "--tag", "epic")
	if epic.Workflow != "epic" {
		t.Fatalf("the epic resolved to workflow %q", epic.Workflow)
	}
	s.must("story", "set", epic.ID, "--status", "underway")
	a := s.create("story", "--title", "child a", "--parent", epic.ID).ID
	bID := s.create("story", "--title", "child b", "--parent", epic.ID).ID

	// Every child's pull request is on origin/main.
	head := strings.TrimSpace(s.git("rev-parse", "origin/main"))
	prJSON, _ := json.Marshal([]map[string]string{
		{"headRefOid": head, "title": "change (" + a + ")"},
		{"headRefOid": head, "title": "change (" + bID + ")"},
	})
	bin := realDir(t, t.TempDir())
	writeFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\ncat <<'EOF'\n"+string(prJSON)+"\nEOF\n", 0o755)
	s.path = append(s.path, bin)

	ready := shBlockVar(t, principle, "## Driving an epic", "epic", epic.ID)
	step0 := shBlock(t, ".satelle/skills/release.md", "## 0.", task.ID)
	fireLine := "every open child of " + epic.ID + " is approved: fire the release"
	closeLine := "no open child of " + epic.ID + ": close it"
	readiness := func() result {
		t.Helper()
		return s.exec("bash", "", "-c", ready)
	}
	// fire runs release step 0 and returns the run and its inputs, checking
	// the lines after the seated line match the task.run record.
	fire := func(want ...string) string {
		t.Helper()
		r := s.exec("bash", "", "-c", step0)
		if r.code != 0 {
			t.Fatalf("step 0: exit %d\n%s%s", r.code, r.out, r.err)
		}
		lines := strings.Split(strings.TrimRight(r.out, "\n"), "\n")
		seatRE := regexp.MustCompile(`^seated release-it on (exe_[0-9a-f]{8}) at `)
		seatAt, exe := -1, ""
		for i, l := range lines {
			if m := seatRE.FindStringSubmatch(l); m != nil {
				seatAt, exe = i, m[1]
			}
		}
		if seatAt < 0 {
			t.Fatalf("no seated line:\n%s", r.out)
		}
		if seatAt+1 >= len(lines) || !strings.HasPrefix(lines[seatAt+1], "attached task to "+exe+" ") {
			t.Fatalf("step 0 does not attach the task right after the seat:\n%s", r.out)
		}
		got := slices.Clone(lines[seatAt+2:])
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("inputs after the seated line %v, want %v\n%s", got, want, r.out)
		}
		var rec struct {
			Inputs []string `json:"inputs"`
		}
		trs := s.ledger(exe, "task.run")
		if len(trs) != 1 || json.Unmarshal(trs[0].Payload, &rec) != nil {
			t.Fatalf("task.run %+v", trs)
		}
		slices.Sort(rec.Inputs)
		if !slices.Equal(rec.Inputs, want) {
			t.Fatalf("task.run inputs %v, want %v", rec.Inputs, want)
		}
		return exe
	}

	// A is approved, B is still in backlog: no fire, and the epic stays open.
	s.must("story", "set", a, "--status", "approved")
	if r := readiness(); r.code != 1 || !strings.Contains(r.out, bID+" backlog") || strings.Contains(r.out, a) ||
		!strings.Contains(r.err, "drive these to approved before the fire") {
		t.Fatalf("readiness with B in backlog: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := s.run("story", "set", epic.ID, "--status", "done"); r.code != 2 || !strings.Contains(r.err, "gate children-closed") {
		t.Fatalf("epic done with open children: exit %d\n%s%s", r.code, r.out, r.err)
	}

	// Both approved: the block says fire, and run 1 takes exactly A and B.
	s.must("story", "set", bID, "--status", "approved")
	if r := readiness(); r.code != 0 || strings.TrimSpace(r.out) != fireLine {
		t.Fatalf("readiness with both approved: exit %d\n%s%s", r.code, r.out, r.err)
	}
	run1 := fire(a, bID)

	// Run 1 sends B back (step 2c) and closes A.
	r := s.exec(satelleBin, "head 0000 does not contain main\n", "story", "attach", bID, "--name", "release-sendback", "--file", "-")
	if r.code != 0 {
		t.Fatalf("attach release-sendback: exit %d\n%s%s", r.code, r.out, r.err)
	}
	s.must("story", "set", bID, "--status", "in_progress")
	s.must("story", "set", a, "--status", "done")
	s.must("execution", "set", run1, "--status", "done")

	// The sent-back child keeps the epic underway.
	if r := readiness(); r.code != 1 || !strings.Contains(r.out, bID+" in_progress") ||
		!strings.Contains(r.err, "drive these to approved before the fire") {
		t.Fatalf("readiness after the send-back: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := s.run("story", "set", epic.ID, "--status", "done"); r.code != 2 || !strings.Contains(r.err, "gate children-closed") || !strings.Contains(r.err, bID) {
		t.Fatalf("epic done with B sent back: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if st := s.get(epic.ID).Status; st != "underway" {
		t.Fatalf("epic status after the send-back %q", st)
	}

	// B is driven to approved again; run 2 takes only B.
	s.must("story", "set", bID, "--status", "approved")
	if r := readiness(); r.code != 0 || strings.TrimSpace(r.out) != fireLine {
		t.Fatalf("readiness with B approved again: exit %d\n%s%s", r.code, r.out, r.err)
	}
	run2 := fire(bID)
	s.must("story", "set", bID, "--status", "done")
	s.must("execution", "set", run2, "--status", "done")

	// No open child: the block says close it, and the epic closes through
	// its gates.
	if r := readiness(); r.code != 0 || strings.TrimSpace(r.out) != closeLine {
		t.Fatalf("readiness with every child done: exit %d\n%s%s", r.code, r.out, r.err)
	}
	s.must("story", "set", epic.ID, "--status", "done")
	if st := s.get(epic.ID).Status; st != "done" {
		t.Fatalf("epic status %q, want done", st)
	}
	runs := s.gateRuns(epic.ID)
	if len(runs) < 2 {
		t.Fatalf("epic gate runs %+v", runs)
	}
	last := runs[len(runs)-2:]
	if last[0].Gate != "children-closed" || last[0].Result != "accept" || last[1].Gate != "done-review" || last[1].Result != "accept" {
		t.Fatalf("the closing round %+v", last)
	}
}
