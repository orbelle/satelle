//go:build unix

package cli

import (
	"bytes"
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
