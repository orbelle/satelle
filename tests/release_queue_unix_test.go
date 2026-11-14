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

	"github.com/BurntSushi/toml"

	"github.com/bobmcallan/satelle/internal/item"
)

// shBlock returns the first ```sh block after heading in a repo file, fence to
// fence, with its task= line rebound to task.
func shBlock(t *testing.T, rel, heading, task string) string {
	t.Helper()
	return shBlockVar(t, rel, heading, "task", task)
}

// shBlockVar returns the first ```sh block after heading in a repo file, fence
// to fence, with its single name= line rebound to value.
func shBlockVar(t *testing.T, rel, heading, name, value string) string {
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
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=\S+$`)
	if len(re.FindAllString(block, -1)) != 1 {
		t.Fatalf("%s: the block has no single %s= line:\n%s", rel, name, block)
	}
	return bindSession(re.ReplaceAllString(block, name+"="+value))
}

// releaseSandbox is a repo whose main is pushed to a bare origin, carrying
// this repo's queue rule, a plain flow for stories (backlog -> approved ->
// done), the runbook and standing workflows, and the release task, which it
// returns with the sandbox and the origin's path.
func releaseSandbox(t *testing.T, tools ...string) (*sandbox, string, item.Item) {
	t.Helper()
	for _, tool := range append([]string{"bash", "jq", "git"}, tools...) {
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
	// The release-run check and the version gates read the run's binary
	// decision through this script, run from the presenting tree.
	writeFile(t, filepath.Join(s.dir, "scripts", "release-binary.sh"), repoText(t, "scripts/release-binary.sh"), 0o755)
	s.must("validate")
	return s, origin, s.create("task", "--title", "Release approved items", "--body", releaseTaskBody)
}

// releaseTaskBody is the release task's body as plan sto_da9775c1's
// Configuration to apply restates it: release.md step 0 fires only against a
// body naming the run's release-binary record.
const releaseTaskBody = `ACTION: follow .satelle/skills/release.md over the run's items.

## Binary decision
This section overrides any statement above that every run bumps, tags, publishes or installs.
ACTION: after the suite passes, the run records as its release-binary attachment whether the candidate changes the shipped binaries (release.md, Decide: did the binary change). binary: changed: bump, tag, publish and install as stated above. binary: unchanged: main fast-forwards to the tested candidate; the run does not bump, tag, dispatch release.yml or install.
VERIFICATION: release-binary is attached and its candidate is the tested sha.
`

// rebindTask is a repo file with its task=tas_42301d36 line, if any, rebound
// to task.
func rebindTask(t *testing.T, rel, task string) string {
	t.Helper()
	text := repoText(t, rel)
	re := regexp.MustCompile(`(?m)^task=tas_42301d36$`)
	if n := len(re.FindAllString(text, -1)); n > 1 {
		t.Fatalf("%s has %d task=tas_42301d36 lines", rel, n)
	}
	return re.ReplaceAllString(text, "task="+task)
}

// writeReleaseQueue writes the repo's release.toml into the sandbox, rebound
// to queue q and task.
func writeReleaseQueue(t *testing.T, s *sandbox, q, task string) {
	t.Helper()
	releaseTOML := repoReleaseQueue(t)
	for _, line := range []string{`id = "que_65bc4edc"`, `task = "tas_42301d36"`} {
		if !strings.Contains(releaseTOML, line+"\n") {
			t.Fatalf(".satelle/queues/release.toml lacks %s:\n%s", line, releaseTOML)
		}
	}
	releaseTOML = strings.Replace(releaseTOML, `id = "que_65bc4edc"`, `id = "`+q+`"`, 1)
	releaseTOML = strings.Replace(releaseTOML, `task = "tas_42301d36"`, `task = "`+task+`"`, 1)
	s.write(".satelle/queues/release.toml", releaseTOML)
}

// The release procedure's step 0, run as written, fires the release run from
// the release queue over exactly the approved stories, and the release-run
// check accepts each of them.
func TestReleaseStepZeroFiresFromTheQueue(t *testing.T) {
	s, _, task := releaseSandbox(t)

	// A queue created on the ledger before queues were files, with one
	// approved story added by hand: the repo's release.toml adopts it.
	q := s.create("queue", "--title", "legacy")
	legacy := s.create("story", "--title", "story")
	s.must("story", "set", legacy.ID, "--status", "approved")
	s.must("queue", "add", q.ID, legacy.ID)
	approved := []string{legacy.ID}

	// The repo's release.toml, rebound to this sandbox's queue and task.
	writeReleaseQueue(t, s, q.ID, task.ID)
	s.write(".satelle/workflows/flow.toml", `name = "flow"
[states.backlog]
start = true
[states.approved]
enqueue = ["release"]
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "approved"
[[transition]]
from = ["approved"]
to = "done"
`)
	s.must("validate")
	var other string
	for i := range 2 {
		st := s.create("story", "--title", "story")
		if i < 1 {
			if out := s.must("story", "set", st.ID, "--status", "approved"); !strings.Contains(out, "queued "+st.ID+" in release ("+q.ID+")") {
				t.Fatalf("moving %s to approved reported no enqueue into release (%s):\n%s", st.ID, q.ID, out)
			}
			approved = append(approved, st.ID)
		} else {
			other = st.ID
		}
	}
	var added, causes []string
	for _, e := range s.ledger(q.ID, item.KindQueueAdded) {
		var qe item.QueueEntry
		if json.Unmarshal(e.Payload, &qe) != nil {
			t.Fatalf("queue.added %s", e.Payload)
		}
		added, causes = append(added, qe.Item), append(causes, qe.Cause)
	}
	if !slices.Equal(added, approved) || !slices.Equal(causes, []string{"", "flow:backlog->approved"}) {
		t.Fatalf("explicit members %v causes %q, want %v", added, causes, approved)
	}
	var shown struct {
		ID        string `json:"id"`
		DefinedBy string `json:"defined_by"`
	}
	s.mustJSON(&shown, "queue", "show", "release")
	if shown.ID != q.ID || shown.DefinedBy != ".satelle/queues/release.toml" {
		t.Fatalf("queue show release %+v", shown)
	}
	step0 := shBlock(t, ".satelle/skills/release.md", "## 0.", task.ID)
	if m := regexp.MustCompile(`(?m)^que=(\S+)$`).FindAllStringSubmatch(step0, -1); len(m) != 1 || m[0][1] != "release" {
		t.Fatalf("step 0 names release queue %v, want one que=release line", m)
	}

	// Run 1: fires the queue and seats the run.
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
	if seatAt+1 >= len(lines) || !strings.HasPrefix(lines[seatAt+1], "attached task to "+exe+" ") {
		t.Fatalf("step 0 does not attach the task right after the seat:\n%s", r.out)
	}
	got := slices.Clone(lines[seatAt+2:])
	slices.Sort(got)
	want := slices.Clone(approved)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("inputs after the seated line %v, want %v\n%s", got, want, r.out)
	}
	before := strings.Join(lines[:seatAt], "\n")
	if !strings.Contains(before, exe+": backlog -> in_progress") {
		t.Fatalf("step 0 output before the seat:\n%s", before)
	}

	var queues []item.Item
	s.mustJSON(&queues, "queue", "list")
	if len(queues) != 1 || queues[0].ID != q.ID {
		t.Fatalf("queues %+v", queues)
	}
	q = queues[0]
	if q.Inputs == nil || q.Inputs.Kind != "story" || q.Inputs.Status != "approved" || q.Trigger == nil ||
		q.Trigger.Task != task.ID || q.Trigger.Base != "origin/main" || q.Trigger.MinSize != 0 {
		t.Fatalf("release queue %+v", q)
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

// repoReleaseQueue is this repo's .satelle/queues/release.toml, after
// checking that the delivery and chore workflows enqueue approved stories
// into it by name.
func repoReleaseQueue(t *testing.T) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\[states\.approved\]\n(?:#.*\n)*enqueue = \["release"\]$`)
	for _, wf := range []string{"delivery", "chore"} {
		b, err := os.ReadFile("../.satelle/workflows/" + wf + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		if !re.Match(b) {
			t.Fatalf("%s.toml: the approved state does not enqueue into release", wf)
		}
	}
	b, err := os.ReadFile("../.satelle/queues/release.toml")
	if err != nil {
		t.Fatal(err)
	}
	var def struct {
		Title   string
		Filter  struct{ Kind, Status string }
		Trigger struct {
			Task, Base string
			MinSize    int `toml:"min_size"`
		}
	}
	if _, err := toml.Decode(string(b), &def); err != nil {
		t.Fatal(err)
	}
	if def.Filter.Kind != "story" || def.Filter.Status != "approved" || def.Trigger.Task != "tas_42301d36" ||
		def.Trigger.Base != "origin/main" || def.Trigger.MinSize != 0 {
		t.Fatalf(".satelle/queues/release.toml %+v", def)
	}
	return string(b)
}

