//go:build integration && unix

package tests

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/workflow"
)

// changelogSandbox is a repo with a bare origin whose first main commit
// (base) holds .version (satelle.version 1.0.0), a CHANGELOG.md with a 1.0.0
// section and this repo's scripts/changelog-items.sh, so every head cut from
// it, every worktree of it and every checkout of the pushed main carries the
// script exactly where the skills read it. A fake gh serves the open pull
// requests from prs.json.
type changelogSandbox struct {
	*sandbox
	base string
	fake string
	prs  []map[string]any
}

const baseChangelog = `## [1.0.0] - 2026-01-01

### Added
- The first release.
`

func newChangelogSandbox(t *testing.T) *changelogSandbox {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "git", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the changelog blocks and is not on PATH", tool)
		}
	}
	script, err := os.ReadFile("../scripts/changelog-items.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := newSandbox(t)
	s.env["SATELLE_SESSION"] = "changelog-it"
	origin := realDir(t, t.TempDir())
	s.exec("git", "", "init", "-q", "--bare", origin)
	s.git("remote", "add", "origin", origin)
	s.must("init")
	s.write(".version", "satelle.version: 1.0.0\nsatelled.version: 1.0.0\n")
	s.write("CHANGELOG.md", baseChangelog)
	writeFile(t, filepath.Join(s.dir, "scripts", "changelog-items.sh"), string(script), 0o755)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "base")
	s.git("push", "-q", "origin", "HEAD:refs/heads/main")
	s.git("fetch", "-q", "origin")
	c := &changelogSandbox{sandbox: s, base: s.rev("HEAD")}
	c.fake = realDir(t, t.TempDir())
	writeFile(t, filepath.Join(c.fake, "gh"), "#!/bin/sh\ncase \"$1\" in\n  pr) cat \"$FAKE_DIR/prs.json\" ;;\n  *) echo \"fake gh: unexpected $*\" >&2; exit 1 ;;\nesac\n", 0o755)
	writeFile(t, filepath.Join(c.fake, "prs.json"), "[]", 0o644)
	s.path = append(s.path, c.fake)
	s.env["FAKE_DIR"] = c.fake
	return c
}

func (s *sandbox) rev(ref string) string {
	s.t.Helper()
	return strings.TrimSpace(s.git("rev-parse", ref))
}

// at runs fn with every command of the sandbox in dir.
func (s *sandbox) at(dir string, fn func()) {
	old := s.dir
	s.dir = dir
	defer func() { s.dir = old }()
	fn()
}

// worktree adds a linked worktree at ref (a branch name checks it out,
// anything else detaches) and returns its path. A linked worktree shares the
// repo's items, so satelle answers in it as in the sandbox.
func (c *changelogSandbox) worktree(ref string, detach bool) string {
	c.t.Helper()
	dir := filepath.Join(realDir(c.t, c.t.TempDir()), "wt")
	args := []string{"worktree", "add", "-q"}
	if detach {
		args = append(args, "--detach")
	}
	c.git(append(args, dir, ref)...)
	return dir
}

// branch cuts branch from base and commits each of files (path -> body) in
// its own commit, then pushes it and returns its head.
func (c *changelogSandbox) branch(name, from string, files ...[2]string) string {
	c.t.Helper()
	dir := c.worktree(from, true)
	c.at(dir, func() {
		c.git("checkout", "-q", "-b", name)
		for _, f := range files {
			if f[1] == "" {
				c.git("rm", "-q", f[0])
			} else {
				writeFile(c.t, filepath.Join(dir, f[0]), f[1], 0o644)
				c.git("add", "-A")
			}
			c.git("commit", "-q", "-m", "change "+f[0])
		}
		c.git("push", "-q", "origin", name)
	})
	c.git("worktree", "remove", "--force", dir)
	return c.rev(name)
}

// story creates a story and, unless changelog is empty, attaches it as its
// changelog attachment.
func (c *changelogSandbox) story(changelog string) string {
	c.t.Helper()
	id := c.create("story", "--title", "change").ID
	if changelog != "" {
		c.attach(id, changelog)
	}
	return id
}

