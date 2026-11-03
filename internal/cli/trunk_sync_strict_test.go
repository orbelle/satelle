package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// `trunk sync --strict` is the mechanism the epic container's merge step runs
// (sty_92337a13): it brings trunk level, prints what came in, and exits non-zero
// only for a state of the stop set.

func TestTrunkSyncStrictBringsTrunkLevelAndNamesTheCommits(t *testing.T) {
	repo, r, _ := trunkEngageRepo(t, "")
	before := r.Git(t, repo, "rev-parse", "main")
	r.PublishFromPusher(t, "a.txt")
	tip := r.Git(t, r.Remote, "rev-parse", "refs/heads/main")
	stdout, stderr, err := runRootSplit(t, "", "trunk", "sync", "--fast-forward", "--strict")
	if err != nil {
		t.Fatalf("strict sync: %v\n%s", err, stderr)
	}
	want := "satelle: trunk fast-forwarded main by 1 commit(s) " + before[:8] + ".." + tip[:8]
	if strings.TrimSpace(stdout) != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if got := r.Git(t, repo, "rev-parse", "main"); got != tip {
		t.Errorf("local main = %s, want the remote tip %s", got, tip)
	}
}

func TestTrunkSyncStrictLevelExitsZero(t *testing.T) {
	_, _, _ = trunkEngageRepo(t, "")
	stdout, _, err := runRootSplit(t, "", "trunk", "sync", "--fast-forward", "--strict")
	if err != nil || !strings.Contains(stdout, "satelle: trunk main level with origin/main") {
		t.Fatalf("level strict sync: %v / %q", err, stdout)
	}
}

// With origin/HEAD deleted and the remote reachable, the strict sync runs
// `git remote set-head --auto` itself, so the trunk resolves and is synced.
func TestTrunkSyncStrictSetHeadAutoRestoresTheTrunk(t *testing.T) {
	repo, r, _ := trunkEngageRepo(t, "")
	r.Git(t, repo, "remote", "set-head", "origin", "--delete")
	r.PublishFromPusher(t, "a.txt")
	tip := r.Git(t, r.Remote, "rev-parse", "refs/heads/main")
	stdout, stderr, err := runRootSplit(t, "", "trunk", "sync", "--fast-forward", "--strict")
	if err != nil {
		t.Fatalf("strict sync with origin/HEAD deleted: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "satelle: trunk fast-forwarded main by 1 commit(s)") {
		t.Errorf("stdout = %q, want the fast-forward line", stdout)
	}
	if got := r.Git(t, repo, "rev-parse", "main"); got != tip {
		t.Errorf("local main = %s, want the remote tip %s", got, tip)
	}
	if got := r.Git(t, repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); got != "origin/main" {
		t.Errorf("origin/HEAD = %q, want set-head --auto to have restored origin/main", got)
	}
}

func TestTrunkSyncStrictHonoursTheTrunkBranchHintAndRefuseOverride(t *testing.T) {
	repo, r, _ := trunkEngageRepo(t, "")
	r.Git(t, repo, "remote", "set-head", "origin", "--delete")
	r.Git(t, r.Remote, "symbolic-ref", "HEAD", "refs/heads/gone")
	r.PublishFromPusher(t, "a.txt")
	tip := r.Git(t, r.Remote, "rev-parse", "refs/heads/main")

	// --trunk-branch names the trunk; the deprecated --branch still does too.
	for _, flag := range []string{"--trunk-branch", "--branch"} {
		if _, _, err := runRootSplit(t, "", "trunk", "sync", "--fast-forward", "--strict", flag, "main"); err != nil {
			t.Fatalf("%s main: %v", flag, err)
		}
	}
	if got := r.Git(t, repo, "rev-parse", "main"); got != tip {
		t.Errorf("main = %s, want %s", got, tip)
	}

	// --refuse replaces the stop set: an ahead trunk proceeds when it is not listed.
	r.Commit(t, repo, "mine.txt", "mine\n")
	if _, _, err := runRootSplit(t, "", "trunk", "sync", "--strict", "--trunk-branch", "main"); err == nil {
		t.Error("ahead trunk passed the default stop set")
	}
	if _, _, err := runRootSplit(t, "", "trunk", "sync", "--strict", "--trunk-branch", "main", "--refuse", "dirty"); err != nil {
		t.Errorf("--refuse dirty did not let an ahead trunk through: %v", err)
	}
	// ... and so does an unresolved one (no hint) when unresolved is not listed.
	if _, _, err := runRootSplit(t, "", "trunk", "sync", "--strict", "--refuse", "dirty"); err != nil {
		t.Errorf("--refuse dirty did not let an unresolved trunk through: %v", err)
	}
	if _, _, err := runRootSplit(t, "", "trunk", "sync", "--strict", "--refuse", "bogus"); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("--refuse bogus: %v, want a rejection naming it", err)
	}
}

func TestTrunkSyncStrictReadsTheConfiguredStopSetAndBranch(t *testing.T) {
	repo, r, _ := trunkEngageRepo(t, "[trunk]\nbase_refuse = [\"dirty\"]\nbranch = \"main\"\n")
	r.Git(t, repo, "remote", "set-head", "origin", "--delete")
	r.Git(t, r.Remote, "symbolic-ref", "HEAD", "refs/heads/gone")
	r.Commit(t, repo, "mine.txt", "mine\n")
	// [trunk] branch names the trunk, and base_refuse leaves ahead out of the stop set.
	if _, stderr, err := runRootSplit(t, "", "trunk", "sync", "--strict"); err != nil {
		t.Fatalf("configured stop set: %v\n%s", err, stderr)
	}
}

// The real command: `story worktree --base main` brings main level first, the
// JSON result carries the report, and --trunk-branch reaches the verb.
func TestStoryWorktreeCommandBringsTheTrunkLevel(t *testing.T) {
	repo, r, _ := trunkEngageRepo(t, "[worktree]\nbranch = \"work/{id}\"\npath = \"../trees/{id}\"\n")
	r.PublishFromPusher(t, "a.txt")
	tip := r.Git(t, r.Remote, "rev-parse", "refs/heads/main")
	stdout, stderr, err := runRootSplit(t, "", "story", "worktree", "sty_wtcmd", "--base", "main", "--trunk-branch", "main", "--json")
	if err != nil {
		t.Fatalf("story worktree: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "satelle: trunk fast-forwarded main by 1 commit(s)") {
		t.Errorf("stderr lacks the report line: %q", stderr)
	}
	if !strings.Contains(stdout, `"trunk"`) || !strings.Contains(stdout, `"fast_forwarded": true`) {
		t.Errorf("result carries no trunk report: %s", stdout)
	}
	if got := r.Git(t, repo, "rev-parse", "main"); got != tip {
		t.Errorf("local main = %s, want %s", got, tip)
	}
	if got := r.Git(t, filepath.Join(filepath.Dir(repo), "trees", "sty_wtcmd"), "rev-parse", "HEAD"); got != tip {
		t.Errorf("worktree HEAD = %s, want %s", got, tip)
	}
}

func TestTrunkSyncStrictDocumented(t *testing.T) {
	out, err := runRoot(t, "trunk", "sync", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"--strict", "--refuse", "--trunk-branch", "base_refuse"} {
		if !strings.Contains(out, w) {
			t.Errorf("trunk sync --help lacks %s", w)
		}
	}
}
