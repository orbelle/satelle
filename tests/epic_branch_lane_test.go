//go:build integration

package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

// sty_b9d9a3df AC4: a repo with no .satelle/workflows runs the shipped route, and
// the shipped route offers the epic-branch lane. Only the judged LLM gates are
// replaced (by same-named always-pass functional checks); the lane's own checks,
// satelle-epic-branch-check and satelle-epic-merged-check, are the shipped ones,
// run by the governed `satelle story set` transition.

// judgedSeats are the LLM seats on the shipped working-lane and container
// routes the test walks. Each is replaced by an always-pass functional check.
var judgedSeats = []string{
	"satelle-story-intent-review",
	"satelle-estimate-actual-review",
	"satelle-step-summary",
	"satelle-workflow-change-review",
	"satelle-story-scope-review",
	"satelle-story-done-review",
	"satelle-instruction-change-review",
	"satelle-instruction-change-trigger",
}

// laneChecks are the shipped checks under test; they must never be stubbed.
var laneChecks = []string{"satelle-epic-branch-check", "satelle-epic-merged-check"}

func passingSkill(name string) string {
	return "---\nname: " + name + "\ntype: skill\ndescription: always-pass functional check for a fixture\n---\n\n```check\ntrue\n```\n"
}

// epicLane is a repo on the shipped route with a remote, an epic and two
// children, each child in a worktree of its own.
type epicLane struct {
	r          testutil.TrunkRepos
	repo       string
	env        []string
	epic, a, b string
	wtA, wtB   string
}

func (w epicLane) run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	return runEnv(t, testBin, dir, w.env, args...)
}

func (w epicLane) must(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := w.run(t, dir, args...)
	if err != nil {
		t.Fatalf("satelle %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func newLaneRepo(t *testing.T) epicLane {
	t.Helper()
	for _, name := range laneChecks {
		for _, stubbed := range judgedSeats {
			if stubbed == name {
				t.Fatalf("%s is the check under test and must not be stubbed", name)
			}
		}
	}
	r := testutil.NewTrunkRepos(t)
	repo := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// `satelle` as the checks call it must be the binary under test.
	bin := t.TempDir()
	if err := os.Symlink(testBin, filepath.Join(bin, "satelle")); err != nil {
		t.Fatal(err)
	}
	w := epicLane{r: r, repo: repo, env: []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}}

	// The tree is a git repo before satelle first touches it: a tree that gains
	// its .git afterwards changes its runtime key, and the worktrees below would
	// then open an empty store.
	w.r = r.Adopt(t, repo)
	mustRun(t, testBin, repo, "init")
	// init's harness scaffolding is untracked and would make trunk dirty, which
	// refuses every engage; it is personal setup, so keep it out of git.
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	var keep strings.Builder
	prior, _ := os.ReadFile(exclude)
	keep.Write(prior)
	for _, ln := range strings.Split(r.Git(t, repo, "status", "--porcelain", "--untracked-files=normal"), "\n") {
		if p := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "??")); p != "" {
			keep.WriteString("/" + p + "\n")
		}
	}
	writeFile(t, exclude, keep.String())

	// The shipped route governs: no authored done.toml or step.toml.
	if _, err := os.Stat(filepath.Join(repo, ".satelle", "workflows", "done.toml")); err == nil {
		t.Fatal("init seeded an authored route; the test would not prove the shipped one")
	}
	writeFile(t, filepath.Join(repo, ".satelle", "satelle.local.toml"),
		"[review]\ngate_create = false\n\n[categories]\nenforce = \"off\"\n\n[trunk]\nprove = \"true\"\n")
	for _, name := range judgedSeats {
		writeFile(t, filepath.Join(repo, ".satelle", "skills", name+".md"), passingSkill(name))
	}

	// The readiness performer is a read-only stub behind a pi-style binding; it
	// reports no repairs.
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "pi")
	performer := "#!/bin/sh\ncat > /dev/null\necho '{\"artifact\":{\"name\":\"ready\",\"type\":\"review\",\"body\":\"parallel: all; edges: none; lanes: none\"},\"changes\":[]}'\n"
	if err := os.WriteFile(stub, []byte(performer), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, ".satelle", "workflows", "agents.toml"), fmt.Sprintf(
		"[ready-reviewer]\ncommand = %q\ntools = \"read,grep,find,ls\"\nmodel = \"stub-model\"\n",
		stub+" -p --tools {tools} {system}"))
	mustRun(t, testBin, repo, "reindex")
	// The .satelle process substrate is tracked in git (sty_45bf867b): init's
	// managed .gitignore re-includes it, which overrides the info/exclude above,
	// so commit it as a repo does; its machine-local files stay ignored.
	r.Git(t, repo, "add", "-A")
	r.Git(t, repo, "commit", "-q", "-m", "track the .satelle substrate")

	create := func(args ...string) string {
		out := w.must(t, repo, append([]string{"story", "create"}, args...)...)
		id := extractID(out, "sty_")
		if id == "" {
			t.Fatalf("no story id:\n%s", out)
		}
		return id
	}
	w.epic = create("--category", "epic-parent", "--title", "Epic", "--body", "umbrella", "--tags", "epic:t")
	w.a = create("--category", "feature", "--title", "Child A", "--body", "a", "--acceptance", "1. a", "--tags", "epic:t")
	w.b = create("--category", "feature", "--title", "Child B", "--body", "b", "--acceptance", "1. b", "--tags", "epic:t")
	w.wtA = w.r.LinkedWorktree(t, repo, "epic/"+w.a)
	w.wtB = w.r.LinkedWorktree(t, repo, "epic/"+w.b)
	return w
}

