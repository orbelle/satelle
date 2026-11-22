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
	writeFile(t, filepath.Join(s.dir, "scripts", "release-binary.sh"), repoText(t, "scripts/release-binary.sh"), 0o755)
	s.write(".satelle/workflows/default.toml", `name = "default"

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
from = ["backlog"]
to = "done"

[[transition]]
from = ["approved"]
to = "done"
`)
	s.write(".satelle/queues/release.toml", queueTOML(repoQueueBase(t), "default"))
	s.mustConfigValid()
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

func (s *sandbox) at(dir string, fn func()) {
	old := s.dir
	s.dir = dir
	defer func() { s.dir = old }()
	fn()
}

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

func (c *changelogSandbox) pr(id, head string) {
	c.t.Helper()
	c.prs = append(c.prs, map[string]any{"number": len(c.prs) + 1, "headRefOid": head, "title": "change (" + id + ")", "isDraft": false})
	b, _ := json.Marshal(c.prs)
	writeFile(c.t, filepath.Join(c.fake, "prs.json"), string(b), 0o644)
}

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

func keptBlock(t *testing.T, id, head, tip string) string {
	t.Helper()
	return "head=" + head + "\ntip=" + tip + "\n" + idBlock(t, ".satelle/skills/release.md", "### Each kept item's changelog", id)
}

func (c *changelogSandbox) verdict(id, head, tip string) string {
	c.t.Helper()
	r := c.exec("bash", "", "-c", keptBlock(c.t, id, head, tip))
	if r.code != 0 || strings.Count(r.out, "\n") != 1 || !strings.HasSuffix(r.out, "\n") {
		c.t.Fatalf("kept-item block for %s: exit %d, want 0 and one line\n%s%s", id, r.code, r.out, r.err)
	}
	return strings.TrimSuffix(r.out, "\n")
}

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

