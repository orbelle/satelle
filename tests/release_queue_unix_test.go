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

	"github.com/bobmcallan/satelle/internal/item"
)

// shBlock returns the first ```sh block after heading in a repo file, fence to
// fence, with its task= line rebound to task.
func shBlock(t *testing.T, rel, heading, task string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	if heading != "" {
		i := strings.Index(doc, heading)
		if i < 0 {
			t.Fatalf("%s has no %q", rel, heading)
		}
		doc = doc[i:]
	}
	_, rest, ok := strings.Cut(doc, "```sh\n")
	if !ok {
		t.Fatalf("%s has no sh block after %q", rel, heading)
	}
	block, _, ok := strings.Cut(rest, "```")
	if !ok {
		t.Fatalf("%s: unclosed sh block", rel)
	}
	re := regexp.MustCompile(`(?m)^task=\S+$`)
	if len(re.FindAllString(block, -1)) != 1 {
		t.Fatalf("%s: the block has no single task= line:\n%s", rel, block)
	}
	return re.ReplaceAllString(block, "task="+task)
}

// The release procedure's step 0, run as written, fires the release run from
// the release queue over exactly the approved stories, and the release-run
// check accepts each of them.
func TestReleaseStepZeroFiresFromTheQueue(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the release blocks and is not on PATH", tool)
		}
	}
	s := newSandbox(t)
	s.env["SATELLE_SESSION"] = "release-it"
	origin := realDir(t, t.TempDir())
	s.exec("git", "", "init", "-q", "--bare", origin)
	s.git("remote", "add", "origin", origin)
	s.git("push", "-q", "origin", "HEAD:refs/heads/main")
	s.must("init")

	// The queue rule exactly as this repo's settings carry it.
	settings, err := os.ReadFile("../.satelle/satelle.toml")
	if err != nil {
		t.Fatal(err)
	}
	rule := regexp.MustCompile(`(?m)^\[\[workflows\.rule\]\]\nworkflow = "standing"\nkind = "queue"\n`).FindString(string(settings))
	if rule == "" {
		t.Fatal(".satelle/satelle.toml has no queue rule")
	}
	s.write(".satelle/satelle.toml", `[workflows]
default = "flow"
[[workflows.rule]]
workflow = "runbook"
kind = "execution"
[[workflows.rule]]
workflow = "standing"
kind = "task"
`+rule)
	s.write(".satelle/workflows/flow.toml", `name = "flow"
[states.backlog]
start = true
[states.approved]
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "approved"
[[transition]]
from = ["approved"]
to = "done"
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
	s.must("validate")
	task := s.create("task", "--title", "Release approved items", "--input-kind", "story", "--input-status", "approved")
	var approved []string
	var other string
	for i := range 3 {
		st := s.create("story", "--title", "story")
		if i < 2 {
			s.must("story", "set", st.ID, "--status", "approved")
			approved = append(approved, st.ID)
		} else {
			other = st.ID
		}
	}
	step0 := shBlock(t, ".satelle/skills/release.md", "## 0.", task.ID)

	// Run 1: creates the queue, clears the task's inputs, fires, seats.
	r := s.exec("bash", "", "-c", step0)
	if r.code != 0 {
		t.Fatalf("step 0: exit %d\n%s%s", r.code, r.out, r.err)
	}
	lines := strings.Split(strings.TrimRight(r.out, "\n"), "\n")
	seatRE := regexp.MustCompile(`^seated release-it on (exe_[0-9a-f]{8}) at local:.+ \(base ([0-9a-f]+)\)$`)
	seatAt, exe, base := -1, "", ""
	for i, l := range lines {
		if strings.HasPrefix(l, "Usage:") {
			t.Fatalf("the guard printed help:\n%s", r.out)
		}
		if m := seatRE.FindStringSubmatch(l); m != nil {
			if seatAt >= 0 {
				t.Fatalf("two seated lines:\n%s", r.out)
			}
			seatAt, exe, base = i, m[1], m[2]
		}
	}
	if seatAt < 0 {
		t.Fatalf("no seated line:\n%s", r.out)
	}
	if head := strings.TrimSpace(s.git("rev-parse", "HEAD")); !strings.HasPrefix(head, base) {
		t.Fatalf("seat base %s is not HEAD %s", base, head)
	}
	got := slices.Clone(lines[seatAt+1:])
	slices.Sort(got)
	want := slices.Clone(approved)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("inputs after the seated line %v, want %v\n%s", got, want, r.out)
	}
	before := strings.Join(lines[:seatAt], "\n")
	if !strings.Contains(before, exe+": backlog -> in_progress") || !strings.Contains(before, task.ID+" updated") {
		t.Fatalf("step 0 output before the seat:\n%s", before)
	}

	var queues []item.Item
	s.mustJSON(&queues, "queue", "list")
	if len(queues) != 1 {
		t.Fatalf("queues %+v", queues)
	}
	q := queues[0]
	if q.Inputs == nil || q.Inputs.Kind != "story" || q.Inputs.Status != "approved" || q.Trigger == nil ||
		q.Trigger.Task != task.ID || q.Trigger.Base != "origin/main" || q.Trigger.MinSize != 0 {
		t.Fatalf("release queue %+v", q)
	}
	var tk item.Item
	s.mustJSON(&tk, "task", "get", task.ID)
	if tk.Inputs != nil {
		t.Fatalf("task inputs not cleared: %+v", tk.Inputs)
	}
	var runs []item.Item
	s.mustJSON(&runs, "execution", "list", "--parent", task.ID)
	if len(runs) != 1 || runs[0].ID != exe || runs[0].Status != "in_progress" {
		t.Fatalf("runs %+v", runs)
	}
	var rec struct {
		Inputs  []string `json:"inputs"`
		Queue   string   `json:"queue"`
		BaseRef string   `json:"base_ref"`
	}
	trs := s.ledger(exe, "task.run")
	if len(trs) != 1 || json.Unmarshal(trs[0].Payload, &rec) != nil {
		t.Fatalf("task.run %+v", trs)
	}
	slices.Sort(rec.Inputs)
	if !slices.Equal(rec.Inputs, want) || rec.Queue != q.ID || rec.BaseRef != "origin/main" {
		t.Fatalf("task.run %+v", rec)
	}
	seated := false
	for _, e := range s.ledger(exe, "seat.bound") {
		var st item.Seat
		seated = seated || (json.Unmarshal(e.Payload, &st) == nil && st.Session == "release-it")
	}
	if !seated {
		t.Fatal("no seat.bound for release-it")
	}
	fired := false
	for _, e := range s.ledger(q.ID, item.KindQueueFired) {
		var f item.QueueFire
		fired = fired || (json.Unmarshal(e.Payload, &f) == nil && f.Run == exe)
	}
	if !fired {
		t.Fatal("the queue records no fire of the run")
	}

	// The release-run check accepts each approved story and refuses the other.
	head := strings.TrimSpace(s.git("rev-parse", "origin/main"))
	var prs []map[string]string
	for _, id := range append(slices.Clone(approved), other) {
		prs = append(prs, map[string]string{"headRefOid": head, "title": "change (" + id + ")"})
	}
	prJSON, _ := json.Marshal(prs)
	bin := realDir(t, t.TempDir())
	writeFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\ncat <<'EOF'\n"+string(prJSON)+"\nEOF\n", 0o755)
	s.path = append(s.path, bin)
	check := shBlock(t, ".satelle/skills/satelle-release-run-check.md", "", task.ID)
	for _, id := range approved {
		s.env["SATELLE_ITEM"] = id
		if r := s.exec("bash", "", "-c", check); r.code != 0 || !strings.Contains(r.out, "release run "+exe+" lists "+id) {
			t.Fatalf("check %s: exit %d\n%s%s", id, r.code, r.out, r.err)
		}
	}
	s.env["SATELLE_ITEM"] = other
	if r := s.exec("bash", "", "-c", check); r.code != 1 {
		t.Fatalf("check %s (not in the run): exit %d\n%s", other, r.code, r.out)
	}
	delete(s.env, "SATELLE_ITEM")

	// Run 2 finds the same queue and is refused while the run is open.
	r = s.exec("bash", "", "-c", step0)
	if r.code != 2 || !strings.Contains(r.err, exe) {
		t.Fatalf("second step 0: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if strings.Contains(r.out, "seated ") || strings.Contains(r.out, " updated") {
		t.Fatalf("second step 0 created or seated:\n%s", r.out)
	}
	s.mustJSON(&queues, "queue", "list")
	s.mustJSON(&runs, "execution", "list", "--parent", task.ID)
	if len(queues) != 1 || len(runs) != 1 {
		t.Fatalf("after run 2: queues %d runs %d", len(queues), len(runs))
	}
}
