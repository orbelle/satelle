//go:build unix

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

const nestedTokenKey = "CLAUDE_CODE_OAUTH_TOKEN"

// fakeHarness stands in for the executor's harness on a node: it runs the
// command it is given as a child with a fresh TMPDIR (as a dispatch overlays
// one) and with the node's credentials stripped: mode "one" drops
// CLAUDE_CODE_OAUTH_TOKEN, mode "all" every CLAUDE_CODE_* variable and the
// hand-off's path variable too.
const fakeHarness = `#!/bin/sh
mode=$1; shift
export TMPDIR="$1"; shift
case "$mode" in
one) exec env -u CLAUDE_CODE_OAUTH_TOKEN "$@" ;;
all)
	for v in $(env | sed -n 's/^\(CLAUDE_CODE_[A-Za-z0-9_]*\)=.*/\1/p'); do unset "$v"; done
	unset ` + remotedef.EnvSecretsFile + `
	exec "$@" ;;
esac
exit 2
`

// fakeReviewer stands in for the reviewer's claude: it records its argv and the
// sha256 of the token it was started with (never the token itself), then
// answers a verdict.
const fakeReviewer = `#!/bin/sh
rec="$(dirname "$0")/rec"
printf '%s\n' "$@" >> "$rec/argv"
cat >/dev/null
if [ -n "$CLAUDE_CODE_OAUTH_TOKEN" ]; then
	printf '%s' "$CLAUDE_CODE_OAUTH_TOKEN" | sha256sum | cut -d' ' -f1 >> "$rec/token-sha"
fi
echo '{"decision":"reject","notes":"the fake reviewer judged the PR","reviewed":"the pointers"}'
`

// nestedRun is one `satelle pr approve` run below the fake harness.
type nestedRun struct {
	out     string // the child's stdout and stderr
	tmp     string // the TMPDIR the harness gave it
	tokens  string // the reviewer's token-sha record ("" when none arrived)
	argv    string // the reviewer's recorded argv
	harness string
}

// approveBelowHarness runs `satelle pr approve` for story as a child of the fake
// harness in mode, the way the executor's Bash tool runs it on a node.
func approveBelowHarness(t *testing.T, n *prNode, story, mode, bin string) nestedRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(bin, "rec")
	_ = os.RemoveAll(rec)
	if err := os.MkdirAll(rec, 0o700); err != nil {
		t.Fatal(err)
	}
	r := nestedRun{tmp: t.TempDir(), harness: filepath.Join(bin, "harness")}
	var out bytes.Buffer
	cmd := exec.Command("sh", r.harness, mode, r.tmp, exe, "pr", "approve", "--story", story, "--execution", n.exe)
	cmd.Dir = n.repo
	cmd.Env = append(os.Environ(), "SATELLE_CLI_REEXEC=1", testForgeAPIURLEnv+"="+n.gh.URL)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("pr approve below the harness (%s): %v\n%s", mode, err, out.String())
	}
	r.out = out.String()
	b, _ := os.ReadFile(filepath.Join(rec, "token-sha"))
	r.tokens = strings.TrimSpace(string(b))
	a, _ := os.ReadFile(filepath.Join(rec, "argv"))
	r.argv = string(a)
	return r
}

