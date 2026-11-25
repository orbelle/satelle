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

type candidateSandbox struct {
	*changelogSandbox
	task      item.Item
	suiteLog  string
	suiteHome string
	tmp       string
	ids       []string
	held      string
	rd        string
	origin    string
	asRoot    bool
}

const nodeToken = "ghp_0123456789abcdefghijKLMNOPQRSTuvwxyz01"

const releaseRepo = "https://github.com/orbelle/satelle"

const releaseGH = `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_DIR/gh.log"
[ -n "${GH_TOKEN:-}" ] && printf 'token sha256 %s\n' "$(printf %s "$GH_TOKEN" | sha256sum | cut -d' ' -f1)" >>"$FAKE_DIR/gh.log"
opt() { want=$1; shift; prev=; for a in "$@"; do [ "$prev" = "$want" ] && { echo "$a"; return; }; prev=$a; done; }
case "$1 $2" in
  "pr list")
    s=$(opt --state "$@"); s=${s:-open}
    jq --arg s "$s" '[.[] | select($s == "all" or ((.state // "OPEN") | ascii_downcase) == $s)]' "$FAKE_DIR/prs.json" ;;
  "workflow run")
    ref=$(opt --ref "$@")
    echo "main at dispatch $(git -C "$ORIGIN" rev-parse main)" >>"$FAKE_DIR/gh.log"
    sha=$(git -C "$ORIGIN" rev-parse "refs/heads/$ref") || exit 1
    concl=success
    [ -f "$FAKE_DIR/fail-release" ] && concl=failure
    if [ "$concl" = success ]; then
      ver=$(git -C "$ORIGIN" show "$sha:.version" | awk '$1 == "satelle.version:" { print $2 }')
      sver=$(git -C "$ORIGIN" show "$sha:.version" | awk '$1 == "satelled.version:" { print $2 }')
      mkdir -p "$FAKE_DIR/drafts"
      for t in "v$ver" "serve-v$sver"; do
        git -C "$ORIGIN" rev-parse -q --verify "refs/tags/$t" >/dev/null && continue
        git -C "$ORIGIN" tag "$t" "$sha" || exit 1
        : >"$FAKE_DIR/drafts/$t"
      done
    fi
    printf '[{"databaseId":7,"headSha":"%s","headBranch":"%s","status":"completed","conclusion":"%s","url":"https://ci.example/7"}]\n' "$sha" "$ref" "$concl" >"$FAKE_DIR/runs.json" ;;
  "run list")
    b=$(opt --branch "$@")
    if [ -n "$b" ]; then jq --arg b "$b" '[.[] | select(.headBranch == $b)]' "$FAKE_DIR/runs.json" 2>/dev/null || echo '[]'
    else cat "$FAKE_DIR/runs.json" 2>/dev/null || echo '[]'; fi ;;
  "run watch") exit 0 ;;
  "run view") jq '.[0]' "$FAKE_DIR/runs.json" ;;
  "release view")
    git -C "$ORIGIN" rev-parse -q --verify "refs/tags/$3" >/dev/null || { echo "release not found" >&2; exit 1; }
    d=false
    [ -f "$FAKE_DIR/drafts/$3" ] && d=true
    case "$(opt --json "$@") $(opt --jq "$@")" in
      "isDraft "*) echo "$d" ;;
      *" ") printf '{"tagName":"%s","isDraft":%s,"assets":[{"name":"%s.tar.gz"}]}\n' "$3" "$d" "$3" ;;
      *) echo 1 ;;
    esac ;;
  "release edit")
    git -C "$ORIGIN" rev-parse -q --verify "refs/tags/$3" >/dev/null || { echo "release not found" >&2; exit 1; }
    [ -f "$FAKE_DIR/fail-edit" ] && { echo "fake gh: release edit failed" >&2; exit 1; }
    case " $* " in *" --draft=false "*) rm -f "$FAKE_DIR/drafts/$3" ;; esac
    case " $* " in *" --latest "*) [ -f "$FAKE_DIR/drafts/$3" ] || echo "$3" >"$FAKE_DIR/latest" ;; esac
    echo "edited $3" >>"$FAKE_DIR/gh.log" ;;
  *) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
`

