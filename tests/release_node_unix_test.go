//go:build integration && unix

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func (c *candidateSandbox) toNode(t *testing.T) string {
	t.Helper()
	run, _ := c.fire()
	c.mustBlock("### Build the candidate")
	c.mustBlock("### Send back")
	c.driftGate()
	if r := c.block(nodeInputHeading); r.code != 0 {
		t.Fatalf("the node's input: exit %d\n%s%s", r.code, r.out, r.err)
	}
	return run
}

func (c *candidateSandbox) failedAt(t *testing.T, r result, step, want string) {
	t.Helper()
	if r.code != 1 || !strings.Contains(r.out, "failed step "+step+": "+want) {
		t.Fatalf("want failed step %s: %s; exit %d\n%s%s\n%s", step, want, r.code, r.out, r.err, c.nodeFile("out/log"))
	}
	if got := c.nodeFile("back/result"); !strings.Contains(got, "failed step "+step+": ") || !strings.Contains(got, "secret-scan clean") {
		t.Fatalf("back/result after failed step %s:\n%s", step, got)
	}
}

func (c *candidateSandbox) mainStays(t *testing.T, want string) {
	t.Helper()
	if got := strings.Fields(c.exec("git", "", "ls-remote", "origin", "refs/heads/main").out); len(got) == 0 || got[0] != want {
		t.Fatalf("origin/main is %v, want %s", got, want)
	}
	for _, id := range c.ids {
		if st := c.status(id); st != "approved" {
			t.Fatalf("%s is %s, want approved", id, st)
		}
	}
}

func TestReleaseNodeRedSuiteNamesTheStep(t *testing.T) {
	c := newCandidateSandbox(t, false)
	c.item("r", "Makefile", "test:\n\t@echo '--- FAIL: TestRed (0.00s)'\n\t@echo '--- FAIL: TestRed/sub (0.00s)'\n\texit 1\n")
	oldMain := c.rev("origin/main")
	c.toNode(t)
	r := c.nodeSteps("input", "clone", "fetch", "test")
	c.failedAt(t, r, "test", "suite-exit 2")
	if got := c.nodeFile("back/failing"); got != "--- FAIL: TestRed\n--- FAIL: TestRed/sub\n" {
		t.Fatalf("back/failing %q, want the failing test names only", got)
	}
	if _, err := os.Stat(filepath.Join(c.nodeRoot(), "back", "log")); !os.IsNotExist(err) {
		t.Fatalf("the node's log is in back/: %v", err)
	}
	c.mainStays(t, oldMain)
}

func TestReleaseNodeRefusesACandidateNotOnItsBranch(t *testing.T) {
	c := newCandidateSandbox(t, false)
	c.item("a", "a2.txt", "a\n")
	oldMain := c.rev("origin/main")
	run := c.toNode(t)
	c.git("push", "-q", "origin", "--delete", "release/"+run)
	c.git("push", "-q", "origin", oldMain+":refs/heads/release/"+run)
	r := c.nodeSteps("input", "clone", "fetch")
	c.failedAt(t, r, "fetch", "release/"+run+" is at "+oldMain+", not the candidate ")
	if runs := c.suiteRuns(); len(runs) != 0 {
		t.Fatalf("make test ran: %v", runs)
	}
	c.mainStays(t, oldMain)
}

func TestReleaseNodeFailedPublishPushesNothing(t *testing.T) {
	b := newBinarySandbox(t)
	b.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 3 }\n"})
	oldMain := b.rev("origin/main")
	b.tested()
	b.decide()
	writeFile(t, filepath.Join(b.fake, "fail-release"), "", 0o644)
	r := b.nodeSteps("bump", "guard", "publish")
	b.failedAt(t, r, "publish", "release.yml run https://ci.example/7 concluded failure")
	b.mainStays(t, oldMain)
	if b.node("push").code == 0 {
		t.Log("push after a failed publish is the caller's error; run never reaches it")
	}
	sum := sha256.Sum256([]byte(nodeToken))
	if log := b.fakeLog("gh.log"); !strings.Contains(log, "token sha256 "+hex.EncodeToString(sum[:])+"\n") || strings.Contains(log, nodeToken) {
		t.Fatalf("gh was not given the token by its environment, or saw it in argv:\n%s", log)
	}
}