// bindSession fills a procedure block's session=<session> line the way a
// session does with its id: here with the sandbox's own $SATELLE_SESSION
// (sto_2a41b9e4). An unfilled line is a shell syntax error by design.
func bindSession(block string) string {
	return regexp.MustCompile(`(?m)^session=<session>`).ReplaceAllString(block, `session="${SATELLE_SESSION:-}"`)
}

// sto_2ac90ab6 AC3: this repo declares its outside waits in its workflow
// files: drift's open and standing's defined wait; the approved state of
// delivery and chore does not (the session fires the release queue from it).
func TestRepoWorkflowsDeclareTheirWaits(t *testing.T) {
	waits := func(wf, state string) string {
		t.Helper()
		var def struct {
			States map[string]struct {
				Waits string `toml:"waits"`
			} `toml:"states"`
		}
		if _, err := toml.DecodeFile("../.satelle/workflows/"+wf+".toml", &def); err != nil {
			t.Fatal(err)
		}
		st, ok := def.States[state]
		if !ok {
			t.Fatalf("%s.toml has no state %s", wf, state)
		}
		return strings.TrimSpace(st.Waits)
	}
	for _, c := range [][2]string{{"drift", "open"}, {"standing", "defined"}} {
		if waits(c[0], c[1]) == "" {
			t.Errorf("%s.toml: %s declares no waits", c[0], c[1])
		}
	}
	for _, wf := range []string{"delivery", "chore"} {
		if w := waits(wf, "approved"); w != "" {
			t.Errorf("%s.toml: approved declares waits %q; the session fires the release queue from it", wf, w)
		}
	}
	repoReleaseQueue(t)
}