func (c *changelogSandbox) attach(id, body string) {
	c.t.Helper()
	if r := c.exec(satelleBin, body, "story", "attach", id, "--name", "changelog", "--type", "text/markdown", "--file", "-"); r.code != 0 {
		c.t.Fatalf("attach %s: %s%s", id, r.out, r.err)
	}
}

// pr makes the fake gh list an open pull request for id on head.
func (c *changelogSandbox) pr(id, head string) {
	c.t.Helper()
	c.prs = append(c.prs, map[string]any{"number": len(c.prs) + 1, "headRefOid": head, "title": "change (" + id + ")", "isDraft": false})
	b, _ := json.Marshal(c.prs)
	writeFile(c.t, filepath.Join(c.fake, "prs.json"), string(b), 0o644)
}

// assertFixtures fails setup unless every head, and each dir, carries the
// script, so a missing fixture never passes as the rule under test.
func (c *changelogSandbox) assertFixtures(heads []string, dirs ...string) {
	c.t.Helper()
	for _, h := range heads {
		if r := c.exec("git", "", "cat-file", "-e", h+":scripts/changelog-items.sh"); r.code != 0 {
			c.t.Fatalf("setup: head %s carries no scripts/changelog-items.sh", h)
		}
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, "scripts", "changelog-items.sh")); err != nil {
			c.t.Fatalf("setup: %s carries no scripts/changelog-items.sh: %v", d, err)
		}
	}
}

// keptBlock is release step 2a's per-item block for id at head over tip.
func keptBlock(t *testing.T, id, head, tip string) string {
	t.Helper()
	return "head=" + head + "\ntip=" + tip + "\n" + idBlock(t, ".satelle/skills/release.md", "### Each kept item's changelog", id)
}

// verdict runs the per-item block and asserts it exits 0 with exactly one
// line, which it returns.
func (c *changelogSandbox) verdict(id, head, tip string) string {
	c.t.Helper()
	r := c.exec("bash", "", "-c", keptBlock(c.t, id, head, tip))
	if r.code != 0 || strings.Count(r.out, "\n") != 1 || !strings.HasSuffix(r.out, "\n") {
		c.t.Fatalf("kept-item block for %s: exit %d, want 0 and one line\n%s%s", id, r.code, r.out, r.err)
	}
	return strings.TrimSuffix(r.out, "\n")
}

