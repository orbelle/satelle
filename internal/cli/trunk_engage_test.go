package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

// Start-of-work trunk check through the real command (sty_9f3e51d1): the repo
// under test is the subject clone of a local bare remote, so `story set
// --status in_progress` compares a real trunk with a real remote.

// trunkEngageRepo is a satelle repo whose working tree is the subject clone of
// a bare remote, with a one-step performing workflow and one story at backlog.
// extraToml is appended to satelle.toml (a [trunk] declaration).
func trunkEngageRepo(t *testing.T, extraToml string) (repo string, r testutil.TrunkRepos, id string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo = tempRepo(t)
	t.Chdir(repo)
	r = testutil.NewTrunkRepos(t).Adopt(t, repo)

	cfg := filepath.Join(repo, ".satelle", "satelle.toml")
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, append(body, []byte("\n"+extraToml)...), 0o644); err != nil {
		t.Fatal(err)
	}
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "closed"]
park = { state = "blocked" }
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	agents := "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n[orchestrator]\nrole = \"agent\"\ncommand = \"in-loop\"\n"
	if err := os.WriteFile(filepath.Join(wfDir, "agents.toml"), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(repo, ".satelle", "agents.toml"))

	out, err := runRoot(t, "story", "create", "--title", "trunk engage", "--body", "goal",
		"--acceptance", "1. the trunk is checked", "--category", "chore")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	// Running inside an agent session, the first command installs that harness's
	// scaffold into the repo. A real repo gitignores it; the fixture does the same,
	// whatever it is, so only the changes a case makes can dirty the tree.
	var ignore strings.Builder
	for _, ln := range strings.Split(r.Git(t, repo, "status", "--porcelain"), "\n") {
		if p, ok := strings.CutPrefix(ln, "?? "); ok {
			ignore.WriteString("/" + p + "\n")
		}
	}
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	prior, _ := os.ReadFile(exclude)
	if err := os.WriteFile(exclude, append(prior, []byte(ignore.String())...), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, r, jsonField(t, out, "id")
}

// trunkLines is every stderr line the trunk check printed.
func trunkLines(stderr string) []string {
	var out []string
	for _, ln := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(ln, "satelle: trunk") {
			out = append(out, ln)
		}
	}
	return out
}

// Outside a detached run the line prints to the command's stderr.
func TestStorySetEngageTrunkLineStaysOnStderrOutsideAGateRun(t *testing.T) {
	repo, r, id := trunkEngageRepo(t, "")
	r.PublishFromPusher(t, "a.txt")
	before := r.Head(t, repo)
	tip := r.Git(t, r.Remote, "rev-parse", "refs/heads/main")

	_, stderr, err := runRootSplit(t, "", "story", "set", id, "--status", "in_progress")
	if err != nil {
		t.Fatalf("engage: %v\n%s", err, stderr)
	}
	want := "satelle: trunk fast-forwarded main by 1 commit(s) " + before[:8] + ".." + tip[:8]
	if lines := trunkLines(stderr); len(lines) != 1 || !strings.HasPrefix(lines[0], want) {
		t.Fatalf("trunk lines = %q, want one starting %q", lines, want)
	}
}

// [trunk] check = false switches the engage-time check off.
func TestStorySetEngageTrunkCheckCanBeDisabled(t *testing.T) {
	repo, r, id := trunkEngageRepo(t, "[trunk]\ncheck = false\n")
	r.PublishFromPusher(t, "a.txt")
	before := r.Head(t, repo)

	_, stderr, err := runRootSplit(t, "", "story", "set", id, "--status", "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if lines := trunkLines(stderr); len(lines) != 0 {
		t.Fatalf("a disabled check printed %q", lines)
	}
	if r.Head(t, repo) != before {
		t.Fatal("a disabled check moved the tree")
	}
}