// sto_2ac90ab6 AC4: no substrate text a session reads says the fire is by
// hand, or that approved is the session's end without naming who fires.
func TestSubstrateNamesWhoFiresAtApproved(t *testing.T) {
	root := filepath.Join("..", ".satelle")
	var files []string
	for _, dir := range []string{"principles", "skills", "workflows", "queues"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) == 0 {
		t.Fatal("no substrate files under ../.satelle")
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	byHand := []string{"fired by hand", "fires by hand", "by hand only"}
	ends := regexp.MustCompile(`(?i)DEV ends|DEV's end|end DEV|ends at approved|waits at approved|awaiting release`)
	for _, p := range files {
		body := read(p)
		for _, w := range byHand {
			if strings.Contains(body, w) {
				t.Errorf("%s says %q", p, w)
			}
		}
		if m := ends.FindString(body); m != "" && !strings.Contains(body, "queue fire") {
			t.Errorf("%s says %q without naming the queue fire", p, m)
		}
	}
	var conf struct {
		Hook struct {
			Inject []string `toml:"inject"`
		} `toml:"hook"`
	}
	if _, err := toml.DecodeFile(filepath.Join(root, "satelle.toml"), &conf); err != nil {
		t.Fatal(err)
	}
	principles := 0
	for _, f := range conf.Hook.Inject {
		if !strings.HasPrefix(f, ".satelle/principles/") {
			continue
		}
		principles++
		if !strings.Contains(read(filepath.Join("..", f)), "queue fire") {
			t.Errorf("injected %s does not name the queue fire", f)
		}
	}
	if principles == 0 {
		t.Fatal("satelle.toml injects no principle")
	}
	arch := read(filepath.Join(root, "principles", "satelle-story-task-release-architecture.md"))
	if !strings.Contains(arch, "queue fire") {
		t.Error("the release architecture does not name the queue fire")
	}
	for _, w := range []string{"holds no agent seat", "not a continuation"} {
		if strings.Contains(arch, w) {
			t.Errorf("the release architecture still says %q", w)
		}
	}
}

// sto_da9775c1: release.md step 0 refuses to fire against a release task whose
// body does not name the run's release-binary record, so no run is judged
// against a task that says every run bumps, tags, publishes and installs; no
// run is created. The same sandbox with the restated body fires.
func TestReleaseRefusesATaskWithoutTheBinaryDecision(t *testing.T) {
	s, _, _ := releaseSandbox(t)
	task := s.create("task", "--title", "Release approved items", "--body", "ACTION: follow .satelle/skills/release.md.\n")
	q := s.create("queue", "--title", "release")
	writeReleaseQueue(t, s, q.ID, task.ID)
	s.must("validate")
	st := s.create("story", "--title", "story")
	s.must("story", "set", st.ID, "--status", "approved")
	s.must("queue", "add", q.ID, st.ID)
	runs := func() int {
		var out []item.Item
		s.mustJSON(&out, "execution", "list", "--parent", task.ID)
		return len(out)
	}
	r := s.exec("bash", "", "-c", shBlock(t, ".satelle/skills/release.md", "## 0.", task.ID))
	if r.code != 1 || !strings.Contains(r.err, "does not name the run's release-binary record") {
		t.Fatalf("step 0 on a task without the binary decision: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := runs(); n != 0 {
		t.Fatalf("a refused step 0 created %d runs", n)
	}
	s.must("task", "set", task.ID, "--body", releaseTaskBody)
	if r := s.exec("bash", "", "-c", shBlock(t, ".satelle/skills/release.md", "## 0.", task.ID)); r.code != 0 {
		t.Fatalf("step 0 on the restated task: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := runs(); n != 1 {
		t.Fatalf("step 0 on the restated task created %d runs", n)
	}
}
