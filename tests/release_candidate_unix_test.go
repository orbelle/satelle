//go:build integration && unix

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// candidateSandbox is a release sandbox whose origin refuses non-fast-forward
// pushes and whose main carries a Makefile: its test target appends the tested
// HEAD to $SUITE_LOG (and fails when red). The stories take a flow that
// enqueues approved stories into the release queue, sends them back to
// in_progress, and closes them only through this repo's release-run check. A
// fake gh lists the open pull requests.
type candidateSandbox struct {
	*changelogSandbox
	task     item.Item
	suiteLog string
	tmp      string
	ids      []string
	held     string // the changelog verdicts' send-back lines, pasted into the candidate block
}

// fakeStateGH lists prs.json's pull requests in the state --state names (open
// when none is given, every one for all, as gh does); a pull request with no
// state is open. Any other gh call fails.
const fakeStateGH = `#!/bin/sh
case "$1" in
  pr)
    s=open; prev=
    for a in "$@"; do [ "$prev" = --state ] && s=$a; prev=$a; done
    jq --arg s "$s" '[.[] | select($s == "all" or ((.state // "OPEN") | ascii_downcase) == $s)]' "$FAKE_DIR/prs.json" ;;
  *) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
`

func newCandidateSandbox(t *testing.T, red bool) *candidateSandbox {
	t.Helper()
	s, origin, task := releaseSandbox(t, "make")
	s.exec("git", "", "-C", origin, "config", "receive.denyNonFastForwards", "true")
	c := &candidateSandbox{changelogSandbox: &changelogSandbox{sandbox: s}, task: task}
	c.tmp = realDir(t, t.TempDir())
	c.suiteLog = filepath.Join(realDir(t, t.TempDir()), "suite.log")
	s.env["TMPDIR"] = c.tmp
	s.env["SUITE_LOG"] = c.suiteLog

	mk := "test:\n\tgit rev-parse HEAD >>\"$$SUITE_LOG\"\n"
	if red {
		mk += "\texit 1\n"
	}
	s.write("Makefile", mk)
	s.write("a.txt", "base\n")
	s.write(".version", "satelle.version: 1.0.0\nsatelled.version: 1.0.0\n")
	s.write("CHANGELOG.md", baseChangelog)
	writeFile(t, filepath.Join(s.dir, "scripts", "changelog-items.sh"), repoText(t, "scripts/changelog-items.sh"), 0o755)
	s.git("add", "Makefile", "a.txt", ".version", "CHANGELOG.md", "scripts/changelog-items.sh")
	s.git("commit", "-q", "-m", "base")
	s.git("push", "-q", "origin", "HEAD:refs/heads/main")
	s.git("fetch", "-q", "origin")
	c.base = s.rev("HEAD")

	q := s.create("queue", "--title", "release")
	writeReleaseQueue(t, s, q.ID, task.ID)
	check, err := os.ReadFile("../.satelle/skills/satelle-release-run-check.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^task=\S+$`)
	if len(re.FindAll(check, -1)) != 1 {
		t.Fatal("satelle-release-run-check.md has no single task= line")
	}
	s.write(".satelle/skills/satelle-release-run-check.md", re.ReplaceAllString(string(check), "task="+task.ID))
	s.write(".satelle/workflows/flow.toml", `name = "flow"
[states.backlog]
start = true
[states.approved]
enqueue = ["release"]
[states.in_progress]
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
from = ["approved"]
to = "done"
groups = [{ mode = "sequential", gates = ["release-run"] }]
`)
	s.must("validate")

	c.fake = realDir(t, t.TempDir())
	writeFile(t, filepath.Join(c.fake, "gh"), fakeStateGH, 0o755)
	writeFile(t, filepath.Join(c.fake, "prs.json"), "[]", 0o644)
	s.path = append(s.path, c.fake)
	s.env["FAKE_DIR"] = c.fake
	return c
}

// item cuts a branch from main that commits path=body, approves a story for
// it (enqueuing it into release) and lists its pull request. It returns the
// story id and the head.
func (c *candidateSandbox) item(name, path, body string) (string, string) {
	c.t.Helper()
	head := c.branch(name, c.base, [2]string{path, body})
	id := c.create("story", "--title", "change "+name).ID
	c.must("story", "set", id, "--status", "approved")
	c.pr(id, head)
	c.ids = append(c.ids, id)
	return id, head
}

// block is a release.md block after heading, task rebound, run in the repo.
func (c *candidateSandbox) block(heading string) result {
	c.t.Helper()
	b := shBlock(c.t, ".satelle/skills/release.md", heading, c.task.ID)
	if heading == "### Build the candidate" {
		re := regexp.MustCompile(`(?m)^<each "<id>: send back: <reason>" line the changelog verdicts printed>$`)
		if len(re.FindAllString(b, -1)) != 1 {
			c.t.Fatalf("the candidate block has no held placeholder:\n%s", b)
		}
		b = re.ReplaceAllLiteralString(b, c.held)
	}
	return c.exec("bash", "", "-c", b)
}

func (c *candidateSandbox) mustBlock(heading string) string {
	c.t.Helper()
	r := c.block(heading)
	if r.code != 0 {
		c.t.Fatalf("%s: exit %d\n%s%s", heading, r.code, r.out, r.err)
	}
	return r.out
}

// fire runs step 0 and returns the run and its state directory.
func (c *candidateSandbox) fire() (string, string) {
	c.t.Helper()
	c.mustBlock("## 0.")
	var runs []item.Item
	c.mustJSON(&runs, "execution", "list", "--parent", c.task.ID, "--status", "in_progress")
	if len(runs) != 1 || runs[0].Status != "in_progress" {
		c.t.Fatalf("runs %+v", runs)
	}
	return runs[0].ID, filepath.Join(c.tmp, "satelle-release-"+runs[0].ID)
}

// merged marks id's pull request merged: the fake gh no longer lists it open.
func (c *candidateSandbox) merged(id string) {
	c.t.Helper()
	found := false
	for _, p := range c.prs {
		if strings.HasSuffix(p["title"].(string), "("+id+")") {
			p["state"], found = "MERGED", true
		}
	}
	if !found {
		c.t.Fatalf("no pull request for %s", id)
	}
	b, _ := json.Marshal(c.prs)
	writeFile(c.t, filepath.Join(c.fake, "prs.json"), string(b), 0o644)
}

// transitions counts id's item.transitioned events from -> to.
func (c *candidateSandbox) transitions(id, from, to string) int {
	c.t.Helper()
	n := 0
	for _, e := range c.ledger(id, item.KindTransitioned) {
		var tr struct{ From, To string }
		if json.Unmarshal(e.Payload, &tr) == nil && tr.From == from && tr.To == to {
			n++
		}
	}
	return n
}

func (c *candidateSandbox) ancestor(a, b string) bool {
	c.t.Helper()
	return c.exec("git", "", "merge-base", "--is-ancestor", a, b).code == 0
}

func (c *candidateSandbox) suiteRuns() []string {
	c.t.Helper()
	b, err := os.ReadFile(c.suiteLog)
	if err != nil && !os.IsNotExist(err) {
		c.t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func (c *candidateSandbox) status(id string) string {
	c.t.Helper()
	return c.get(id).Status
}

// lines are the item lines a candidate block printed, in order, without the
// final candidate line.
func itemLines(out string) []string {
	var got []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(l, "candidate ") {
			got = append(got, l)
		}
	}
	return got
}

var candidateRE = regexp.MustCompile(`(?m)^candidate ([0-9a-f]{40}) at (\S+)$`)

// AC1, AC2: three approved items cut from the same main, touching no shared
// file, land in one run: the first fast-forwards, the others merge onto the
// candidate in queue order, make test runs once on the final candidate, and
// main fast-forwards only to that tested tree (plus the bump); every item then
// reaches done through the release-run gate while the run is in progress.
func TestReleaseLandsEveryApprovedItem(t *testing.T) {
	c := newCandidateSandbox(t, false)
	idA, headA := c.item("a", "a2.txt", "a\n")
	idB, headB := c.item("b", "b.txt", "b\n")
	idC, headC := c.item("c", "c.txt", "c\n")
	oldMain := c.rev("origin/main")
	run, rd := c.fire()

	out := c.mustBlock("### Build the candidate")
	want := []string{idA + ": fast-forward head " + headA, idB + ": merged head " + headB, idC + ": merged head " + headC}
	if got := itemLines(out); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("candidate lines (queue order):\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	m := candidateRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no candidate line:\n%s", out)
	}
	cand, wt := m[1], m[2]
	if wt != filepath.Join(rd, "candidate") {
		t.Fatalf("candidate at %s, want under %s", wt, rd)
	}
	if out := c.mustBlock("### Send back"); out != "" {
		t.Fatalf("send back with nothing to send:\n%s", out)
	}
	c.mustBlock("### Test the candidate once")
	if runs := c.suiteRuns(); len(runs) != 1 || runs[0] != cand {
		t.Fatalf("make test ran on %v, want once on the candidate %s", runs, cand)
	}

	// AC2: the candidate changed after the suite ran is never pushed.
	writeFile(t, filepath.Join(wt, "late.txt"), "late\n", 0o644)
	c.exec("git", "", "-C", wt, "add", "late.txt")
	c.exec("git", "", "-C", wt, "commit", "-q", "-m", "late change")
	if r := c.block("## 4."); r.code == 0 || !strings.Contains(r.err, "changed after the suite ran") {
		t.Fatalf("step 4 on a changed candidate: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := c.rev("origin/main"); got != oldMain {
		t.Fatalf("origin/main moved to %s on a refused push", got)
	}
	c.exec("git", "", "-C", wt, "reset", "-q", "--hard", cand)

	// The bump (step 3's two files) on top of the tested candidate is pushed.
	writeFile(t, filepath.Join(wt, ".version"), "satelle.version: 1.0.1\n", 0o644)
	c.exec("git", "", "-C", wt, "add", ".version")
	c.exec("git", "", "-C", wt, "commit", "-q", "-m", "chore(release): v1.0.1")
	bump := strings.TrimSpace(c.exec("git", "", "-C", wt, "rev-parse", "HEAD").out)
	c.mustBlock("## 4.")
	c.git("fetch", "-q", "origin")
	if got := c.rev("origin/main"); got != bump || c.rev(bump+"^") != cand {
		t.Fatalf("origin/main %s, want the bump %s on the tested candidate %s", got, bump, cand)
	}
	if !c.ancestor(oldMain, cand) {
		t.Fatal("origin/main did not fast-forward from the old main")
	}
	for _, h := range []string{headA, headB, headC} {
		if !c.ancestor(h, "origin/main") {
			t.Fatalf("head %s (never rewritten) is not on origin/main", h)
		}
	}
	if n := strings.Fields(c.git("rev-list", "--merges", oldMain+"..origin/main")); len(n) != 2 {
		t.Fatalf("%d merge commits, want 2", len(n))
	}
	if runs := c.suiteRuns(); len(runs) != 1 {
		t.Fatalf("make test ran %d times", len(runs))
	}
	if _, err := os.Stat(rd); !os.IsNotExist(err) {
		t.Fatalf("the run directory %s survives the push: %v", rd, err)
	}
	// AC1 (sto_4a863578): step 4 recorded the kept list on the run.
	if got, want := c.must("execution", "attachment", run, "release-kept"), idA+" "+headA+"\n"+idB+" "+headB+"\n"+idC+" "+headC+"\n"; got != want {
		t.Fatalf("release-kept on %s:\n%s\nwant\n%s", run, got, want)
	}

	for _, id := range []string{idA, idB, idC} {
		if r := c.run("story", "set", id, "--status", "done"); r.code != 0 {
			t.Fatalf("done %s: exit %d\n%s%s", id, r.code, r.out, r.err)
		}
		if st := c.status(id); st != "done" {
			t.Fatalf("%s is %s", id, st)
		}
		if g := latestRun(c.sandbox, id, "release-run"); g.Result != "accept" {
			t.Fatalf("release-run on %s: %+v", id, g)
		}
	}
}

// A head cut from an older main does not edit CHANGELOG.md just because main
// gained a release section since: the changelog verdict reads the head's own
// commits, so a head the candidate merges is kept.
func TestChangelogVerdictReadsTheHeadsOwnCommits(t *testing.T) {
	c := newChangelogSandbox(t)
	id := c.story("### Added\n- A adds\n")
	head := c.branch("a", c.base, [2]string{"a.txt", "a\n"})
	wt := c.worktree("origin/main", true)
	// A release since the head was cut writes its section on main.
	c.at(wt, func() { c.write("CHANGELOG.md", "## [1.0.1] - 2026-02-01\n\n### Added\n- Z adds\n\n"+baseChangelog) })
	c.bump(wt, "1.0.1")
	c.assertFixtures([]string{head})
	main := c.rev("origin/main")
	if c.exec("git", "", "diff", "--quiet", main, head, "--", "CHANGELOG.md").code == 0 {
		t.Fatal("setup: main's CHANGELOG.md does not differ from the head's")
	}
	if got, want := c.verdict(id, head, main), id+": keep head "+head; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

// AC3: an item whose merge conflicts is dropped from the candidate and sent
// back alone, naming the conflicting paths; the others still land. An item
// the changelog verdicts sent back (pasted into held) never enters it.
func TestReleaseConflictSendsBackOne(t *testing.T) {
	c := newCandidateSandbox(t, false)
	idX, headX := c.item("x", "a.txt", "x\n")
	idY, headY := c.item("y", "a.txt", "y\n")
	idZ, headZ := c.item("z", "c.txt", "z\n")
	idH, headH := c.item("h", "h.txt", "h\n")
	heldWhy := "head " + headH + " edits CHANGELOG.md; restore it"
	c.held = idH + ": send back: " + heldWhy + "\n" + idX + ": keep head " + headX
	_, rd := c.fire()

	out := c.mustBlock("### Build the candidate")
	got := itemLines(out)
	if len(got) != 4 || got[0] != idX+": fast-forward head "+headX || got[1] != idY+": sent back: head "+headY+" conflicts in: a.txt" ||
		got[2] != idZ+": merged head "+headZ || got[3] != idH+": sent back: its changelog" {
		t.Fatalf("candidate lines:\n%s", out)
	}
	wt := filepath.Join(rd, "candidate")
	if r := c.exec("git", "", "-C", wt, "rev-parse", "-q", "--verify", "MERGE_HEAD"); r.code == 0 {
		t.Fatal("the candidate has a merge in progress")
	}
	if st := strings.TrimSpace(c.exec("git", "", "-C", wt, "status", "--porcelain").out); st != "" {
		t.Fatalf("the candidate is not clean:\n%s", st)
	}

	if out := c.mustBlock("### Send back"); !strings.Contains(out, idY+": sent back: ") {
		t.Fatalf("send back:\n%s", out)
	}
	if st := c.status(idY); st != "in_progress" {
		t.Fatalf("%s is %s, want in_progress", idY, st)
	}
	reason := c.must("story", "attachment", idY, "release-sendback")
	if !strings.Contains(reason, "head "+headY+" conflicts") || !strings.Contains(reason, "after "+idX+" in: a.txt;") {
		t.Fatalf("release-sendback on %s: %q", idY, reason)
	}
	if got := c.must("story", "attachment", idH, "release-sendback"); strings.TrimSpace(got) != heldWhy || c.status(idH) != "in_progress" {
		t.Fatalf("held %s: %s, release-sendback %q", idH, c.status(idH), got)
	}
	c.mustBlock("### Test the candidate once")
	if runs := c.suiteRuns(); len(runs) != 1 {
		t.Fatalf("make test ran %d times", len(runs))
	}
	c.mustBlock("## 4.")
	c.git("fetch", "-q", "origin")
	if c.ancestor(headY, "origin/main") || c.ancestor(headH, "origin/main") {
		t.Fatal("the conflicting or held head landed")
	}
	for _, id := range []string{idX, idZ} {
		if r := c.run("story", "set", id, "--status", "done"); r.code != 0 {
			t.Fatalf("done %s: exit %d\n%s%s", id, r.code, r.out, r.err)
		}
	}
	if st := c.status(idY); st != "in_progress" {
		t.Fatalf("%s is %s", idY, st)
	}
}

// AC2, AC4: a red candidate lands nothing; only the item that entered it by
// merge is sent back, the fast-forward prefix stays approved for the next
// fire, and step 4 refuses with no tested candidate.
func TestReleaseRedCandidateLandsNothing(t *testing.T) {
	c := newCandidateSandbox(t, true)
	idF, _ := c.item("f", "f.txt", "f\n")
	idM, _ := c.item("m", "m.txt", "m\n")
	oldMain := c.rev("origin/main")
	_, rd := c.fire()

	c.mustBlock("### Build the candidate")
	if r := c.block("### Test the candidate once"); r.code != 1 || !strings.Contains(r.err, "make test failed") {
		t.Fatalf("2d on a red candidate: exit %d\n%s%s", r.code, r.out, r.err)
	}
	sb, err := os.ReadFile(filepath.Join(rd, "sendback"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimRight(string(sb), "\n"), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], idM+"\t") ||
		!strings.Contains(lines[0], "failed make test") {
		t.Fatalf("sendback %q, want %s only", sb, idM)
	}
	c.mustBlock("### Send back")
	if st := c.status(idM); st != "in_progress" {
		t.Fatalf("%s is %s, want in_progress", idM, st)
	}
	if st := c.status(idF); st != "approved" {
		t.Fatalf("%s is %s, want approved", idF, st)
	}
	if r := c.block("## 4."); r.code == 0 || !strings.Contains(r.err, "changed after the suite ran") {
		t.Fatalf("step 4 with no tested candidate: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.git("fetch", "-q", "origin")
	if got := c.rev("origin/main"); got != oldMain {
		t.Fatalf("origin/main moved to %s", got)
	}
}

// AC4: the release rules are restated; no process text says the release ships
// without testing or never merges.
func TestReleaseRulesRestated(t *testing.T) {
	files := []string{".satelle/skills/release.md", ".satelle/skills/satelle-integration-check.md", ".satelle/skills/satelle-pr-ready-check.md", ".satelle/workflows/delivery.toml"}
	text := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile("../" + f)
		if err != nil {
			t.Fatal(err)
		}
		text[f] = string(b)
		for _, old := range []string{"without re-testing", "without testing again", "never merges or rebases main"} {
			if strings.Contains(string(b), old) {
				t.Errorf("%s still says %q", f, old)
			}
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(text[files[0]]), " "), "main only ever fast-forwards to a tree the release tested") {
		t.Error("release.md does not say main only ever fast-forwards to a tree the release tested")
	}
	// The step-4 guard excludes exactly the files step 3 commits.
	if !strings.Contains(text[files[0]], "Commit only `.version` and `CHANGELOG.md`") ||
		!strings.Contains(shBlock(t, files[0], "## 4.", "tas_x"), `-- . ':!.version' ':!CHANGELOG.md'`) {
		t.Error("step 3's commit list and step 4's guard name different files")
	}
}

// AC2, AC4, AC1 (sto_4a863578): an item a run landed but did not close
// (approved, its pull request merged, its head on origin/main inside a
// released v* tag, the landing run cancelled after its done was refused)
// reaches done through a fresh release run following release.md as written.
// The review and build keep it as contained, released in its tag, and never
// send it back; step 3 writes no second section for it; changelog-entry
// accepts its done from the earlier section; the run closes only once it is
// done. A control item with no open pull request and a merged head that never
// reached origin/main is still sent back.
func TestReleaseCarriesALandedItemToDone(t *testing.T) {
	c := newCandidateSandbox(t, false)
	for _, name := range []string{"satelle-pr-approval-check", "satelle-changelog-entry-check", "satelle-release-kept-check"} {
		c.write(".satelle/skills/"+name+".md", repoText(t, ".satelle/skills/"+name+".md"))
	}
	c.write(".satelle/workflows/flow.toml", `name = "flow"
[states.backlog]
start = true
[states.approved]
enqueue = ["release"]
[states.in_progress]
[states.done]
terminal = true
[gates.release-run]
skill = "satelle-release-run-check"
[gates.changelog-entry]
skill = "satelle-changelog-entry-check"
[[transition]]
from = ["backlog"]
to = "approved"
[[transition]]
from = ["approved"]
to = "in_progress"
[[transition]]
from = ["approved"]
to = "done"
groups = [{ mode = "sequential", gates = ["release-run", "changelog-entry"] }]
`)
	c.write(".satelle/workflows/runbook.toml", `name = "runbook"
[states.backlog]
start = true
[states.in_progress]
[states.done]
terminal = true
[states.cancelled]
terminal = true
[gates.release-kept]
skill = "satelle-release-kept-check"
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "done"
groups = [{ mode = "sequential", gates = ["release-kept"] }]
[[transition]]
from = ["*"]
to = "cancelled"
`)
	c.must("validate")
	c.git("tag", "v1.0.0", c.base)
	c.git("push", "-q", "origin", "v1.0.0")

	// 1. R1 lands A (step 3 writes [1.0.1], step 4 pushes) and does not close it.
	idA, headA := c.item("a", "a2.txt", "a\n")
	c.attach(idA, "### Fixed\n- A fixes a thing\n")
	r1, rd1 := c.fire()
	if got := itemLines(c.mustBlock("### Build the candidate")); strings.Join(got, "\n") != idA+": fast-forward head "+headA {
		t.Fatalf("R1 build lines: %q", got)
	}
	c.mustBlock("### Send back")
	c.mustBlock("### Test the candidate once")
	wt1 := filepath.Join(rd1, "candidate")
	if r := c.exec("bash", "", "-c", changelogBlock(t, wt1, "1.0.1", idA)); r.code != 0 {
		t.Fatalf("R1 step 3: exit %d\n%s%s", r.code, r.out, r.err)
	}
	writeFile(t, filepath.Join(wt1, ".version"), "satelle.version: 1.0.1\nsatelled.version: 1.0.0\n", 0o644)
	c.exec("git", "", "-C", wt1, "add", ".version", "CHANGELOG.md")
	c.exec("git", "", "-C", wt1, "commit", "-q", "-m", "chore(release): v1.0.1")
	c.mustBlock("## 4.")
	c.git("fetch", "-q", "origin")
	c.git("tag", "v1.0.1", "origin/main")
	c.git("push", "-q", "origin", "v1.0.1")
	if got := c.must("execution", "attachment", r1, "release-kept"); got != idA+" "+headA+"\n" {
		t.Fatalf("R1 release-kept %q", got)
	}

	// 2. R1's done is refused while A is approved; R1 is cancelled and
	// GitHub marks A's pull request merged.
	if r := c.run("execution", "set", r1, "--status", "done"); r.code == 0 {
		t.Fatalf("R1 reached done with %s approved:\n%s%s", idA, r.out, r.err)
	}
	if g := latestRun(c.sandbox, r1, "release-kept"); g.Result != "reject" || !strings.Contains(g.Notes, idA+" is approved, not terminal") {
		t.Fatalf("release-kept on R1: %+v", g)
	}
	c.must("execution", "set", r1, "--status", "cancelled")
	c.merged(idA)

	// 3. main moves on past A's release: v1.0.2 carries no line of A.
	wtB := c.worktree("origin/main", true)
	c.at(wtB, func() {
		c.write("b.txt", "b\n")
		cl, err := os.ReadFile(filepath.Join(wtB, "CHANGELOG.md"))
		if err != nil {
			t.Fatal(err)
		}
		c.write("CHANGELOG.md", "## [1.0.2] - 2026-03-01\n\n### Added\n- B adds a thing (sto_0000000b)\n\n"+string(cl))
		c.write(".version", "satelle.version: 1.0.2\nsatelled.version: 1.0.0\n")
		c.git("add", "-A")
		c.git("commit", "-q", "-m", "chore(release): v1.0.2")
		c.git("push", "-q", "origin", "HEAD:refs/heads/main")
	})
	c.git("fetch", "-q", "origin")
	c.git("tag", "v1.0.2", "origin/main")
	c.git("push", "-q", "origin", "v1.0.2")
	main := c.rev("origin/main")
	changelog := c.git("show", "origin/main:CHANGELOG.md")

	// 4. R2 takes A and the control C (merged head never on origin/main).
	idC, headC := c.item("c", "c.txt", "c\n")
	c.merged(idC)
	r2, rd2 := c.fire()
	review := func(id string) string {
		r := c.exec("bash", "", "-c", idBlock(t, ".satelle/skills/release.md", "### Reviews on the heads", id))
		if r.code != 0 {
			t.Fatalf("review block %s: exit %d\n%s%s", id, r.code, r.out, r.err)
		}
		return strings.TrimSpace(r.out)
	}
	if got, want := review(idA), idA+": PR #1 merged, head "+headA+" on origin/main: contained, no review to read"; got != want {
		t.Fatalf("review A: %q, want %q", got, want)
	}
	if got, want := review(idC), idC+": no open pull request (send it back)"; got != want {
		t.Fatalf("review C: %q, want %q", got, want)
	}
	if got, want := c.verdict(idA, headA, "origin/main"), idA+": keep head "+headA; got != want {
		t.Fatalf("A's changelog verdict: %q, want %q", got, want)
	}
	out := c.mustBlock("### Build the candidate")
	lines := itemLines(out)
	wantA, wantC := idA+": contained, released in v1.0.1 head "+headA, idC+": sent back: no open pull request"
	if len(lines) != 2 || !slices.Contains(lines, wantA) || !slices.Contains(lines, wantC) {
		t.Fatalf("R2 build lines:\n%s\nwant %q and %q", strings.Join(lines, "\n"), wantA, wantC)
	}
	if m := candidateRE.FindStringSubmatch(out); m == nil || m[1] != main {
		t.Fatalf("R2 candidate is not origin/main %s:\n%s", main, out)
	}
	sb, err := os.ReadFile(filepath.Join(rd2, "sendback"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sb), idA) || !strings.Contains(string(sb), idC+"\t") {
		t.Fatalf("R2 sendback %q: want %s only", sb, idC)
	}
	sent := c.mustBlock("### Send back")
	if strings.Contains(sent, idA) || !strings.Contains(sent, idC+": sent back: ") {
		t.Fatalf("send back:\n%s", sent)
	}
	if c.status(idA) != "approved" || c.status(idC) != "in_progress" {
		t.Fatalf("after send back: %s %s, %s %s", idA, c.status(idA), idC, c.status(idC))
	}
	if got := c.must("story", "attachment", idC, "release-sendback"); !strings.Contains(got, "no open pull request against main") {
		t.Fatalf("release-sendback on %s: %q", idC, got)
	}
	c.mustBlock("### Test the candidate once")
	if runs := c.suiteRuns(); len(runs) != 2 || runs[1] != main {
		t.Fatalf("make test ran on %v, want R2's once on origin/main %s", runs, main)
	}
	wt2 := filepath.Join(rd2, "candidate")
	r := c.exec("bash", "", "-c", changelogBlock(t, wt2, "1.0.2"))
	if r.code != 0 || !strings.Contains(r.out, "released before") {
		t.Fatalf("R2 step 3 with no unreleased item: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got, _ := os.ReadFile(filepath.Join(wt2, "CHANGELOG.md")); string(got) != changelog {
		t.Fatalf("R2 step 3 changed CHANGELOG.md:\n%s", got)
	}
	c.mustBlock("## 4.")
	c.git("fetch", "-q", "origin")
	if got := c.rev("origin/main"); got != main {
		t.Fatalf("R2 moved origin/main to %s", got)
	}
	if got := c.must("execution", "attachment", r2, "release-kept"); got != idA+" "+headA+"\n" {
		t.Fatalf("R2 release-kept %q", got)
	}

	// 5. A reaches done through R2.
	if r := c.run("story", "set", idA, "--status", "done"); r.code != 0 {
		t.Fatalf("done %s: exit %d\n%s%s", idA, r.code, r.out, r.err)
	}
	if g := latestRun(c.sandbox, idA, "release-run"); g.Result != "accept" || !strings.Contains(g.Notes, r2) {
		t.Fatalf("release-run on %s: %+v", idA, g)
	}
	if g := latestRun(c.sandbox, idA, "changelog-entry"); g.Result != "accept" || !strings.Contains(g.Notes, "[1.0.1]") {
		t.Fatalf("changelog-entry on %s: %+v", idA, g)
	}

	// 6. R2 closes, its kept item done.
	if r := c.run("execution", "set", r2, "--status", "done"); r.code != 0 {
		t.Fatalf("R2 done: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if g := latestRun(c.sandbox, r2, "release-kept"); g.Result != "accept" || !strings.Contains(g.Notes, idA+" done") {
		t.Fatalf("release-kept on R2: %+v", g)
	}

	// 7. On the ledger: A went approved -> done and never back to in_progress.
	if n := c.transitions(idA, "approved", "done"); n != 1 {
		t.Fatalf("%s approved -> done %d times", idA, n)
	}
	if n := c.transitions(idA, "approved", "in_progress"); n != 0 {
		t.Fatalf("%s was sent back %d times", idA, n)
	}
	if c.ancestor(headC, "origin/main") {
		t.Fatal("the control's head landed")
	}
}