func TestReleaseNodeRefusesAMovedMain(t *testing.T) {
	c := newCandidateSandbox(t, false)
	c.item("a", "a2.txt", "a\n")
	run := c.toNode(t)
	c.mustNode("input", "clone", "fetch", "test", "decide", "bump", "guard", "publish")
	other := c.worktree("origin/main", true)
	c.at(other, func() {
		c.write("other.txt", "other\n")
		c.git("add", "other.txt")
		c.git("commit", "-q", "-m", "a human push")
		c.git("push", "-q", "origin", "HEAD:refs/heads/main")
	})
	moved := strings.TrimSpace(c.exec("git", "", "-C", other, "rev-parse", "HEAD").out)
	r := c.node("push")
	c.failedAt(t, r, "push", "origin/main moved from ")
	c.mainStays(t, moved)
	_ = run
}

func TestReleaseNodeSecretScanRefusesARecord(t *testing.T) {
	c := newCandidateSandbox(t, false)
	c.item("a", "a2.txt", "a\n")
	run := c.toNode(t)
	c.env["NODE_GH_TOKEN"] = run
	defer delete(c.env, "NODE_GH_TOKEN")
	c.mustNode("input")
	r := c.node("decide")
	if r.code != 1 || !strings.Contains(r.out, "failed step secret-scan") || strings.Contains(r.out, "failed step decide") {
		t.Fatalf("a record holding the token: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if _, err := os.Stat(filepath.Join(c.nodeRoot(), "back")); !os.IsNotExist(err) {
		t.Fatalf("back/ was written after a secret-scan hit: %v", err)
	}
}

func TestReleaseNodeRefusesAnotherOrigin(t *testing.T) {
	c := newCandidateSandbox(t, false)
	c.item("a", "a2.txt", "a\n")
	c.toNode(t)
	in := filepath.Join(c.rd, "node-in", "run")
	b, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, in, strings.Replace(string(b), "repo: "+releaseRepo, "repo: https://evil.example/x", 1), 0o600)
	r := c.node("input")
	c.failedAt(t, r, "clone", "origin is not https://github.com/orbelle/satelle")
	if _, err := os.Stat(c.work()); !os.IsNotExist(err) {
		t.Fatalf("the node cloned another origin: %v", err)
	}
	writeFile(t, in, string(b), 0o600)
	c.mustNode("input", "clone")
	c.exec("git", "", "-C", c.work(), "remote", "set-url", "origin", "https://evil.example/x")
	c.failedAt(t, c.node("fetch"), "fetch", "origin is not https://github.com/orbelle/satelle")
}

