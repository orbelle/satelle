//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

// ghAppToken is the installation token the fake server mints for the run: a value
// that must appear nowhere but the node's process environment.
const ghAppToken = "ghs_NEVER-WRITTEN-ANYWHERE"

// ghAppNoPermissions is a github-app override that declares no permissions,
// which the node refuses before any read.
const ghAppNoPermissions = `{ provider = "github-app" }`

// ghAppOverride is the [secrets.override] value a definition declares for a
// run-minted GitHub credential, with the permissions its token is minted with.
const ghAppOverride = `{ provider = "github-app", permissions = { contents = "read", pull_requests = "write", statuses = "write" } }`

// ghAppResult is what the task's performer saw of GH_TOKEN, recorded as booleans
// and statuses only, never the value.
type ghAppResult struct {
	mu                    sync.Mutex
	ran, envSet, sameRead bool
	statuses              map[string]int
	expires               time.Time
	err                   error
}

// ghAppTaskRig is a task rig whose definition declares GH_TOKEN from github-app
// and two github.com [[repo]] entries, acme/widgets and acme/gadgets, both fetched
// from the rig's local git server. The fake server grants GH_TOKEN as a token the
// fake GitHub minted for exactly those repos, answers with expires (50 minutes
// ahead), and revokes the token at the fake GitHub when the node ends the fire,
// standing in for the server's DELETE /installation/token. *served is the fake
// once the node has been served.
func ghAppTaskRig(t *testing.T) (r *nodeRig, gh *devtest.GitHub, expires time.Time, served **devtest.Node) {
	t.Helper()
	r, _ = taskRig(t, "nightly-sweep", true)
	gh = devtest.NewGitHub(t)
	gh.Mint(ghAppToken, "acme/widgets", "acme/gadgets")
	r.addOverride("GH_TOKEN", ghAppOverride)
	r.bundle["remote.toml"] += "\n[[repo]]\nurl = \"https://github.com/acme/gadgets.git\"\nref = \"main\"\n"
	nodeFetchEnv = append(append([]string(nil), r.repo.Env...),
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=url."+r.repo.URL+".insteadOf",
		"GIT_CONFIG_VALUE_0=https://github.com/acme/widgets.git",
		"GIT_CONFIG_KEY_1=url."+r.repo.URL+".insteadOf",
		"GIT_CONFIG_VALUE_1=https://github.com/acme/gadgets.git")

	expires = time.Now().Add(50 * time.Minute).UTC().Truncate(time.Second)
	served = new(*devtest.Node)
	setup := r.fakeSetup
	r.fakeSetup = func(f *devtest.Node) {
		setup(f)
		f.Secrets["GH_TOKEN"] = ghAppToken
		f.SecretExpiry = map[string]time.Time{"GH_TOKEN": expires}
		f.OnEnd = func() { gh.Revoke(ghAppToken) }
		*served = f
	}
	// The node's environment must start without GH_TOKEN for its absence after the
	// fire to mean anything.
	if _, had := os.LookupEnv("GH_TOKEN"); had {
		t.Setenv("GH_TOKEN", "")
		_ = os.Unsetenv("GH_TOKEN")
	}
	return r, gh, expires, served
}

// assertTokenGoneAfterTheFire is the fire's end, seen from outside: the node
// ended once, the fake GitHub now refuses the token, the node's environment and
// its process-secret set no longer carry it, and no GH_TOKEN read follows the end.
func assertTokenGoneAfterTheFire(t *testing.T, gh *devtest.GitHub, fake *devtest.Node) {
	t.Helper()
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	if code, err := gh.RepoStatus(ghAppToken, "acme/widgets"); err != nil || code != http.StatusUnauthorized {
		t.Errorf("after the fire the token answered %d (%v), want 401", code, err)
	}
	if v, had := os.LookupEnv("GH_TOKEN"); had && v != "" {
		t.Error("the node's environment still carries GH_TOKEN after the fire")
	}
	if currentNodeProcessSecrets() != nil {
		t.Error("the node still holds its process secrets after the fire")
	}
	ended := false
	for _, c := range fake.Calls() {
		switch {
		case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/end"):
			ended = true
		case ended && c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/secrets/GH_TOKEN"):
			t.Error("GH_TOKEN was read after the fire ended")
		}
	}
}

// ghAppAgentArg, as the test binary's first argument, makes it the executor agent
// of TestSlowNodeTaskWakeProductionDriveGivesTheTaskProcessTheToken (see init): a
// separate process, started by the production engine, that reports what it saw
// of GH_TOKEN.
const ghAppAgentArg = "gh-app-agent"

// ghAppAgentReport is what the agent process saw, written to the file its argv
// names: booleans and statuses only, never the value.
type ghAppAgentReport struct {
	EnvSet   bool           `json:"env_set"`
	Statuses map[string]int `json:"statuses"`
	Err      string         `json:"err,omitempty"`
}

// The repos a run asks the fake GitHub for with its token: the two declared ones
// and one outside [[repo]].
var ghAppAgentRepos = []string{"acme/widgets", "acme/gadgets", "acme/outside"}

func init() {
	// argv: <test binary> gh-app-agent <report file> <fake GitHub URL> <the engine's own args…>
	if len(os.Args) < 4 || os.Args[1] != ghAppAgentArg {
		return
	}
	os.Exit(runGHAppAgent(os.Args[2], os.Args[3]))
}

