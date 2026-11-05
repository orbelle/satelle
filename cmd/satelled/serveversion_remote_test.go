package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

// Stable substrings the gate prints for remote-baseline refusals (sty_da6c3874).
// Real-repo tests match these when deciding to skip a tree that cannot establish
// a verified baseline.
const (
	serveGateStaleBaseline  = "baseline is stale"
	serveGateCannotVerify   = "cannot verify the serve-tag baseline"
	serveGateFetchRemedy    = "git fetch --tags origin"
	serveGateOkPrefix       = "ok —"
	serveGateFirstReleaseOk = "— ok ("
)

func requireGitAndGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
}

func gateIn(t *testing.T, dir string, env []string, args ...string) (exit int, out string) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(dir, "scripts/check-serve-version.sh")}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	b, err := cmd.CombinedOutput()
	out = string(b)
	if err == nil {
		return 0, out
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), out
	}
	t.Fatalf("gate: %v\n%s", err, out)
	return -1, out
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildServeGateFixture creates a temp repo with a bare origin remote.
// Local has serve-v0.0.1; when stale is true, origin also has serve-v0.0.2
// that the fixture clone has not fetched.
func buildServeGateFixture(t *testing.T, stale bool) (repo, bare string) {
	t.Helper()
	requireGitAndGo(t)

	root := repoRoot(t)
	scriptSrc, err := os.ReadFile(filepath.Join(root, gateScript))
	if err != nil {
		t.Fatalf("read gate script: %v", err)
	}

	bare = filepath.Join(t.TempDir(), "origin.git")
	if out, err := gittest.Command(t, "", "init", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		// Older git without init -b: init then point HEAD at main.
		if out2, err2 := gittest.Command(t, "", "init", "--bare", bare).CombinedOutput(); err2 != nil {
			t.Fatalf("git init --bare: %v\n%s\n(fallback after -b failed: %v\n%s)", err2, out2, err, out)
		}
		gittest.Git(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	}

	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(filepath.Join(seed, "cmd", "satelled"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(seed, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(seed, "go.mod"), "module example.com/fx\n\ngo 1.22\n")
	writeFile(t, filepath.Join(seed, "cmd", "satelled", "main.go"), "package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(seed, ".version"), "satelled.version: 0.0.1\n")
	writeFile(t, filepath.Join(seed, "scripts", "check-serve-version.sh"), string(scriptSrc))
	if err := os.Chmod(filepath.Join(seed, "scripts", "check-serve-version.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	gittest.Git(t, seed, "init", "-b", "main")
	gittest.Git(t, seed, "add", ".")
	gittest.Git(t, seed, "commit", "-m", "seed")
	gittest.Git(t, seed, "tag", "serve-v0.0.1")
	gittest.Git(t, seed, "remote", "add", "origin", bare)
	gittest.Git(t, seed, "push", "-u", "origin", "main")
	gittest.Git(t, seed, "push", "origin", "serve-v0.0.1")

	if stale {
		// Push a newer serve tag from a throwaway clone; seed (and the fixture
		// cloned from it) keep only serve-v0.0.1 locally.
		advParent := t.TempDir()
		adv := filepath.Join(advParent, "adv")
		gittest.Git(t, advParent, "clone", bare, adv)
		gittest.Git(t, adv, "checkout", "main")
		writeFile(t, filepath.Join(adv, "cmd", "satelled", "extra.go"), "package main\n\nvar _ = 1\n")
		writeFile(t, filepath.Join(adv, ".version"), "satelled.version: 0.0.2\n")
		gittest.Git(t, adv, "add", ".")
		gittest.Git(t, adv, "commit", "-m", "advance")
		gittest.Git(t, adv, "tag", "serve-v0.0.2")
		gittest.Git(t, adv, "push", "origin", "main")
		gittest.Git(t, adv, "push", "origin", "serve-v0.0.2")
	}

	// Clone the seed worktree so local tags stay at serve-v0.0.1 even when the
	// bare remote has advanced (a direct clone of bare would fetch the new tag).
	fixParent := t.TempDir()
	repo = filepath.Join(fixParent, "fixture")
	gittest.Git(t, fixParent, "clone", seed, repo)
	gittest.Git(t, repo, "remote", "set-url", "origin", bare)

	if stale {
		remoteTags := gittest.Git(t, repo, "ls-remote", "--tags", "--refs", "origin", "serve-v*")
		if !strings.Contains(remoteTags, "serve-v0.0.2") {
			t.Fatalf("expected remote to have serve-v0.0.2, got:\n%s", remoteTags)
		}
		localTags := gittest.Git(t, repo, "tag", "-l", "serve-v*")
		if strings.Contains(localTags, "serve-v0.0.2") {
			t.Fatalf("fixture local tags unexpectedly include serve-v0.0.2:\n%s", localTags)
		}
	}

	return repo, bare
}

func TestServeGateRefusesStaleBaseline(t *testing.T) {
	repo, _ := buildServeGateFixture(t, true)
	code, out := gateIn(t, repo, nil)
	if code == 0 {
		t.Fatalf("expected non-zero exit on stale baseline, got 0\n%s", out)
	}
	for _, want := range []string{"serve-v0.0.1", "serve-v0.0.2", serveGateFetchRemedy, serveGateStaleBaseline} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, serveGateOkPrefix) || strings.Contains(out, serveGateFirstReleaseOk) {
		t.Errorf("stale refusal must not print an ok verdict\n%s", out)
	}
}

func TestServeGateNormalVerdictAfterFetch(t *testing.T) {
	repo, _ := buildServeGateFixture(t, true)

	gittest.Git(t, repo, "fetch", "--tags", "origin")
	gittest.Git(t, repo, "reset", "--hard", "origin/main")

	code, out := gateIn(t, repo, nil)
	if code != 0 {
		t.Fatalf("expected exit 0 after fetch with no further serve-path change, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "no serve-path changes since serve-v0.0.2") {
		t.Errorf("expected no-change verdict against serve-v0.0.2\n%s", out)
	}
	if strings.Contains(out, serveGateStaleBaseline) {
		t.Errorf("fetched fixture must not report stale baseline\n%s", out)
	}
}

func TestServeGateBumpDemandedWhenTagsAgree(t *testing.T) {
	repo, _ := buildServeGateFixture(t, true)
	gittest.Git(t, repo, "fetch", "--tags", "origin")
	gittest.Git(t, repo, "reset", "--hard", "origin/main")

	writeFile(t, filepath.Join(repo, "cmd", "satelled", "bump_probe.go"), "package main\n\nvar probe = 1\n")
	gittest.Git(t, repo, "add", "cmd/satelled/bump_probe.go")
	gittest.Git(t, repo, "commit", "-m", "probe without bump")

	code, out := gateIn(t, repo, nil)
	if code == 0 {
		t.Fatalf("expected bump demand, got exit 0\n%s", out)
	}
	if strings.Contains(out, serveGateStaleBaseline) {
		t.Errorf("bump path must not be a stale-baseline refusal\n%s", out)
	}
	if !strings.Contains(out, "satelled.version still") && !strings.Contains(out, "bump satelled.version") {
		t.Errorf("expected bump-demanded message\n%s", out)
	}
}

func TestServeGateRefusesUnreachableRemote(t *testing.T) {
	repo, _ := buildServeGateFixture(t, false)
	code, out := gateIn(t, repo, []string{"SERVE_TAG_REMOTE=nonexistent-remote-xyz"})
	if code == 0 {
		t.Fatalf("expected non-zero exit when remote is unreachable, got 0\n%s", out)
	}
	if !strings.Contains(out, serveGateCannotVerify) {
		t.Errorf("missing %q\n%s", serveGateCannotVerify, out)
	}
	if strings.Contains(out, serveGateOkPrefix) || strings.Contains(out, serveGateFirstReleaseOk) {
		t.Errorf("unreachable remote must not print an ok verdict\n%s", out)
	}
}

func TestServeGateOfflineModesIgnoreRemote(t *testing.T) {
	repo, _ := buildServeGateFixture(t, false)
	env := []string{"SERVE_TAG_REMOTE=nonexistent-remote-xyz"}

	code, out := gateIn(t, repo, env, "--paths")
	if code != 0 {
		t.Fatalf("--paths with broken remote: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "cmd/satelled/") {
		t.Errorf("--paths should list cmd/satelled/\n%s", out)
	}

	code, out = gateIn(t, repo, env, "--check-path", "cmd/satelled/main.go")
	if code != 0 {
		t.Fatalf("--check-path with broken remote: exit %d\n%s", code, out)
	}
}