func TestReleaseNodeKeepsTheTokenFromTheSuiteAndScripts(t *testing.T) {
	c := newCandidateSandbox(t, false)
	c.item("a", "a2.txt", "a\n")
	c.asRoot = true
	log := filepath.Join(c.fake, "priv.log")
	realGit, _ := exec.LookPath("git")
	realGrep, _ := exec.LookPath("grep")
	realBash, _ := exec.LookPath("bash")
	realMake, _ := exec.LookPath("make")
	writeFile(t, filepath.Join(c.fake, "id"), "#!/bin/sh\n[ \"$1\" = -u ] && { echo 0; exit 0; }\nexec /usr/bin/id \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(c.fake, "chown"), "#!/bin/sh\necho \"chown $*\" >>'"+log+"'\n", 0o755)
	writeFile(t, filepath.Join(c.fake, "runuser"), "#!/bin/sh\n{ echo \"== runuser $*\"; env; } >>'"+log+"'\nwhile [ $# -gt 0 ] && [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(c.fake, "git"), "#!/bin/sh\necho \"git $USER $PWD $*\" >>'"+log+"'\nexec '"+realGit+"' \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(c.fake, "grep"), "#!/bin/sh\necho \"grep $*\" >>'"+log+"'\nexec '"+realGrep+"' \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(c.fake, "make"), "#!/bin/sh\n{ echo \"== make $*\"; env; } >>'"+log+"'\nexec '"+realMake+"' \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(c.fake, "bash"), "#!/bin/sh\ncase \"$*\" in *release-binary.sh*|*changelog-items.sh*) { echo \"== bash $*\"; env; } >>'"+log+"' ;; esac\nexec '"+realBash+"' \"$@\"\n", 0o755)
	run := c.toNode(t)
	c.mustNode("input")
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	r := c.node("run")
	if r.code != 0 || !strings.HasSuffix(r.out, "secret-scan clean\n") || !strings.HasPrefix(r.out, "run "+run+"\n") {
		t.Fatalf("run: exit %d, want only the compact result on stdout\n%s%s\n%s", r.code, r.out, r.err, c.nodeFile("out/log"))
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	root := c.nodeRoot()
	if strings.Contains(text, nodeToken) || strings.Contains(text, "GH_TOKEN=") {
		t.Fatalf("the token reached a logged argv or environment:\n%s", text)
	}
	if !strings.Contains(text, "== runuser -u release -- make -C "+root+"/suite test") || !strings.Contains(text, "== bash ") {
		t.Fatalf("the suite did not run as release, or no script ran:\n%s", text)
	}
	var chowns []string
	suiteAt := -1
	for i, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "chown ") {
			chowns = append(chowns, l)
			if strings.Contains(l, "release:release") {
				suiteAt = i
			}
		}
		if suiteAt >= 0 && i > suiteAt && strings.HasPrefix(l, "git ") && !strings.HasPrefix(l, "git release ") && strings.Contains(l, root+"/suite") {
			t.Fatalf("root ran git in the suite's copy after giving it to release: %s", l)
		}
	}
	if want := []string{"chown -R -h root:root " + root + "/in", "chown -R -h release:release " + root + "/suite"}; strings.Join(chowns, "\n") != strings.Join(want, "\n") {
		t.Fatalf("chowns:\n%s\nwant\n%s", strings.Join(chowns, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(text, "grep -rqaF -f /dev/fd/3 ") {
		t.Fatalf("the secret scan did not read the token on fd 3:\n%s", text)
	}
	for _, d := range []string{"out", "work", "back", "in"} {
		if st, err := os.Stat(filepath.Join(root, d)); err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("%s/%s: %v %v, want mode 0700", root, d, st.Mode(), err)
		}
	}
	if res := c.nodeFile("back/result"); !strings.Contains(res, "privilege release\n") || !strings.Contains(res, "main-after ") {
		t.Fatalf("back/result:\n%s", res)
	}
	_ = run
}

func (c *candidateSandbox) draft(tag string) bool {
	c.t.Helper()
	_, err := os.Stat(filepath.Join(c.fake, "drafts", tag))
	return err == nil
}

func (c *candidateSandbox) latest() string {
	c.t.Helper()
	b, _ := os.ReadFile(filepath.Join(c.fake, "latest"))
	return strings.TrimSpace(string(b))
}

func TestReleaseWorkflowCreatesDrafts(t *testing.T) {
	text := repoText(t, ".github/workflows/release.yml")
	var calls []string
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "gh release create ") {
			continue
		}
		call := lines[i]
		for strings.HasSuffix(lines[i], "\\") && i+1 < len(lines) {
			i++
			call += "\n" + lines[i]
		}
		calls = append(calls, call)
	}
	if len(calls) != 2 {
		t.Fatalf("%d gh release create calls in release.yml, want 2:\n%s", len(calls), strings.Join(calls, "\n--\n"))
	}
	serve := 0
	for _, call := range calls {
		args := strings.Fields(call)
		if !slices.Contains(args, "--draft") {
			t.Errorf("a gh release create call without --draft:\n%s", call)
		}
		if slices.Contains(args, "--latest=false") {
			serve++
		}
	}
	if serve != 1 {
		t.Errorf("%d gh release create calls keep --latest=false, want the serve release's only", serve)
	}
}

func TestReleaseNodePublishesItsDraftsOnlyAfterMainMoves(t *testing.T) {
	b := newBinarySandbox(t)
	b.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 5 }\n"})
	oldMain := b.rev("origin/main")
	_, _, _, tested := b.tested()
	b.decide()
	b.mustNode("bump", "guard", "publish")
	for _, tg := range []string{"v1.0.1", "serve-v1.0.1"} {
		if !b.draft(tg) {
			t.Fatalf("release.yml created %s published, want a draft", tg)
		}
	}
	if l := b.latest(); l == "v1.0.1" {
		t.Fatalf("the draft v1.0.1 is the latest release before main moved")
	}
	b.mainStays(t, oldMain)
	b.mustNode("push")
	if !b.draft("v1.0.1") || b.latest() == "v1.0.1" {
		t.Fatalf("push published v1.0.1; only the release step publishes")
	}
	b.mustNode("release")
	for _, tg := range []string{"v1.0.1", "serve-v1.0.1"} {
		if b.draft(tg) {
			t.Fatalf("%s is still a draft after the release step", tg)
		}
	}
	if l := b.latest(); l != "v1.0.1" {
		t.Fatalf("latest release %q, want v1.0.1", l)
	}
	result := b.nodeFile("out/result")
	for _, want := range []string{"published v1.0.1\n", "published serve-v1.0.1\n"} {
		if !strings.Contains(result, want) {
			t.Fatalf("the node's result lacks %q:\n%s", want, result)
		}
	}
	if strings.Contains(result, "to-publish ") || strings.Contains(result, "failed step ") {
		t.Fatalf("a clean run's result:\n%s", result)
	}
	log := b.fakeLog("gh.log")
	if !strings.Contains(log, "release edit v1.0.1 --draft=false --latest --repo orbelle/satelle\n") ||
		!strings.Contains(log, "release edit serve-v1.0.1 --draft=false --latest=false --repo orbelle/satelle\n") {
		t.Fatalf("the drafts were not published with v on latest and serve off it:\n%s", log)
	}
	b.git("fetch", "-q", "origin", "--tags")
	main := b.rev("origin/main")
	if b.rev(main+"^") == tested && b.rev("v1.0.1^{commit}") == main && b.rev("serve-v1.0.1^{commit}") == main {
		return
	}
	t.Fatalf("origin/main %s is not the bump on the tested candidate %s holding v1.0.1 and serve-v1.0.1", main, tested)
}