// AC1, AC2: on a node whose definition declares CLAUDE_CODE_OAUTH_TOKEN, the
// reviewer `satelle pr approve` starts gets that token in its environment even
// when the executor's harness strips CLAUDE_CODE_* from what it runs, and the
// token is in none of the reviewer's argv, the run's output, the dispatch
// scratch, the ledger, the store or the repo.
func TestPRApproveBelowAStrippingHarnessHandsTheReviewerTheNodeToken(t *testing.T) {
	const token = "sk-ant-oat01-NESTED-SENTINEL-9c1f"
	sum := sha256.Sum256([]byte(token))
	want := hex.EncodeToString(sum[:])

	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv(nestedTokenKey, "")
	_ = os.Unsetenv(nestedTokenKey)

	story, body := criteriaBody(t)
	n := newPRNode(t, story, true)
	n.addPR(story, body)
	// The harness gives the child its own TMPDIR, so the child's credentials
	// file must sit under SATELLE_HOME for the suite's guard.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("SATELLE_HOME"), "xdg"))
	bin := t.TempDir()
	reviewer := filepath.Join(bin, "fake-claude")
	for path, script := range map[string]string{reviewer: fakeReviewer, filepath.Join(bin, "harness"): fakeHarness} {
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRepoFile(t, n.repo, ".satelle/agents.toml",
		"[executor]\nharness = \"in-loop\"\n\n[reviewer]\nrole = \"reviewer\"\ncommand = \""+reviewer+" {system}\"\ntools = \"Read\"\n")

	// What join does with the definition's declared secrets for the fire.
	var joinLog bytes.Buffer
	restore := setNodeProcessSecrets(map[string]string{nestedTokenKey: token}, &joinLog)
	restored := false
	t.Cleanup(func() {
		if !restored {
			restore()
		}
	})
	handoff, err := remotedef.HandoffPath()
	if err != nil {
		t.Fatal(err)
	}

	var runs []nestedRun
	for _, mode := range []string{"one", "all"} {
		r := approveBelowHarness(t, n, story, mode, bin)
		runs = append(runs, r)
		if r.tokens != want {
			t.Errorf("harness mode %s: the reviewer's token sha = %q, want the declared token's (%s)\nrun output:\n%s", mode, r.tokens, want, r.out)
		}
		if !strings.Contains(r.out, `"outcome": "reject"`) || !strings.Contains(r.out, "the fake reviewer judged the PR") {
			t.Errorf("harness mode %s: the run did not reach the reviewer's verdict:\n%s", mode, r.out)
		}
	}

	restore()
	restored = true
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Errorf("the hand-off %s is still there after the fire (stat err %v)", handoff, err)
	}
	if strings.Contains(joinLog.String(), token) || strings.Contains(joinLog.String(), "warning") {
		t.Errorf("join's log names the token or warns:\n%s", joinLog.String())
	}

	// With the fire over the same strip leaves the reviewer without a token: the
	// probe can fail.
	after := approveBelowHarness(t, n, story, "one", bin)
	if after.tokens != "" {
		t.Errorf("after the fire the reviewer still got a token (sha %s)", after.tokens)
	}

	// AC2: the token is written nowhere a run keeps.
	for i, r := range append(runs, after) {
		for name, text := range map[string]string{"reviewer argv": r.argv, "run output": r.out} {
			if text == "" && name == "reviewer argv" {
				t.Errorf("run %d recorded no reviewer argv", i)
			}
			if strings.Contains(text, token) {
				t.Errorf("run %d: the token is in the %s", i, name)
			}
		}
		assertTreeLacks(t, r.tmp, token, "the dispatch scratch")
	}
	for _, id := range []string{story, n.exe} {
		ledgerOut, err := runRoot(t, "ledger", "list", "--story", id, "--json")
		if err != nil {
			t.Fatalf("ledger list --story %s: %v\n%s", id, err, ledgerOut)
		}
		if strings.Contains(ledgerOut, token) {
			t.Errorf("the token is in %s's ledger", id)
		}
	}
	assertTreeLacks(t, n.repo, token, "the repo")
	assertTreeLacks(t, os.Getenv("SATELLE_HOME"), token, "the store")
	assertTreeLacks(t, cache, token, "the user cache dir")
}

// assertTreeLacks fails when any file under root holds needle.
func assertTreeLacks(t *testing.T, root, needle, what string) {
	t.Helper()
	if root == "" {
		t.Fatalf("%s: no root to scan", what)
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte(needle)) {
			t.Errorf("the token is in %s: %s", what, path)
		}
		return nil
	})
	if err != nil {
		t.Errorf("scanning %s: %v", what, err)
	}
}