// changelogBlock is release step 3's block with its ids= and ver= lines
// rebound and wt set.
func changelogBlock(t *testing.T, wt, ver string, ids ...string) string {
	t.Helper()
	b, err := os.ReadFile("../.satelle/skills/release.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "### Changelog from the items")
	if i < 0 {
		t.Fatal("release.md has no ### Changelog from the items")
	}
	_, rest, ok := strings.Cut(doc[i:], "```sh\n")
	if !ok {
		t.Fatal("release.md has no sh block after ### Changelog from the items")
	}
	block, _, _ := strings.Cut(rest, "```")
	for name, val := range map[string]string{"ids": `"` + strings.Join(ids, " ") + `"`, "ver": ver} {
		re := regexp.MustCompile(`(?m)^` + name + `=.*$`)
		if len(re.FindAllString(block, -1)) != 1 {
			t.Fatalf("the step-3 block has no single %s= line:\n%s", name, block)
		}
		block = re.ReplaceAllLiteralString(block, name+"="+val)
	}
	return "wt=" + wt + "\n" + bindSession(block)
}

// bump writes .version's satelle.version in wt, commits only .version and
// CHANGELOG.md and pushes main.
func (c *changelogSandbox) bump(wt, ver string) {
	c.t.Helper()
	c.at(wt, func() {
		c.write(".version", "satelle.version: "+ver+"\nsatelled.version: 1.0.0\n")
		c.git("add", ".version", "CHANGELOG.md")
		c.git("commit", "-q", "-m", "chore(release): v"+ver)
		if got := strings.Fields(c.git("diff", "--name-only", "HEAD~1", "HEAD")); !slices.Equal(got, []string{".version", "CHANGELOG.md"}) {
			c.t.Fatalf("the bump commit touches %v", got)
		}
		c.git("push", "-q", "origin", "HEAD:refs/heads/main")
	})
	c.git("fetch", "-q", "origin")
}

// The release run's verdict per item, its version section written from the
// items' attachments, and changelog-entry's check of every landed item.
func TestReleaseWritesChangelogFromItems(t *testing.T) {
	c := newChangelogSandbox(t)
	idA := c.story("### Fixed\n- A fixes a thing\n### Added\n- A adds a thing\n")
	idB := c.story("### Changed\n- B changes a thing\n")
	headA := c.branch("a", c.base, [2]string{"a.txt", "a\n"})
	headB := c.branch("b", headA, [2]string{"b.txt", "b\n"})
	idN := c.story("")
	idE := c.story("### Fixed\n- E fixes\n")
	headE := c.branch("e", c.base, [2]string{"CHANGELOG.md", "## [1.0.0] - 2026-01-01\n\n### Added\n- E's line\n- The first release.\n"})
	idL := c.story("### Fixed\n- L fixes\n")
	headL := c.branch("l", c.base, [2]string{"scripts/changelog-items.sh", ""})
	main := c.base
	wt := c.worktree(headB, true)
	c.assertFixtures([]string{headA, headB, headE}, wt)

	if got, want := c.verdict(idA, headA, main), idA+": keep head "+headA; got != want {
		t.Fatalf("A: %q, want %q", got, want)
	}
	if got, want := c.verdict(idB, headB, headA), idB+": keep head "+headB; got != want {
		t.Fatalf("B: %q, want %q", got, want)
	}
	if got := c.verdict(idN, headA, main); !strings.HasPrefix(got, idN+": send back: no valid changelog attachment on head "+headA) || !strings.Contains(got, "--name changelog") {
		t.Fatalf("no attachment: %q", got)
	}
	if got := c.verdict(idE, headE, main); !strings.HasPrefix(got, idE+": send back: head "+headE+" edits CHANGELOG.md") {
		t.Fatalf("edits CHANGELOG.md: %q", got)
	}
	if got := c.verdict(idL, headL, main); !strings.HasPrefix(got, idL+": send back: no valid changelog attachment on head "+headL) {
		t.Fatalf("legacy head: %q", got)
	}
	// With no satelle on PATH the block cannot judge: the only exit 1.
	bin := realDir(t, t.TempDir())
	for _, tool := range []string{"git", "bash"} {
		p, _ := exec.LookPath(tool)
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	bash, _ := exec.LookPath("bash")
	cmd := exec.Command(bash, "-c", keptBlock(t, idA, headA, main))
	cmd.Dir = c.dir
	for _, kv := range c.environ() {
		if !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+bin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(errb.String(), "satelle") {
		t.Fatalf("no satelle on PATH: %v, stdout %q stderr %q", err, out.String(), errb.String())
	}

	// Step 3 writes the version section on top, from A then B.
	block := changelogBlock(t, wt, "1.0.1", idA, idB)
	if r := c.exec("bash", "", "-c", block); r.code != 0 {
		t.Fatalf("step 3: exit %d\n%s%s", r.code, r.out, r.err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	want := "## [1.0.1] - " + today + "\n\n### Added\n- A adds a thing (" + idA + ")\n\n### Changed\n- B changes a thing (" + idB +
		")\n\n### Fixed\n- A fixes a thing (" + idA + ")\n\n" + baseChangelog
	got, _ := os.ReadFile(filepath.Join(wt, "CHANGELOG.md"))
	if string(got) != want {
		t.Fatalf("CHANGELOG.md:\n%s\nwant\n%s", got, want)
	}
	if r := c.exec("bash", "", "-c", block); r.code != 0 {
		t.Fatalf("step 3 again: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if again, _ := os.ReadFile(filepath.Join(wt, "CHANGELOG.md")); string(again) != want || strings.Contains(string(again), "Unreleased") {
		t.Fatalf("a second step 3 changed CHANGELOG.md:\n%s", again)
	}
	c.bump(wt, "1.0.1")

	// changelog-entry, from a checkout of the pushed main.
	check := shBlock0(t, ".satelle/skills/satelle-changelog-entry-check.md")
	gate := c.worktree("origin/main", true)
	c.assertFixtures(nil, gate)
	entry := func(id string) result {
		c.env["SATELLE_ITEM"] = id
		defer delete(c.env, "SATELLE_ITEM")
		var r result
		c.at(gate, func() { r = c.exec("bash", "", "-c", check) })
		return r
	}
	for _, id := range []string{idA, idB} {
		if r := entry(id); r.code != 0 {
			t.Fatalf("changelog-entry %s: exit %d\n%s%s", id, r.code, r.out, r.err)
		}
	}
	idX := c.story("### Added\n- X never landed\n")
	if r := entry(idX); r.code != 1 || !strings.Contains(r.out, `"- X never landed (`+idX+`)" under ### Added`) {
		t.Fatalf("changelog-entry for an absent entry: exit %d\n%s%s", r.code, r.out, r.err)
	}

	// AC3 (sto_4a863578): a later release writes [1.0.2] from D only. An item
	// an earlier release published is accepted from that section; D from the
	// first; an absent entry is still refused with the current version's
	// reason.
	idD := c.story("### Added\n- D adds a thing\n")
	wtD := c.worktree("origin/main", true)
	if r := c.exec("bash", "", "-c", changelogBlock(t, wtD, "1.0.2", idD)); r.code != 0 {
		t.Fatalf("step 3 for 1.0.2: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.bump(wtD, "1.0.2")
	c.at(gate, func() { c.git("checkout", "-q", "--detach", "origin/main") })
	if r := entry(idA); r.code != 0 || !strings.Contains(r.out, "in the [1.0.1] section, the release that published it") {
		t.Fatalf("changelog-entry for an item [1.0.1] published: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := entry(idD); r.code != 0 || !strings.Contains(r.out, "opens with the [1.0.2] section") {
		t.Fatalf("changelog-entry for an item of the first section: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := entry(idX); r.code != 1 || !strings.Contains(r.out, `"- X never landed (`+idX+`)" under ### Added`) || !strings.Contains(r.out, "[1.0.2]") {
		t.Fatalf("changelog-entry for an absent entry after 1.0.2: exit %d\n%s%s", r.code, r.out, r.err)
	}
	// Step 3 with every kept item released before writes nothing.
	wtE := c.worktree("origin/main", true)
	before, _ := os.ReadFile(filepath.Join(wtE, "CHANGELOG.md"))
	if r := c.exec("bash", "", "-c", changelogBlock(t, wtE, "1.0.3")); r.code != 0 || !strings.Contains(r.out, "released before") {
		t.Fatalf("step 3 with no unreleased item: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if after, _ := os.ReadFile(filepath.Join(wtE, "CHANGELOG.md")); string(after) != string(before) {
		t.Fatalf("step 3 with no unreleased item changed CHANGELOG.md:\n%s", after)
	}

	push := func(body string) {
		c.at(gate, func() {
			c.write("CHANGELOG.md", body)
			c.git("commit", "-q", "-am", "edit changelog")
			c.git("push", "-q", "origin", "HEAD:refs/heads/main")
		})
	}
	stage := "## [" + "Unreleased" + "]"
	push(stage + "\n\n### Added\n- staged\n\n" + want)
	if r := entry(idA); r.code != 1 || !strings.Contains(r.out, "opens with '"+stage+"'") {
		t.Fatalf("changelog-entry with a staging heading on top: exit %d\n%s%s", r.code, r.out, r.err)
	}
	push(baseChangelog + "\n" + strings.TrimSuffix(want, baseChangelog))
	if r := entry(idA); r.code != 1 || !strings.Contains(r.out, "opens with '## [1.0.0] - 2026-01-01'") {
		t.Fatalf("changelog-entry with an older version on top: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

// pr-ready refuses an item with no or a malformed changelog attachment, and
// a head that edits CHANGELOG.md, naming the attachment instead.
func TestPRReadyRequiresChangelogAttachment(t *testing.T) {
	c := newChangelogSandbox(t)
	id := c.story("")
	head := c.branch("p", c.base, [2]string{"p.txt", "p\n"})
	c.pr(id, head)
	dir := c.worktree("p", false)
	c.assertFixtures([]string{head}, dir)
	check := shBlock0(t, ".satelle/skills/satelle-pr-ready-check.md")
	ready := func() result {
		c.env["SATELLE_ITEM"] = id
		defer delete(c.env, "SATELLE_ITEM")
		var r result
		c.at(dir, func() { r = c.exec("bash", "", "-c", check) })
		return r
	}
	if r := ready(); r.code != 1 || !strings.Contains(r.out, "no changelog attachment on "+id) || !strings.Contains(r.out, "--name changelog") {
		t.Fatalf("(a) no attachment: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.attach(id, "### Misc\n- odd\n")
	if r := ready(); r.code != 1 || !strings.Contains(r.out, "line 1: unknown section") {
		t.Fatalf("(b) malformed attachment: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.attach(id, "### Added\n- P adds\n")
	if r := ready(); r.code != 0 || !strings.Contains(r.out, "ready: PR #1") {
		t.Fatalf("(d) valid attachment, CHANGELOG.md untouched: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.at(dir, func() {
		c.write("CHANGELOG.md", "## [1.0.0] - 2026-01-01\n\n### Added\n- P's line\n- The first release.\n")
		c.git("commit", "-q", "-am", "edit changelog")
		c.git("push", "-q", "origin", "p")
	})
	c.pr(id, c.rev("p"))
	if r := ready(); r.code != 1 || !strings.Contains(r.out, "edits CHANGELOG.md") || !strings.Contains(r.out, "--name changelog") ||
		!strings.Contains(r.out, "git checkout origin/main -- CHANGELOG.md") {
		t.Fatalf("(c) head edits CHANGELOG.md: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

// Two items cut in parallel from the same main: one lands, the other is sent
// back and merges origin/main without a conflict, because neither touches the
// shared changelog. The control shows the fixture conflicts when they do.
func TestSentBackItemMergesMainCleanly(t *testing.T) {
	c := newChangelogSandbox(t)
	idA := c.story("### Added\n- A adds\n")
	idB := c.story("### Fixed\n- B fixes\n")
	headA := c.branch("a", c.base, [2]string{"a.txt", "a\n"})
	headB := c.branch("b", c.base, [2]string{"b1.txt", "b\n"}, [2]string{"b2.txt", "b\n"})
	wt := c.worktree(headA, true)
	c.assertFixtures([]string{headA, headB}, wt)

	// Land A: fewer commits, so step 1 orders it first.
	count := func(h string) string { return strings.TrimSpace(c.git("rev-list", "--count", "origin/main.."+h)) }
	if count(headA) != "1" || count(headB) != "2" {
		t.Fatalf("commit counts A %s B %s", count(headA), count(headB))
	}
	if got, want := c.verdict(idA, headA, c.base), idA+": keep head "+headA; got != want {
		t.Fatalf("A: %q, want %q", got, want)
	}
	tip := headA
	if r := c.exec("git", "", "merge-base", "--is-ancestor", tip, headB); r.code != 1 {
		t.Fatalf("B contains A: exit %d", r.code)
	}
	reason := "head " + headB + " does not contain main " + tip + " after " + idA + "; merge origin/main into the branch (git fetch origin && git merge origin/main), resolve any conflict in the real work, push, and come through integration again"
	if r := c.exec(satelleBin, reason+"\n", "story", "attach", idB, "--name", "release-sendback", "--file", "-"); r.code != 0 {
		t.Fatalf("release-sendback: %s%s", r.out, r.err)
	}
	if r := c.exec("bash", "", "-c", changelogBlock(t, wt, "1.0.1", idA)); r.code != 0 {
		t.Fatalf("step 3: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.bump(wt, "1.0.1")
	if got := strings.Fields(c.git("diff", "--name-only", headA, "origin/main")); !slices.Equal(got, []string{".version", "CHANGELOG.md"}) {
		t.Fatalf("main after A differs from A in %v", got)
	}
	mainAfterA := c.rev("origin/main")

	// B merges origin/main in DEV.
	dir := c.worktree("b", false)
	c.at(dir, func() {
		c.git("fetch", "-q", "origin")
		if r := c.exec("git", "", "merge", "--no-edit", "-q", "origin/main"); r.code != 0 {
			t.Fatalf("merging origin/main into B: exit %d\n%s%s", r.code, r.out, r.err)
		}
		if u := strings.TrimSpace(c.git("diff", "--name-only", "--diff-filter=U")); u != "" {
			t.Fatalf("unmerged paths: %s", u)
		}
		if r := c.exec("git", "", "diff", "--quiet", c.base, headB, "--", "CHANGELOG.md"); r.code != 0 {
			t.Fatal("B's own commits touch CHANGELOG.md")
		}
		if r := c.exec("git", "", "diff", "--quiet", "origin/main", "HEAD", "--", "CHANGELOG.md"); r.code != 0 {
			t.Fatal("the merge did not take main's CHANGELOG.md unchanged")
		}
		c.git("push", "-q", "origin", "b")
	})
	merged := c.rev("b")

	// Return: pr-ready accepts the merged head and the next run keeps it.
	c.pr(idB, merged)
	c.env["SATELLE_ITEM"] = idB
	c.at(dir, func() {
		if r := c.exec("bash", "", "-c", shBlock0(t, ".satelle/skills/satelle-pr-ready-check.md")); r.code != 0 {
			t.Fatalf("pr-ready on B merged: exit %d\n%s%s", r.code, r.out, r.err)
		}
	})
	delete(c.env, "SATELLE_ITEM")
	if got, want := c.verdict(idB, merged, mainAfterA), idB+": keep head "+merged; got != want {
		t.Fatalf("B merged: %q, want %q", got, want)
	}

	// Control: two branches that each add a line to the changelog, as items
	// did before, conflict there and only there.
	ctl := newChangelogSandbox(t)
	line := func(s string) [2]string {
		return [2]string{"CHANGELOG.md", "## [1.0.0] - 2026-01-01\n\n### Added\n- " + s + "\n- The first release.\n"}
	}
	headX := ctl.branch("x", ctl.base, [2]string{"x.txt", "x\n"}, line("X's line"))
	ctl.branch("y", ctl.base, [2]string{"y.txt", "y\n"}, line("Y's line"))
	ctl.git("push", "-q", "origin", headX+":refs/heads/main")
	ydir := ctl.worktree("y", false)
	ctl.at(ydir, func() {
		ctl.git("fetch", "-q", "origin")
		if r := ctl.exec("git", "", "merge", "--no-edit", "-q", "origin/main"); r.code == 0 {
			t.Fatal("control: merging main into Y did not conflict")
		}
		if u := strings.Fields(ctl.git("diff", "--name-only", "--diff-filter=U")); !slices.Equal(u, []string{"CHANGELOG.md"}) {
			t.Fatalf("control: unmerged paths %v, want only CHANGELOG.md", u)
		}
	})
}

// A sent-back item comes through integration again, where the full suite
// runs on the combined tree.
func TestReworkRerunsTheFullSuite(t *testing.T) {
	for _, wf := range []string{"delivery", "chore"} {
		w, err := workflow.Parse("../.satelle/workflows/" + wf + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Find("approved", "in_progress", ""); err != nil {
			t.Errorf("%s: no approved -> in_progress: %v", wf, err)
		}
		tr, err := w.Find("integration", "approved", "")
		if err != nil {
			t.Fatalf("%s: %v", wf, err)
		}
		found := false
		for _, g := range tr.Groups {
			found = found || slices.Contains(g.Gates, "integration-check")
		}
		if !found {
			t.Errorf("%s: integration -> approved does not run integration-check", wf)
		}
		g, ok := w.Gates["integration-check"]
		if !ok || g.Carry {
			t.Errorf("%s: integration-check %+v (defined %v), want a gate that never carries", wf, g, ok)
		}
	}
}

// No process file names the old staging heading: items record their lines on
// themselves and the release run writes the version section.
func TestProcessUsesNoUnreleasedHeading(t *testing.T) {
	word := "Unreleased"
	// .satelle/remotes/satelle-cicd is the pre-1.0 remote, kept as reference
	// only (operator, 2026-11-07): nothing dispatches it, so its text is not
	// part of the process this test guards.
	reference := filepath.Join("..", ".satelle", "remotes", "satelle-cicd")
	for _, root := range []string{".satelle/skills", ".satelle/workflows", ".satelle/queues", ".satelle/tasks", ".satelle/remotes", "scripts"} {
		err := filepath.WalkDir(filepath.Join("..", root), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && p == filepath.Join("..", root) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if p == reference {
					return filepath.SkipDir
				}
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), word) {
				t.Errorf("%s names %s", strings.TrimPrefix(p, "../"), word)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