// runGHAppAgent is the agent process: it reads GH_TOKEN from the environment the
// engine started it with, asks the fake GitHub for each repo with it, writes what
// it saw to report, and prints the token, which the engine must scrub before any
// of its output is kept.
func runGHAppAgent(report, ghURL string) int {
	_, _ = io.Copy(io.Discard, os.Stdin)
	token := os.Getenv("GH_TOKEN")
	rep := ghAppAgentReport{EnvSet: token != "", Statuses: map[string]int{}}
	gh := &devtest.GitHub{URL: ghURL}
	for _, repo := range ghAppAgentRepos {
		code, err := gh.RepoStatus(token, repo)
		if err != nil {
			rep.Err = err.Error()
		}
		rep.Statuses[repo] = code
	}
	raw, _ := json.Marshal(rep)
	if err := os.WriteFile(report, raw, 0o600); err != nil {
		return 1
	}
	fmt.Println("checked the repos with GH_TOKEN=" + token)
	return 0
}

// assertTokenNowhere greps the node's log, every file under the node's root, its
// satelle home and its home, and every status and batch the server received.
func assertTokenNowhere(t *testing.T, r *nodeRig, fake *devtest.Node) {
	t.Helper()
	needle := []byte(ghAppToken)
	if strings.Contains(r.log.String(), ghAppToken) {
		t.Error("the node's log carries the token")
	}
	for _, dir := range []string{r.nodeRoot, os.Getenv("SATELLE_HOME"), os.Getenv("HOME")} {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if body, rerr := os.ReadFile(p); rerr == nil && bytes.Contains(body, needle) {
				t.Errorf("%s carries the token", p)
			}
			return nil
		})
	}
	for _, v := range []any{fake.Statuses(), fake.Pushed()} {
		raw, _ := json.Marshal(v)
		if bytes.Contains(raw, needle) {
			t.Errorf("a record sent to the server carries the token: %s", raw)
		}
	}
}

// treeContains reports whether any regular file under dirs carries needle.
func treeContains(t *testing.T, needle []byte, dirs ...string) bool {
	t.Helper()
	found := false
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if body, rerr := os.ReadFile(p); rerr == nil && bytes.Contains(body, needle) {
				found = true
			}
			return nil
		})
	}
	return found
}

// A local owner run asking for a github-app key gets the server's explicit
// refusal: join says so, naming the key and the code, reports the failed start,
// ends, and never fetches a repo or dials the relay.
func TestNodeJoinStopsOnTheGitHubAppOwnerRunRefusal(t *testing.T) {
	r := newNodeRig(t)
	r.addOverride("GH_TOKEN", ghAppOverride)
	r.fakeSetup = func(f *devtest.Node) {
		f.Refuse = map[string]devtest.Refusal{"GH_TOKEN": {
			Code: remotenode.SecretRefusedGitHubAppOwnerRun, Reason: "an installation token is minted only for a woken remote run",
		}}
	}
	fake := r.serve("nod_a", "node-token", r.encode())
	jerr := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if jerr == nil {
		t.Fatal("join succeeded with the github-app key refused")
	}
	for _, want := range []string{"GH_TOKEN", "local owner run", remotenode.SecretRefusedGitHubAppOwnerRun, "minted only for a woken remote run"} {
		if !strings.Contains(jerr.Error(), want) {
			t.Errorf("join error %q does not name %q", jerr, want)
		}
	}
	assertStartFailureStatuses(t, fake, 0)
	if n := fake.Count(http.MethodPost, "/secrets/GH_TOKEN"); n != 1 {
		t.Errorf("GH_TOKEN reads = %d, want 1", n)
	}
	if n := r.relay.Attempts(); n != 0 {
		t.Errorf("relay dials = %d, want none", n)
	}
	if _, err := os.Stat(filepath.Join(r.nodeRoot, "repos")); err == nil {
		t.Error("the node fetched its repos after the refusal")
	}
}

// A definition whose github-app override has no [[repo]] on github.com is
// refused at join, before any secret is read, naming the override as
// satelle-server does.
func TestNodeJoinRefusesAGitHubAppOverrideWithNoRepoOnGitHub(t *testing.T) {
	r := newNodeRig(t)
	r.bundle["remote.toml"] = strings.Replace(r.bundle["remote.toml"], "https://github.com/acme/widgets.git", "https://ghe.example.com/acme/widgets.git", 1)
	r.addOverride("GH_TOKEN", ghAppOverride)
	fake := r.serve("nod_a", "node-token", r.encode())
	jerr := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if jerr == nil || !strings.Contains(jerr.Error(), "secrets.override.GH_TOKEN: a github-app token needs a [[repo]] on github.com") {
		t.Fatalf("join ended with %v, want the validator's refusal", jerr)
	}
	assertStartFailureStatuses(t, fake, 0)
	for _, key := range []string{"GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if n := fake.Count(http.MethodPost, "/secrets/"+key); n != 0 {
			t.Errorf("%s reads = %d, want none", key, n)
		}
	}
}

// A definition whose github-app override declares no permissions is refused at
// join before any secret is read, naming secrets.override.GH_TOKEN.permissions as
// satelle-server does.
func TestNodeJoinRefusesAGitHubAppOverrideWithNoPermissions(t *testing.T) {
	r := newNodeRig(t)
	r.addOverride("GH_TOKEN", ghAppNoPermissions)
	fake := r.serve("nod_a", "node-token", r.encode())
	jerr := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if jerr == nil || !strings.Contains(jerr.Error(), "secrets.override.GH_TOKEN.permissions: required") {
		t.Fatalf("join ended with %v, want the validator's refusal", jerr)
	}
	assertStartFailureStatuses(t, fake, 0)
	for _, key := range []string{"GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if n := fake.Count(http.MethodPost, "/secrets/"+key); n != 0 {
			t.Errorf("%s reads = %d, want none", key, n)
		}
	}
}