func newCandidateSandbox(t *testing.T, red bool) *candidateSandbox {
	t.Helper()
	return newCandidateSandboxWith(t, red, nil)
}

func newCandidateSandboxWith(t *testing.T, red bool, base map[string]string) *candidateSandbox {
	t.Helper()
	s, origin, task := releaseSandbox(t, "make")
	s.exec("git", "", "-C", origin, "config", "receive.denyNonFastForwards", "true")
	c := &candidateSandbox{changelogSandbox: &changelogSandbox{sandbox: s}, task: task}
	c.tmp = realDir(t, t.TempDir())
	c.suiteLog = filepath.Join(realDir(t, t.TempDir()), "suite.log")
	c.suiteHome = realDir(t, t.TempDir())
	c.origin = origin
	s.env["TMPDIR"] = c.tmp

	mk := "test:\n\tgit rev-parse HEAD >>'" + c.suiteLog + "'\n"
	if red {
		mk += "\texit 1\n"
	}
	s.write("Makefile", mk)
	s.write("a.txt", "base\n")
	s.write(".version", "satelle.version: 1.0.0\nsatelled.version: 1.0.0\n")
	s.write("docs/releases/v1.0.0.md", baseChangelog)
	writeFile(t, filepath.Join(s.dir, "scripts", "changelog-items.sh"), repoText(t, "scripts/changelog-items.sh"), 0o755)
	writeFile(t, filepath.Join(s.dir, "scripts", "drift-check.sh"), repoText(t, "scripts/drift-check.sh"), 0o755)
	writeFile(t, filepath.Join(s.dir, "scripts", "check-serve-version.sh"), repoText(t, "scripts/check-serve-version.sh"), 0o755)
	add := []string{"Makefile", "a.txt", ".version", "docs/releases", "scripts/changelog-items.sh", "scripts/drift-check.sh",
		"scripts/release-binary.sh", "scripts/check-serve-version.sh", ".satelle"}
	for p, body := range base {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		writeFile(t, filepath.Join(s.dir, p), body, mode)
		add = append(add, p)
	}
	s.git(append([]string{"add"}, add...)...)
	s.git("commit", "-q", "-m", "base")
	s.git("push", "-q", "origin", "HEAD:refs/heads/main")
	s.env["GIT_CONFIG_COUNT"] = "4"
	s.env["GIT_CONFIG_KEY_3"] = "url." + origin + ".insteadOf"
	s.env["GIT_CONFIG_VALUE_3"] = releaseRepo
	s.env["ORIGIN"] = origin
	s.git("remote", "set-url", "origin", releaseRepo)
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
	writeFile(t, filepath.Join(c.fake, "gh"), releaseGH, 0o755)
	writeFile(t, filepath.Join(c.fake, "prs.json"), "[]", 0o644)
	writeFile(t, filepath.Join(c.fake, "go"), `#!/bin/sh
out=; prev=
for a in "$@"; do [ "$prev" = -o ] && out=$a; prev=$a; done
[ "$1" = build ] && [ -n "$out" ] || { echo "fake go: unexpected $*" >&2; exit 1; }
cp "$SATELLE_BIN" "$out"
`, 0o755)
	s.env["SATELLE_BIN"] = satelleBin
	s.path = append(s.path, c.fake)
	s.env["FAKE_DIR"] = c.fake
	return c
}

func (c *candidateSandbox) item(name, path, body string) (string, string) {
	c.t.Helper()
	head := c.branch(name, c.base, [2]string{path, body})
	id := c.create("story", "--title", "change "+name).ID
	c.attach(id, "### Added\n- "+name+" lands\n")
	c.must("story", "set", id, "--status", "approved")
	c.pr(id, head)
	c.ids = append(c.ids, id)
	return id, head
}

func (c *candidateSandbox) nodeRoot() string { return filepath.Join(c.rd, "node-root") }