func TestReleaseNodeFailedMainPushLeavesDraftsAndSkipsTheVersion(t *testing.T) {
	b := newBinarySandbox(t)
	b.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 6 }\n"})
	oldMain := b.rev("origin/main")
	run, _, _, tested := b.tested()
	b.decide()
	lock := filepath.Join(b.origin, "refs", "heads", "main.lock")
	writeFile(t, lock, "", 0o644)
	r := b.nodeSteps("bump", "guard", "publish", "push")
	b.failedAt(t, r, "push", "git push main failed")
	b.mainStays(t, oldMain)
	for _, tg := range []string{"v1.0.1", "serve-v1.0.1"} {
		if !b.draft(tg) {
			t.Fatalf("%s is not a draft after the push of main failed", tg)
		}
	}
	if b.latest() == "v1.0.1" || strings.Contains(b.fakeLog("gh.log"), "release edit ") {
		t.Fatalf("a run that stopped before main moved published a release:\n%s", b.fakeLog("gh.log"))
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	b.git("push", "-q", "origin", "--delete", "release/"+run)
	b.git("push", "-q", "origin", tested+":refs/heads/release/"+run)

	b.mustNode("input", "clone", "fetch", "test", "decide", "bump")
	if got := b.git("-C", b.work(), "show", "HEAD:.version"); !strings.Contains(got, "satelle.version: 1.0.2\n") || !strings.Contains(got, "satelled.version: 1.0.2\n") {
		t.Fatalf("the next bump did not skip 1.0.1, whose tag a stopped run left:\n%s", got)
	}
	b.mustNode("guard", "publish", "push", "release")
	result := b.nodeFile("out/result")
	for _, want := range []string{"version 1.0.2\n", "published v1.0.2\n", "published serve-v1.0.2\n"} {
		if !strings.Contains(result, want) {
			t.Fatalf("the next run's result lacks %q:\n%s", want, result)
		}
	}
	if !b.draft("v1.0.1") || !b.draft("serve-v1.0.1") || b.latest() != "v1.0.2" {
		t.Fatalf("the skipped version's drafts were published, or v1.0.2 is not latest (%q)", b.latest())
	}
}