func (w epicLane) status(t *testing.T, id string) string {
	t.Helper()
	out := w.must(t, w.repo, "story", "get", id)
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), `"status":`) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), `"status":`)), `",`)
		}
	}
	t.Fatalf("no status in:\n%s", out)
	return ""
}

func TestEpicBranchLaneOnTheShippedRoute(t *testing.T) {
	w := newLaneRepo(t)
	remote := w.r

	// The epic's readiness round runs, which sequences its children before they
	// engage. Its children are shipped-lane stories with no readiness step of
	// their own, so `story ready` reports the epic not marked while the accepted
	// round is what the engage gate reads; engaging the children below proves it.
	w.must(t, w.repo, "story", "set", w.epic, "--status", "ready")

	// Nothing merges before the children are done.
	if out, err := w.run(t, w.repo, "story", "set", w.epic, "--status", "merging"); err == nil {
		t.Fatalf("the epic entered merging with its children open:\n%s", out)
	} else if !strings.Contains(out+err.Error(), w.a) {
		t.Errorf("the refusal must name the child still holding the merge back (%s):\n%s", w.a, out)
	}

	trunkBefore := remote.RemoteHead(t)

	// Child A, un-laned: its slice goes to epic/<A> from its own worktree.
	w.must(t, w.wtA, "story", "set", w.a, "--status", "in_progress")
	remote.Commit(t, w.wtA, "a.txt", "a\n")
	remote.Git(t, w.wtA, "push", "--quiet", "origin", "epic/"+w.a)
	w.must(t, w.wtA, "story", "set", w.a, "--status", "done")
	if got := w.status(t, w.a); got != "done" {
		t.Fatalf("child A status = %s, want done", got)
	}
	if got := remote.RemoteHead(t); got != trunkBefore {
		t.Errorf("closing an un-laned child moved origin's trunk: %s -> %s", trunkBefore, got)
	}
	if got := remote.Git(t, w.repo, "ls-remote", "origin", "refs/heads/epic/"+w.a); got == "" {
		t.Errorf("epic/%s is not on origin", w.a)
	}
	if ledger := w.must(t, w.repo, "ledger", "list", "--story", w.a); !strings.Contains(ledger, "satelle-epic-branch-check") {
		t.Errorf("the lane check did not run on A's close; ledger:\n%s", ledger)
	}

	// Control: B's slice is on trunk, so the governed close refuses naming the check.
	w.must(t, w.wtB, "story", "set", w.b, "--status", "in_progress")
	remote.Commit(t, w.wtB, "b.txt", "b\n")
	remote.Git(t, w.wtB, "push", "--quiet", "origin", "epic/"+w.b)
	remote.Git(t, w.repo, "merge", "--quiet", "--no-ff", "-m", "land b on trunk", "epic/"+w.b)
	remote.Git(t, w.repo, "push", "--quiet", "origin", "main")
	out, err := w.run(t, w.wtB, "story", "set", w.b, "--status", "done")
	if err == nil {
		t.Fatalf("a branch-lane child whose slice is on trunk closed:\n%s", out)
	}
	if !strings.Contains(out+err.Error(), "satelle-epic-branch-check") {
		t.Errorf("the refusal must name satelle-epic-branch-check:\n%s\n%v", out, err)
	}
	if got := w.status(t, w.b); got == "done" {
		t.Errorf("B closed despite the refusal")
	}
	// Tagged lane:trunk it is on the trunk lane, and closes.
	w.must(t, w.repo, "story", "set", w.b, "--add-tags", "lane:trunk")
	w.must(t, w.wtB, "story", "set", w.b, "--status", "done")
	if got := w.status(t, w.b); got != "done" {
		t.Fatalf("lane:trunk child B status = %s, want done", got)
	}

	// With every child done the epic may merge. Its close is refused until A's
	// branch is on origin's trunk.
	w.must(t, w.repo, "story", "set", w.epic, "--status", "merging")
	out, err = w.run(t, w.repo, "story", "set", w.epic, "--status", "done")
	if err == nil {
		t.Fatalf("the epic closed with epic/%s unmerged:\n%s", w.a, out)
	}
	if !strings.Contains(out+err.Error(), "satelle-epic-merged-check") {
		t.Errorf("the refusal must name satelle-epic-merged-check:\n%s\n%v", out, err)
	}

	// The merge step's work: merge A's branch, publish trunk once.
	remote.Git(t, w.repo, "merge", "--quiet", "--no-ff", "-m", "merge epic/"+w.a, "origin/epic/"+w.a)
	w.must(t, w.repo, "trunk", "publish", "--story", w.epic)
	w.must(t, w.repo, "story", "set", w.epic, "--status", "done")
	if got := w.status(t, w.epic); got != "done" {
		t.Fatalf("epic status = %s, want done", got)
	}
}