func (c *candidateSandbox) node(step string, args ...string) result {
	c.t.Helper()
	root := c.nodeRoot()
	if step == "input" {
		if err := os.RemoveAll(root); err != nil {
			c.t.Fatal(err)
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			c.t.Fatal(err)
		}
		if r := c.exec("cp", "", "-R", filepath.Join(c.rd, "node-in"), filepath.Join(root, "in")); r.code != 0 {
			c.t.Fatalf("seeding the node's input: %s%s", r.out, r.err)
		}
	}
	path := ""
	for _, kv := range c.environ() {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	env := map[string]string{"SATELLE_RELEASE_ROOT": root, "SATELLE_RELEASE_TEST_SAME_UID": "1", "GH_TOKEN": nodeToken,
		"SATELLE_RELEASE_SUITE_PATH": path, "SATELLE_RELEASE_SUITE_HOME": c.suiteHome, "SATELLE_RELEASE_POLL": "0"}
	if c.asRoot {
		delete(env, "SATELLE_RELEASE_TEST_SAME_UID")
	}
	if tok, ok := c.env["NODE_GH_TOKEN"]; ok {
		env["GH_TOKEN"] = tok
	}
	for k, v := range env {
		c.env[k] = v
	}
	defer func() {
		for k := range env {
			delete(c.env, k)
		}
	}()
	script, err := filepath.Abs("../" + releaseNodeScript)
	if err != nil {
		c.t.Fatal(err)
	}
	return c.exec("bash", "", append([]string{script, step}, args...)...)
}

func (c *candidateSandbox) nodeSteps(steps ...string) result {
	c.t.Helper()
	for _, st := range steps {
		if r := c.node(st); r.code != 0 {
			return r
		}
	}
	return result{}
}

func (c *candidateSandbox) mustNode(steps ...string) {
	c.t.Helper()
	if r := c.nodeSteps(steps...); r.code != 0 {
		c.t.Fatalf("node %v: exit %d\n%s%s\n%s", steps, r.code, r.out, r.err, c.nodeFile("out/log"))
	}
}

func (c *candidateSandbox) nodeFile(rel string) string {
	c.t.Helper()
	b, err := os.ReadFile(filepath.Join(c.nodeRoot(), rel))
	if err != nil && !os.IsNotExist(err) {
		c.t.Fatal(err)
	}
	return string(b)
}

func (c *candidateSandbox) nodeTest() result {
	c.t.Helper()
	if r := c.block(nodeInputHeading); r.code != 0 {
		return r
	}
	return c.nodeSteps("input", "clone", "fetch", "test")
}

func (c *candidateSandbox) mustNodeTest() {
	c.t.Helper()
	if r := c.nodeTest(); r.code != 0 {
		c.t.Fatalf("the node's test: exit %d\n%s%s\n%s", r.code, r.out, r.err, c.nodeFile("out/log"))
	}
}

func (c *candidateSandbox) attachDecision(run string) {
	c.t.Helper()
	c.must("execution", "attach", run, "--name", "release-binary", "--type", "text/plain", "--file", filepath.Join(c.nodeRoot(), "out", "release-binary"))
}

func (c *candidateSandbox) land(run string) result {
	c.t.Helper()
	r := c.nodeSteps("bump", "guard", "publish", "push", "release")
	if r.code == 0 {
		c.attachDecision(run)
	}
	return r
}

func (c *candidateSandbox) mustLand(run string) {
	c.t.Helper()
	if r := c.land(run); r.code != 0 {
		c.t.Fatalf("the node's landing: exit %d\n%s%s\n%s", r.code, r.out, r.err, c.nodeFile("out/log"))
	}
}

func (c *candidateSandbox) work() string { return filepath.Join(c.nodeRoot(), "work") }

func (c *candidateSandbox) block(heading string) result {
	c.t.Helper()
	b := shBlock(c.t, releaseSkill, heading, c.task.ID)
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

func (c *candidateSandbox) decide() string {
	c.t.Helper()
	c.mustNode("decide")
	return c.nodeFile("out/release-binary")
}

func (c *candidateSandbox) driftGate() string {
	c.t.Helper()
	return c.mustBlock("### Drift gate on the candidate")
}

func (c *candidateSandbox) fire() (string, string) {
	c.t.Helper()
	c.mustBlock("## 0.")
	var runs []item.Item
	c.mustJSON(&runs, "execution", "list", "--parent", c.task.ID, "--status", "in_progress")
	if len(runs) != 1 || runs[0].Status != "in_progress" {
		c.t.Fatalf("runs %+v", runs)
	}
	c.rd = filepath.Join(c.tmp, "satelle-release-"+runs[0].ID)
	return runs[0].ID, c.rd
}

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
	c.driftGate()
	c.mustNodeTest()
	c.decide()
	if runs := c.suiteRuns(); len(runs) != 1 || runs[0] != cand {
		t.Fatalf("make test ran on %v, want once on the candidate %s", runs, cand)
	}

	work := c.work()
	writeFile(t, filepath.Join(work, "late.txt"), "late\n", 0o644)
	c.exec("git", "", "-C", work, "add", "late.txt")
	c.exec("git", "", "-C", work, "commit", "-q", "-m", "late change")
	if r := c.node("guard"); r.code == 0 || !strings.Contains(r.out, "failed step guard: the candidate changed after the suite ran") {
		t.Fatalf("guard on a changed candidate: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.git("fetch", "-q", "origin")
	if got := c.rev("origin/main"); got != oldMain {
		t.Fatalf("origin/main moved to %s on a refused push", got)
	}
	c.exec("git", "", "-C", work, "reset", "-q", "--hard", cand)

	c.mustLand(run)
	c.git("fetch", "-q", "origin")
	if got := c.rev("origin/main"); got != cand {
		t.Fatalf("origin/main %s, want the tested candidate %s (its version is unpublished, so nothing is bumped)", got, cand)
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
	if r := c.exec("git", "", "ls-remote", "origin", "refs/heads/release/"+run); strings.TrimSpace(r.out) != "" {
		t.Fatalf("release/%s survives the push: %s", run, r.out)
	}
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

func TestChangelogVerdictReadsTheHeadsOwnCommits(t *testing.T) {
	c := newChangelogSandbox(t)
	id := c.story("### Added\n- A adds\n")
	head := c.branch("a", c.base, [2]string{"a.txt", "a\n"})
	wt := c.worktree("origin/main", true)
	c.at(wt, func() { c.write("docs/releases/v1.0.1.md", "## [1.0.1] - 2026-02-01\n\n### Added\n- Z adds\n") })
	c.bump(wt, "1.0.1")
	c.assertFixtures([]string{head})
	main := c.rev("origin/main")
	if c.exec("git", "", "diff", "--quiet", main, head, "--", "docs/releases").code == 0 {
		t.Fatal("setup: main's docs/releases does not differ from the head's")
	}
	if got, want := c.verdict(id, head, main), id+": keep head "+head; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

func TestReleaseConflictSendsBackOne(t *testing.T) {
	c := newCandidateSandbox(t, false)
	idX, headX := c.item("x", "a.txt", "x\n")
	idY, headY := c.item("y", "a.txt", "y\n")
	idZ, headZ := c.item("z", "c.txt", "z\n")
	idH, headH := c.item("h", "h.txt", "h\n")
	heldWhy := "head " + headH + " edits CHANGELOG.md; restore it"
	c.held = idH + ": send back: " + heldWhy + "\n" + idX + ": keep head " + headX
	run, rd := c.fire()

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
	c.driftGate()
	c.mustNodeTest()
	c.decide()
	if runs := c.suiteRuns(); len(runs) != 1 {
		t.Fatalf("make test ran %d times", len(runs))
	}
	c.mustLand(run)
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

func TestReleaseRedCandidateLandsNothing(t *testing.T) {
	c := newCandidateSandbox(t, true)
	idF, _ := c.item("f", "f.txt", "f\n")
	idM, _ := c.item("m", "m.txt", "m\n")
	oldMain := c.rev("origin/main")
	_, rd := c.fire()

	c.mustBlock("### Build the candidate")
	c.driftGate()
	if r := c.nodeTest(); r.code != 1 || !strings.Contains(r.out, "failed step test: suite-exit ") {
		t.Fatalf("the node's test on a red candidate: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := c.nodeFile("back/result"); !strings.Contains(got, "failed step test: suite-exit ") || !strings.Contains(got, "secret-scan clean") {
		t.Fatalf("back/result of a red suite:\n%s", got)
	}
	if sb, _ := os.ReadFile(filepath.Join(rd, "sendback")); len(sb) != 0 {
		t.Fatalf("a red suite sent back within the run: %q", sb)
	}
	for _, id := range []string{idF, idM} {
		if st := c.status(id); st != "approved" {
			t.Fatalf("%s is %s, want approved", id, st)
		}
	}
	if r := c.node("decide"); r.code != 1 || !strings.Contains(r.out, "failed step decide: the suite has not passed") {
		t.Fatalf("decide on a red candidate: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := c.node("guard"); r.code == 0 || !strings.Contains(r.out, "changed after the suite ran") {
		t.Fatalf("guard with no tested candidate: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.git("fetch", "-q", "origin")
	if got := c.rev("origin/main"); got != oldMain {
		t.Fatalf("origin/main moved to %s", got)
	}
}

func TestReleaseRulesRestated(t *testing.T) {
	files := []string{releaseSkill, ".satelle/skills/satelle-integration-check.md", ".satelle/skills/satelle-pr-ready-check/SKILL.md", ".satelle/workflows/delivery.toml"}
	text := map[string]string{}
	for _, f := range files {
		b := []byte(skillDoc(t, f))
		text[f] = string(b)
		for _, old := range []string{"without re-testing", "without testing again", "never merges or rebases main"} {
			if strings.Contains(string(b), old) {
				t.Errorf("%s still says %q", f, old)
			}
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(text[files[0]]), " "), "main only ever fast-forwards to a tree the release tested") {
		t.Error("release/SKILL.md does not say main only ever fast-forwards to a tree the release tested")
	}
	if !strings.Contains(text[files[0]], "Commit only `.version` and `docs/releases/v<ver>.md`") ||
		!strings.Contains(repoText(t, releaseNodeScript), `-- . ':!.version' ":!docs/releases/v$ver.md"`) {
		t.Error("step 3's commit list and step 4's guard name different files")
	}
}

func TestReleaseCarriesALandedItemToDone(t *testing.T) {
	c := newKeptSandbox(t, nil)
	c.git("tag", "v1.0.0", c.base)
	c.git("push", "-q", "origin", "v1.0.0")

	idA, headA := c.item("a", "a2.txt", "a\n")
	c.attach(idA, "### Fixed\n- A fixes a thing\n")
	r1, rd1 := c.fire()
	if got := itemLines(c.mustBlock("### Build the candidate")); strings.Join(got, "\n") != idA+": fast-forward head "+headA {
		t.Fatalf("R1 build lines: %q", got)
	}
	c.mustBlock("### Send back")
	c.driftGate()
	c.mustNodeTest()
	c.decide()
	c.mustLand(r1)
	c.git("fetch", "-q", "origin", "--tags")
	if c.rev("v1.0.1^{commit}") != c.rev("origin/main") || c.git("show", "origin/main:.version") != "satelle.version: 1.0.1\nsatelled.version: 1.0.0\n" {
		t.Fatalf("R1 did not publish v1.0.1 at main:\n%s", c.git("show", "origin/main:.version"))
	}
	_ = rd1
	if got := c.must("execution", "attachment", r1, "release-kept"); got != idA+" "+headA+"\n" {
		t.Fatalf("R1 release-kept %q", got)
	}

	if r := c.run("execution", "set", r1, "--status", "done"); r.code == 0 {
		t.Fatalf("R1 reached done with %s approved:\n%s%s", idA, r.out, r.err)
	}
	if g := latestRun(c.sandbox, r1, "release-kept"); g.Result != "reject" || !strings.Contains(g.Notes, idA+" is approved, not terminal") {
		t.Fatalf("release-kept on R1: %+v", g)
	}
	c.must("execution", "set", r1, "--status", "cancelled")
	c.merged(idA)

	wtB := c.worktree("origin/main", true)
	c.at(wtB, func() {
		c.write("b.txt", "b\n")
		c.write("docs/releases/v1.0.2.md", "## [1.0.2] - 2026-03-01\n\n### Added\n- B adds a thing (sto_0000000b)\n")
		c.write(".version", "satelle.version: 1.0.2\nsatelled.version: 1.0.1\n")
		c.git("add", "-A")
		c.git("commit", "-q", "-m", "chore(release): v1.0.2")
		c.git("push", "-q", "origin", "HEAD:refs/heads/main")
	})
	c.git("fetch", "-q", "origin")
	c.git("tag", "v1.0.2", "origin/main")
	c.git("push", "-q", "origin", "v1.0.2")
	main := c.rev("origin/main")
	changelog := c.git("show", "origin/main:docs/releases/v1.0.0.md")

	idC, headC := c.item("c", "c.txt", "c\n")
	c.merged(idC)
	r2, rd2 := c.fire()
	review := func(id string) string {
		r := c.exec("bash", "", "-c", idBlock(t, releaseSkill, "### Reviews on the heads", id))
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
	c.driftGate()
	c.mustNodeTest()
	c.decide()
	if runs := c.suiteRuns(); len(runs) != 2 || runs[1] != main {
		t.Fatalf("make test ran on %v, want R2's once on origin/main %s", runs, main)
	}
	if ids := c.nodeFile("in/notes-ids"); strings.TrimSpace(ids) != "" {
		t.Fatalf("R2 notes ids %q, want none: %s was released in v1.0.1", ids, idA)
	}
	c.mustLand(r2)
	if log := c.nodeFile("out/log"); !strings.Contains(log, "released before") {
		t.Fatalf("R2 bump with no unreleased item:\n%s", log)
	}
	if got := c.git("show", "origin/main:docs/releases/v1.0.0.md"); got != changelog {
		t.Fatalf("R2 changed docs/releases/v1.0.0.md:\n%s", got)
	}
	_ = rd2
	c.git("fetch", "-q", "origin")
	if got := c.rev("origin/main"); got != main {
		t.Fatalf("R2 moved origin/main to %s", got)
	}
	if got := c.must("execution", "attachment", r2, "release-kept"); got != idA+" "+headA+"\n" {
		t.Fatalf("R2 release-kept %q", got)
	}

	if r := c.run("story", "set", idA, "--status", "done"); r.code != 0 {
		t.Fatalf("done %s: exit %d\n%s%s", idA, r.code, r.out, r.err)
	}
	if g := latestRun(c.sandbox, idA, "release-run"); g.Result != "accept" || !strings.Contains(g.Notes, r2) {
		t.Fatalf("release-run on %s: %+v", idA, g)
	}
	if g := latestRun(c.sandbox, idA, "changelog-entry"); g.Result != "accept" || !strings.Contains(g.Notes, "[1.0.1]") {
		t.Fatalf("changelog-entry on %s: %+v", idA, g)
	}

	if r := c.run("execution", "set", r2, "--status", "done"); r.code != 0 {
		t.Fatalf("R2 done: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if g := latestRun(c.sandbox, r2, "release-kept"); g.Result != "accept" || !strings.Contains(g.Notes, idA+" done") {
		t.Fatalf("release-kept on R2: %+v", g)
	}

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

func newKeptSandbox(t *testing.T, base map[string]string) *candidateSandbox {
	t.Helper()
	c := newCandidateSandboxWith(t, false, base)
	for _, name := range []string{"satelle-pr-approval-check", "satelle-changelog-entry-check", "satelle-release-kept-check"} {
		rel := repoSkillRel(t, name)
		c.write(rel, rebindTask(t, rel, c.task.ID))
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
	return c
}

func TestReleaseBumpsPastTheNewestOfSeveralTags(t *testing.T) {
	c := newKeptSandbox(t, map[string]string{".version": "satelle.version: 1.0.1\nsatelled.version: 1.0.0\n"})
	c.git("tag", "v1.0.0", c.base)
	c.git("tag", "v1.0.1", c.base)
	c.git("push", "-q", "origin", "v1.0.0", "v1.0.1")

	id, _ := c.item("a", "a2.txt", "a\n")
	c.attach(id, "### Fixed\n- A fixes a thing\n")
	run, _ := c.fire()
	c.mustBlock("### Build the candidate")
	c.mustBlock("### Send back")
	c.driftGate()
	c.mustNodeTest()
	c.decide()
	c.mustLand(run)
	c.git("fetch", "-q", "origin", "--tags")
	if got := c.git("show", "origin/main:.version"); !strings.HasPrefix(got, "satelle.version: 1.0.2\n") {
		t.Fatalf("origin/main's .version after a run over v1.0.0 and v1.0.1:\n%s", got)
	}
	if c.rev("v1.0.2^{commit}") != c.rev("origin/main") {
		t.Fatal("v1.0.2 is not at origin/main")
	}
}

func TestReleaseDriftGateRefusesThenFixes(t *testing.T) {
	c := newCandidateSandbox(t, false)
	c.item("x", ".satelle/skills/x.md", "---\nname: x\ntype: skill\ndescription: x\n---\n\nRun `satelle nosuchcmd <id>`.\n")
	oldMain := c.rev("origin/main")
	run, rd := c.fire()
	out := c.mustBlock("### Build the candidate")
	wt := candidateRE.FindStringSubmatch(out)[2]

	r := c.block("### Drift gate on the candidate")
	if r.code != 1 || !strings.Contains(r.out, ".satelle/skills/x.md:7: command: satelle nosuchcmd — ") || !strings.Contains(r.err, "names what its binary lacks") {
		t.Fatalf("drift gate on a drifted candidate: exit %d, want 1 naming the line\n%s%s", r.code, r.out, r.err)
	}
	if _, err := os.Stat(filepath.Join(rd, "drift-checked")); !os.IsNotExist(err) {
		t.Fatalf("drift-checked written for a refused candidate: %v", err)
	}
	if r := c.nodeTest(); r.code != 1 || !strings.Contains(r.err, "the drift gate has not passed") {
		t.Fatalf("the node's input on an unchecked candidate: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if runs := c.suiteRuns(); len(runs) != 0 {
		t.Fatalf("make test ran on a candidate the drift gate refused: %v", runs)
	}
	if r := c.exec("git", "", "ls-remote", "origin", "refs/heads/release/"+run); strings.TrimSpace(r.out) != "" {
		t.Fatalf("a refused candidate was pushed to release/%s", run)
	}
	c.git("fetch", "-q", "origin")
	if got := c.rev("origin/main"); got != oldMain {
		t.Fatalf("origin/main moved to %s", got)
	}

	writeFile(t, filepath.Join(wt, ".satelle/skills/x.md"), "---\nname: x\ntype: skill\ndescription: x\n---\n\nRun `satelle story list`.\n", 0o644)
	c.exec("git", "", "-C", wt, "add", ".satelle/skills/x.md")
	c.exec("git", "", "-C", wt, "commit", "-q", "-m", "chore(release): substrate drift fix ("+run+")")
	fixCommit := strings.TrimSpace(c.exec("git", "", "-C", wt, "rev-parse", "HEAD").out)
	c.driftGate()
	if b, _ := os.ReadFile(filepath.Join(rd, "drift-checked")); strings.TrimSpace(string(b)) != fixCommit {
		t.Fatalf("drift-checked %q, want the fixed candidate %s", b, fixCommit)
	}

	writeFile(t, filepath.Join(wt, "late.txt"), "late\n", 0o644)
	c.exec("git", "", "-C", wt, "add", "late.txt")
	c.exec("git", "", "-C", wt, "commit", "-q", "-m", "late")
	if r := c.nodeTest(); r.code != 1 || !strings.Contains(r.err, "the drift gate has not passed") {
		t.Fatalf("the node's input after a later commit: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.exec("git", "", "-C", wt, "reset", "-q", "--hard", fixCommit)
	c.driftGate()
	c.mustNodeTest()
	c.decide()
	if runs := c.suiteRuns(); len(runs) != 1 || runs[0] != fixCommit {
		t.Fatalf("make test ran on %v, want once on %s", runs, fixCommit)
	}
	c.mustLand(run)
	c.git("fetch", "-q", "origin")
	if !c.ancestor(fixCommit, "origin/main") {
		t.Fatal("origin/main does not carry the drift-fix commit")
	}
	got := c.must("execution", "attachment", run, "release-drift")
	if !strings.HasPrefix(got, "drift-check exit 0 on "+fixCommit+"\nfixed in this run:\n") || !strings.Contains(got, ".satelle/skills/x.md:7: command: satelle nosuchcmd — ") {
		t.Fatalf("release-drift:\n%s", got)
	}
}

const nodeInputHeading = "\n### The node's input\n"