// `satelle trunk sync`: the same unit, run by hand.
func TestTrunkSyncReportsAndFastForwards(t *testing.T) {
	repo, r, _ := trunkEngageRepo(t, "")
	r.PublishFromPusher(t, "a.txt")
	tip := r.Git(t, r.Remote, "rev-parse", "refs/heads/main")
	before := r.Head(t, repo)

	stdout, stderr, err := runRootSplit(t, "", "trunk", "sync")
	if err != nil {
		t.Fatalf("trunk sync: %v\n%s", err, stderr)
	}
	if want := "satelle: trunk behind origin/main by 1, not moved: fast-forward not requested"; strings.TrimSpace(stdout) != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if r.Head(t, repo) != before {
		t.Fatal("sync without --fast-forward moved the tree")
	}

	stdout, stderr, err = runRootSplit(t, "", "trunk", "sync", "--fast-forward", "--json")
	if err != nil {
		t.Fatalf("trunk sync --fast-forward --json: %v\n%s", err, stderr)
	}
	var rep struct {
		State         string `json:"state"`
		Ahead         int    `json:"ahead"`
		Behind        int    `json:"behind"`
		Remote        string `json:"remote"`
		Trunk         string `json:"trunk"`
		FastForwarded bool   `json:"fast_forwarded"`
		From          string `json:"from"`
		To            string `json:"to"`
	}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the report JSON: %v\n%s", err, stdout)
	}
	if rep.State != "behind" || rep.Behind != 1 || rep.Ahead != 0 || rep.Remote != "origin" || rep.Trunk != "main" ||
		!rep.FastForwarded || rep.From != before || rep.To != tip {
		t.Fatalf("report = %+v, want behind by 1, fast-forwarded %s..%s", rep, before, tip)
	}
	if r.Head(t, repo) != tip {
		t.Fatal("--fast-forward did not move the tree to the remote tip")
	}

	stdout, _, err = runRootSplit(t, "", "trunk", "sync")
	if err != nil || strings.TrimSpace(stdout) != "satelle: trunk main level with origin/main" {
		t.Fatalf("level sync: %v %q", err, stdout)
	}
}

// --remote and --trunk-branch reach the check: a second remote with no HEAD ref is
// unresolvable on its own, and resolves to the named branch with --branch.
func TestTrunkSyncHonoursRemoteAndBranch(t *testing.T) {
	repo, r, _ := trunkEngageRepo(t, "")
	r.Git(t, repo, "remote", "add", "upstream", r.Remote)
	r.Git(t, repo, "fetch", "--quiet", "upstream")
	// A fetch may record the remote's HEAD itself; this remote must have none.
	r.Git(t, repo, "remote", "set-head", "upstream", "--delete")
	// `trunk sync` restores a missing HEAD ref with set-head --auto (sty_92337a13),
	// so the remote's own HEAD must name nothing for the trunk to stay unresolved.
	r.Git(t, r.Remote, "symbolic-ref", "HEAD", "refs/heads/gone")
	r.PublishFromPusher(t, "b.txt")

	stdout, stderr, err := runRootSplit(t, "", "trunk", "sync", "--remote", "upstream", "--json")
	if err != nil {
		t.Fatalf("trunk sync --remote upstream: %v\n%s", err, stderr)
	}
	var rep struct {
		State  string `json:"state"`
		Behind int    `json:"behind"`
		Remote string `json:"remote"`
		Trunk  string `json:"trunk"`
	}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the report JSON: %v\n%s", err, stdout)
	}
	if rep.State != "skipped" {
		t.Fatalf("--remote upstream without --branch: report = %+v, want skipped (upstream has no HEAD ref)", rep)
	}

	stdout, stderr, err = runRootSplit(t, "", "trunk", "sync", "--remote", "upstream", "--trunk-branch", "main", "--json")
	if err != nil {
		t.Fatalf("trunk sync --remote upstream --branch main: %v\n%s", err, stderr)
	}
	rep = struct {
		State  string `json:"state"`
		Behind int    `json:"behind"`
		Remote string `json:"remote"`
		Trunk  string `json:"trunk"`
	}{}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the report JSON: %v\n%s", err, stdout)
	}
	if rep.State != "behind" || rep.Behind != 1 || rep.Remote != "upstream" || rep.Trunk != "main" {
		t.Fatalf("report = %+v, want behind upstream/main by 1", rep)
	}
}

// Any reported state exits 0, including an unreachable remote.
func TestTrunkSyncExitsZeroOnceAStateIsReported(t *testing.T) {
	repo, r, _ := trunkEngageRepo(t, "")
	r.Git(t, repo, "remote", "set-url", "origin", filepath.Join(filepath.Dir(r.Remote), "gone.git"))
	stdout, _, err := runRootSplit(t, "", "trunk", "sync")
	if err != nil {
		t.Fatalf("an offline report must exit 0: %v", err)
	}
	if !strings.HasPrefix(stdout, "satelle: trunk fetch from origin failed (proceeding): ") {
		t.Fatalf("stdout = %q", stdout)
	}
}