func (c *changelogSandbox) bump(wt, ver string) {
	c.t.Helper()
	c.at(wt, func() {
		c.write(".version", "satelle.version: "+ver+"\nsatelled.version: 1.0.0\n")
		c.git("add", ".version", "docs/releases")
		c.git("commit", "-q", "-m", "chore(release): v"+ver)
		if got := strings.Fields(c.git("diff", "--name-only", "HEAD~1", "HEAD")); !slices.Equal(got, []string{".version", "docs/releases/v" + ver + ".md"}) {
			c.t.Fatalf("the bump commit touches %v", got)
		}
		c.git("push", "-q", "origin", "HEAD:refs/heads/main")
	})
	c.git("fetch", "-q", "origin")
}

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
	idR := c.story("### Fixed\n- R fixes\n")
	headR := c.branch("r", c.base, [2]string{"docs/releases/v9.9.9.md", "## [9.9.9] - 2026-01-01\n\n### Fixed\n- R's line\n"})
	main := c.base
	wt := c.worktree(headB, true)
	c.assertFixtures([]string{headA, headB, headE, headR}, wt)

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
	if got := c.verdict(idR, headR, main); !strings.HasPrefix(got, idR+": send back: head "+headR+" edits docs/releases/v9.9.9.md") ||
		!strings.Contains(got, "git rm docs/releases/v9.9.9.md") {
		t.Fatalf("edits docs/releases: %q", got)
	}
	if got := c.verdict(idL, headL, main); !strings.HasPrefix(got, idL+": send back: no valid changelog attachment on head "+headL) {
		t.Fatalf("legacy head: %q", got)
	}
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

	block := changelogBlock(t, wt, "1.0.1", idA, idB)
	if r := c.exec("bash", "", "-c", block); r.code != 0 {
		t.Fatalf("step 3: exit %d\n%s%s", r.code, r.out, r.err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	want := "## [1.0.1] - " + today + "\n\n### Added\n- A adds a thing (" + idA + ")\n\n### Changed\n- B changes a thing (" + idB +
		")\n\n### Fixed\n- A fixes a thing (" + idA + ")\n"
	notes := filepath.Join(wt, "docs", "releases", "v1.0.1.md")
	unchanged := func(when string) {
		t.Helper()
		if got, _ := os.ReadFile(filepath.Join(wt, "CHANGELOG.md")); string(got) != baseChangelog {
			t.Fatalf("%s changed CHANGELOG.md:\n%s", when, got)
		}
	}
	got, _ := os.ReadFile(notes)
	if string(got) != want {
		t.Fatalf("docs/releases/v1.0.1.md:\n%q\nwant\n%q", got, want)
	}
	unchanged("step 3")
	if r := c.exec("bash", "", "-c", block); r.code != 0 || !strings.Contains(r.out, "already exists") {
		t.Fatalf("step 3 again: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if again, _ := os.ReadFile(notes); string(again) != want || strings.Contains(string(again), "Unreleased") {
		t.Fatalf("a second step 3 changed the notes:\n%s", again)
	}
	unchanged("a second step 3")
	c.bump(wt, "1.0.1")

	check := shBlock(t, ".satelle/skills/satelle-changelog-entry-check.md", "", "tas_00000000")
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
	if r := entry(idD); !strings.Contains(r.out, "docs/releases/v1.0.2.md on origin/main opens with") {
		t.Fatalf("changelog-entry for 1.0.2 does not read docs/releases/v1.0.2.md: %s", r.out)
	}
	if r := entry(idA); !strings.Contains(r.out, "docs/releases/v1.0.1.md on origin/main holds every line") {
		t.Fatalf("changelog-entry for an item [1.0.1] published does not read docs/releases/v1.0.1.md: %s", r.out)
	}
	wtE := c.worktree("origin/main", true)
	if r := c.exec("bash", "", "-c", changelogBlock(t, wtE, "1.0.3")); r.code != 0 || !strings.Contains(r.out, "released before") {
		t.Fatalf("step 3 with no unreleased item: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if after, _ := os.ReadFile(filepath.Join(wtE, "CHANGELOG.md")); string(after) != baseChangelog {
		t.Fatalf("step 3 with no unreleased item changed CHANGELOG.md:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(wtE, "docs", "releases", "v1.0.3.md")); !os.IsNotExist(err) {
		t.Fatalf("step 3 with no unreleased item wrote docs/releases/v1.0.3.md: %v", err)
	}

	notes102 := "docs/releases/v1.0.2.md"
	section102, _ := os.ReadFile(filepath.Join(gate, notes102))
	push := func(body string) {
		c.at(gate, func() {
			c.write(notes102, body)
			c.git("commit", "-q", "-am", "edit release notes")
			c.git("push", "-q", "origin", "HEAD:refs/heads/main")
		})
	}
	stage := "## [" + "Unreleased" + "]"
	push(stage + "\n\n### Added\n- staged\n\n" + string(section102))
	if r := entry(idD); r.code != 1 || !strings.Contains(r.out, "opens with '"+stage+"'") || !strings.Contains(r.out, notes102) {
		t.Fatalf("changelog-entry with a staging heading on top: exit %d\n%s%s", r.code, r.out, r.err)
	}
	push(baseChangelog + "\n" + string(section102))
	if r := entry(idD); r.code != 1 || !strings.Contains(r.out, "opens with '## [1.0.0] - 2026-01-01'") {
		t.Fatalf("changelog-entry with an older version on top: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestChangelogEntryReadsCHANGELOGForOlderVersions(t *testing.T) {
	c := newChangelogSandbox(t)
	idL := c.story("### Fixed\n- L fixes a thing\n")
	idM := c.story("### Added\n- M adds a thing\n")
	legacy := c.worktree("origin/main", true)
	c.at(legacy, func() {
		c.write(".version", "satelle.version: 1.0.1\nsatelled.version: 1.0.0\n")
		c.write("CHANGELOG.md", "## [1.0.1] - 2026-02-01\n\n### Fixed\n- L fixes a thing ("+idL+")\n\n"+baseChangelog)
		c.git("commit", "-q", "-am", "chore(release): v1.0.1 (old scheme)")
		c.git("push", "-q", "origin", "HEAD:refs/heads/main")
	})
	c.git("fetch", "-q", "origin")
	check := shBlock(t, ".satelle/skills/satelle-changelog-entry-check.md", "", "tas_00000000")
	gate := c.worktree("origin/main", true)
	entry := func(id string) result {
		c.env["SATELLE_ITEM"] = id
		defer delete(c.env, "SATELLE_ITEM")
		var r result
		c.at(gate, func() {
			c.git("checkout", "-q", "--detach", "origin/main")
			r = c.exec("bash", "", "-c", check)
		})
		return r
	}
	if r := entry(idL); r.code != 0 || !strings.Contains(r.out, "CHANGELOG.md on origin/main opens with the [1.0.1] section") {
		t.Fatalf("changelog-entry for a version older than docs/releases: exit %d\n%s%s", r.code, r.out, r.err)
	}

	wt := c.worktree("origin/main", true)
	if r := c.exec("bash", "", "-c", changelogBlock(t, wt, "1.0.2", idM)); r.code != 0 {
		t.Fatalf("step 3 for 1.0.2: exit %d\n%s%s", r.code, r.out, r.err)
	}
	c.bump(wt, "1.0.2")
	if r := entry(idM); r.code != 0 || !strings.Contains(r.out, "docs/releases/v1.0.2.md on origin/main opens with the [1.0.2] section") {
		t.Fatalf("changelog-entry for 1.0.2: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := entry(idL); r.code != 0 || !strings.Contains(r.out, "CHANGELOG.md on origin/main holds every line of "+idL+"'s changelog attachment in the [1.0.1] section, the release that published it") {
		t.Fatalf("changelog-entry for an item CHANGELOG.md published: exit %d\n%s%s", r.code, r.out, r.err)
	}

	c.at(wt, func() {
		c.write(".version", "satelle.version: 1.0.3\nsatelled.version: 1.0.0\n")
		c.git("commit", "-q", "-am", "version with no notes")
		c.git("push", "-q", "origin", "HEAD:refs/heads/main")
	})
	c.git("fetch", "-q", "origin")
	if r := entry(idM); r.code != 1 || !strings.Contains(r.out, "docs/releases/v1.0.3.md") {
		t.Fatalf("changelog-entry for a version with no notes: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestReleaseNotesRulesNameDocsReleases(t *testing.T) {
	for _, f := range []string{".satelle/skills/release.md", ".satelle/skills/satelle-pr-ready-check/SKILL.md", ".satelle/skills/coder.md"} {
		if !strings.Contains(repoText(t, f), "docs/releases/v<version>.md") {
			t.Errorf("%s does not name docs/releases/v<version>.md as where the release writes notes", f)
		}
	}
}

func TestPRReadyRequiresChangelogAttachment(t *testing.T) {
	c := newChangelogSandbox(t)
	id := c.story("")
	head := c.branch("p", c.base, [2]string{"p.txt", "p\n"})
	c.pr(id, head)
	dir := c.worktree("p", false)
	c.assertFixtures([]string{head}, dir)
	ready := func() result {
		c.env["SATELLE_ITEM"] = id
		defer delete(c.env, "SATELLE_ITEM")
		var r result
		c.at(dir, func() { r = runSkillCheck(t, c.sandbox, "satelle-pr-ready-check") })
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
	c.at(dir, func() {
		c.git("checkout", "-q", "origin/main", "--", "CHANGELOG.md")
		c.write("docs/releases/v1.0.1.md", "## [1.0.1] - 2026-02-01\n\n### Added\n- P adds\n")
		c.git("add", "-A")
		c.git("commit", "-q", "-m", "write release notes")
		c.git("push", "-q", "origin", "p")
	})
	c.pr(id, c.rev("p"))
	if r := ready(); r.code != 1 || !strings.Contains(r.out, "edits docs/releases/v1.0.1.md;") || !strings.Contains(r.out, "git rm docs/releases/v1.0.1.md") ||
		!strings.Contains(r.out, "--name changelog") || strings.Contains(r.out, "CHANGELOG.md") {
		t.Fatalf("(e) head adds docs/releases/v1.0.1.md: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestSentBackItemMergesMainCleanly(t *testing.T) {
	c := newChangelogSandbox(t)
	idA := c.story("### Added\n- A adds\n")
	idB := c.story("### Fixed\n- B fixes\n")
	headA := c.branch("a", c.base, [2]string{"a.txt", "a\n"})
	headB := c.branch("b", c.base, [2]string{"b1.txt", "b\n"}, [2]string{"b2.txt", "b\n"})
	wt := c.worktree(headA, true)
	c.assertFixtures([]string{headA, headB}, wt)

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
	if got := strings.Fields(c.git("diff", "--name-only", headA, "origin/main")); !slices.Equal(got, []string{".version", "docs/releases/v1.0.1.md"}) {
		t.Fatalf("main after A differs from A in %v", got)
	}
	mainAfterA := c.rev("origin/main")

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

	c.pr(idB, merged)
	c.env["SATELLE_ITEM"] = idB
	c.at(dir, func() {
		if r := runSkillCheck(t, c.sandbox, "satelle-pr-ready-check"); r.code != 0 {
			t.Fatalf("pr-ready on B merged: exit %d\n%s%s", r.code, r.out, r.err)
		}
	})
	delete(c.env, "SATELLE_ITEM")
	if got, want := c.verdict(idB, merged, mainAfterA), idB+": keep head "+merged; got != want {
		t.Fatalf("B merged: %q, want %q", got, want)
	}

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

func TestReworkRerunsTheFullSuite(t *testing.T) {
	for _, wf := range []string{"delivery", "chore"} {
		w, err := workflow.Parse("../.satelle/workflows/" + wf + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Find("approved", "in_progress", ""); err != nil {
			t.Errorf("%s: no approved -> in_progress: %v", wf, err)
		}
		tr, err := w.Find("integration", "in_review", "")
		if err != nil {
			t.Fatalf("%s: %v", wf, err)
		}
		found := false
		for _, g := range tr.Groups {
			found = found || slices.Contains(g.Gates, "integration-check")
		}
		if !found {
			t.Errorf("%s: integration -> in_review does not run integration-check", wf)
		}
		g, ok := w.Gates["integration-check"]
		if !ok || g.Carry {
			t.Errorf("%s: integration-check %+v (defined %v), want a gate that never carries", wf, g, ok)
		}
	}
}

func TestProcessUsesNoUnreleasedHeading(t *testing.T) {
	word := "Unreleased"
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

func TestItemBackfillsReleasedNotes(t *testing.T) {
	c := newChangelogSandbox(t)
	const rule = ".satelle/skills/satelle-pr-ready-check/scripts/notes-paths.sh"
	if release := repoText(t, ".satelle/skills/release.md"); strings.Contains(release, "satelle-pr-ready-check/scripts/") {
		t.Error("release.md runs a script of satelle-pr-ready-check; a skill is self-contained and applies the backfill rule inline")
	}
	notes := func(ver string) [2]string {
		return [2]string{"docs/releases/v" + ver + ".md", "## [" + ver + "] - 2026-01-01\n\n### Added\n- " + ver + "\n"}
	}
	main := c.branch("released", c.base,
		[2]string{".version", "satelle.version: 1.0.9\nsatelled.version: 1.0.9\n"},
		[2]string{rule, repoText(t, rule)},
		notes("1.0.8"))
	c.git("push", "-q", "origin", main+":refs/heads/main")
	c.git("fetch", "-q", "origin")
	cases := []struct {
		name, refused string
		files         [][2]string
	}{
		{"backfill", "", [][2]string{{"CHANGELOG.md", ""}, notes("1.0.5"), notes("0")}},
		{"current", "docs/releases/v1.0.9.md", [][2]string{notes("1.0.9")}},
		{"later", "docs/releases/v1.0.10.md", [][2]string{{"CHANGELOG.md", ""}, notes("1.0.10")}},
		{"modify", "docs/releases/v1.0.8.md", [][2]string{{"docs/releases/v1.0.8.md", "## [1.0.8] - 2026-01-01\n\n### Added\n- rewritten\n"}}},
		{"delete", "docs/releases/v1.0.8.md", [][2]string{{"docs/releases/v1.0.8.md", ""}}},
		{"changelog", "CHANGELOG.md", [][2]string{{"CHANGELOG.md", baseChangelog + "- an edit\n"}, notes("1.0.5")}},
	}
	for _, tc := range cases {
		id := c.story("### Changed\n- " + tc.name + "\n")
		head := c.branch(tc.name, main, tc.files...)
		c.pr(id, head)
		dir := c.worktree(tc.name, false)
		c.assertFixtures([]string{head}, dir)
		c.env["SATELLE_ITEM"] = id
		var r result
		c.at(dir, func() { r = runSkillCheck(t, c.sandbox, "satelle-pr-ready-check") })
		delete(c.env, "SATELLE_ITEM")
		got := c.verdict(id, head, main)
		if tc.refused == "" {
			if r.code != 0 || !strings.Contains(r.out, "ready: PR #") {
				t.Errorf("%s: pr-ready refused the backfill: exit %d\n%s%s", tc.name, r.code, r.out, r.err)
			}
			if want := id + ": keep head " + head; got != want {
				t.Errorf("%s: step 2a %q, want %q", tc.name, got, want)
			}
			continue
		}
		if r.code != 1 || !strings.Contains(r.out, "edits "+tc.refused+";") {
			t.Errorf("%s: pr-ready does not refuse naming only %s: exit %d\n%s%s", tc.name, tc.refused, r.code, r.out, r.err)
		}
		if !strings.HasPrefix(got, id+": send back: head "+head+" edits "+tc.refused+";") {
			t.Errorf("%s: step 2a %q, want a send-back naming only %s", tc.name, got, tc.refused)
		}
	}
}