func TestReleaseNodeStopsAtReleaseAndAResumePublishesTheDrafts(t *testing.T) {
	b := newBinarySandbox(t)
	b.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 7 }\n"})
	run, _, _, _ := b.tested()
	b.decide()
	writeFile(t, filepath.Join(b.fake, "fail-edit"), "", 0o644)
	b.mustNode("bump", "guard", "publish", "push")
	b.git("fetch", "-q", "origin", "--tags")
	bump := b.rev("origin/main")
	r := b.node("release")
	b.failedAt(t, r, "release", "main holds "+bump+" and v1.0.1 serve-v1.0.1 is still a draft")
	result := b.nodeFile("back/result")
	for _, want := range []string{"to-publish v1.0.1\n", "to-publish serve-v1.0.1\n", "main-after " + bump + "\n"} {
		if !strings.Contains(result, want) {
			t.Fatalf("the stopped run's result lacks %q:\n%s", want, result)
		}
	}
	b.mainStays(t, bump)
	if !b.draft("v1.0.1") || !b.draft("serve-v1.0.1") {
		t.Fatal("the drafts were published by a failed release edit")
	}
	if err := os.Remove(filepath.Join(b.fake, "fail-edit")); err != nil {
		t.Fatal(err)
	}

	in := filepath.Join(b.rd, "node-in", "run")
	body, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	refire := regexp.MustCompile(`(?m)^(candidate|drift-checked|base|ff): [0-9a-f]{40}$`).ReplaceAllString(string(body), "${1}: "+bump)
	writeFile(t, in, refire, 0o600)
	b.git("push", "-q", "origin", bump+":refs/heads/release/"+run)
	b.mustNode("input", "clone", "fetch", "test", "decide", "bump", "guard", "publish", "push", "release")
	result = b.nodeFile("out/result")
	for _, want := range []string{"version 1.0.1\n", "published v1.0.1\n", "published serve-v1.0.1\n", "main-after " + bump + "\n"} {
		if !strings.Contains(result, want) {
			t.Fatalf("the re-fire's result lacks %q:\n%s", want, result)
		}
	}
	if b.draft("v1.0.1") || b.draft("serve-v1.0.1") || b.latest() != "v1.0.1" {
		t.Fatalf("the re-fire left a draft, or v1.0.1 is not latest (%q)", b.latest())
	}
	b.mainStays(t, bump)
}

func TestReleaseNodePublishWaitsOutAWatchThatReturnsEarly(t *testing.T) {
	b := newBinarySandbox(t)
	b.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 8 }\n"})
	b.tested()
	b.decide()
	writeFile(t, filepath.Join(b.fake, "pending-views"), "2\n", 0o644)
	b.mustNode("bump", "guard", "publish")
	bump := strings.TrimSpace(b.git("-C", b.work(), "rev-parse", "HEAD"))
	if got := strings.TrimSpace(b.fakeLog("pending-views")); got != "0" {
		t.Fatalf("pending-views %q, want the publish step to have read past every in-progress view", got)
	}
	if n := strings.Count(b.nodeFile("out/log"), "has not concluded after the watch (exit 0); waiting"); n != 2 {
		t.Fatalf("%d waits logged, want 2:\n%s", n, b.nodeFile("out/log"))
	}
	if result := b.nodeFile("out/result"); !strings.Contains(result, "release-yml https://ci.example/7 "+bump+" success\n") || strings.Contains(result, "failed step ") {
		t.Fatalf("the publish step did not record the concluded run:\n%s", result)
	}
	b.mustNode("push", "release")
	result := b.nodeFile("out/result")
	for _, want := range []string{"published v1.0.1\n", "published serve-v1.0.1\n", "main-after " + bump + "\n"} {
		if !strings.Contains(result, want) {
			t.Fatalf("the run's result lacks %q:\n%s", want, result)
		}
	}
	if strings.Contains(result, "to-publish ") || strings.Contains(result, "failed step ") {
		t.Fatalf("a clean run's result:\n%s", result)
	}
	if b.draft("v1.0.1") || b.draft("serve-v1.0.1") || b.latest() != "v1.0.1" {
		t.Fatalf("a draft is left, or v1.0.1 is not latest (%q)", b.latest())
	}
}

func TestReleaseNodePublishFailsAWatchThatReturnsBeforeTheRunConcludes(t *testing.T) {
	b := newBinarySandbox(t)
	b.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 9 }\n"})
	oldMain := b.rev("origin/main")
	b.tested()
	b.decide()
	writeFile(t, filepath.Join(b.fake, "pending-views"), "100\n", 0o644)
	b.env["SATELLE_RELEASE_WAIT"] = "3"
	defer delete(b.env, "SATELLE_RELEASE_WAIT")
	r := b.nodeSteps("bump", "guard", "publish")
	b.failedAt(t, r, "publish", "the watch of release.yml run https://ci.example/7 returned (exit 0) before the run concluded, and it had not concluded after 3 checks")
	if got := strings.TrimSpace(b.fakeLog("pending-views")); got != "97" {
		t.Fatalf("pending-views %q, want exactly 3 views within the bound", got)
	}
	for _, f := range []string{"out/result", "back/result"} {
		if got := b.nodeFile(f); strings.Contains(got, "release-yml ") {
			t.Fatalf("%s records a conclusion for a run that never concluded:\n%s", f, got)
		}
	}
	b.mainStays(t, oldMain)
	if strings.Contains(b.fakeLog("gh.log"), "release edit ") {
		t.Fatalf("a failed publish published a release:\n%s", b.fakeLog("gh.log"))
	}
}
