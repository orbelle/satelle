//go:build clislow && !windows

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A woken task-kind run whose definition declares GH_TOKEN from github-app gets,
// read once at join through POST /secrets/GH_TOKEN, a token scoped to exactly its
// [[repo]] entries in its environment: the fake GitHub answers both declared
// repos and refuses one outside them. A second read in the same fire returns the
// same token with the expires_at the server set. When the fire ends the token is
// revoked (the server's end stands in for DELETE /installation/token), the node's
// environment no longer carries it, no read follows the end, and the token is in
// no log, file, ledger row or pushed batch.
func TestSlowNodeTaskWakeGetsARepoScopedGitHubAppToken(t *testing.T) {
	r, gh, expires, served := ghAppTaskRig(t)

	res := &ghAppResult{statuses: map[string]int{}}
	useTaskNode(t, &taskPerformers{step: func(item workitem.Item) error {
		res.mu.Lock()
		defer res.mu.Unlock()
		res.ran = true
		env := os.Getenv("GH_TOKEN")
		res.envSet = env != ""
		for _, repo := range ghAppAgentRepos {
			code, err := gh.RepoStatus(env, repo)
			if err != nil {
				res.err = err
			}
			res.statuses[repo] = code
		}
		// The second read of the fire, as a tool in the run would make it.
		f := *served
		c := remotenode.NewNodeClient(f.URL, f.ID(), f.Token(), f.Client())
		v, exp, err := c.SecretWithExpiry(context.Background(), "GH_TOKEN")
		if err != nil {
			res.err = err
		}
		res.sameRead = err == nil && v == env
		res.expires = exp
		return dispatchInto(context.Background(), "execution-record",
			map[string]any{"id": item.ID, "output": "checked the repos", "outcome": verb.OutcomeAccept}, &struct{}{})
	}}, 0)

	w := r.wake(t)
	fake, joined := w.fake, w.joined
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}

	// AC2: the run had the token, scoped to its repos; one token over two reads.
	res.mu.Lock()
	defer res.mu.Unlock()
	if !res.ran || res.err != nil {
		t.Fatalf("performer ran = %v, err = %v\nnode log:\n%s", res.ran, res.err, r.log)
	}
	if !res.envSet {
		t.Error("the woken run's environment carried no GH_TOKEN")
	}
	if res.statuses["acme/widgets"] != http.StatusOK || res.statuses["acme/gadgets"] != http.StatusOK {
		t.Errorf("declared repos answered %v, want 200 for both", res.statuses)
	}
	if !res.sameRead {
		t.Error("the second read in the fire did not return the token in the environment")
	}
	if !res.expires.Equal(expires) {
		t.Errorf("second read expires_at = %v, want %v", res.expires, expires)
	}
	if n := fake.Count(http.MethodPost, "/secrets/GH_TOKEN"); n != 2 {
		t.Errorf("GH_TOKEN reads = %d, want 2 (join, and the run's own)", n)
	}
	// The read carries nothing but the key: the server mints the token with the
	// permissions of the definition it stores, never ones the node sends.
	for _, c := range fake.Calls() {
		if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/secrets/GH_TOKEN") && c.Body != "" {
			t.Errorf("GH_TOKEN read carried body %q, want none", c.Body)
		}
	}
	st := fake.Statuses()
	if len(st) == 0 || st[len(st)-1]["run_state"] != "concluded" || st[len(st)-1]["outcome"] != verb.OutcomeAccept {
		t.Errorf("last status = %v, want the run concluded accept", st)
	}

	// AC4: the token's repo scope is the fence, and the token is gone after the fire.
	if got := res.statuses["acme/outside"]; got != http.StatusNotFound {
		t.Errorf("a repo outside [[repo]] answered %d with the token, want 404", got)
	}
	assertTokenGoneAfterTheFire(t, gh, fake)

	// AC5: the value is in no log, file, ledger row or pushed record.
	assertTokenNowhere(t, r, fake)
}

// The production drive of a woken task-kind run: nodeDriveOptions as the node
// ships it builds the engine (and calls SetProcessSecrets on it), and the engine
// starts the [executor] binding as a real process. That process finds GH_TOKEN in
// the environment it inherits, scoped to exactly the definition's [[repo]]
// entries; the token is gone when the fire ends; and the token the process prints
// is scrubbed (redacted, not dropped) from everything the node keeps or sends.
func TestSlowNodeTaskWakeProductionDriveGivesTheTaskProcessTheToken(t *testing.T) {
	r, gh, _, _ := ghAppTaskRig(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "agent-report.json")
	r.bundle["workflows/agents.toml"] += "\n[executor]\nrole = \"agent\"\ncommand = \"" +
		exe + " " + ghAppAgentArg + " " + report + " " + gh.URL + " {system}\"\ntools = \"read_file\"\n"

	w := r.wake(t)
	fake, joined := w.fake, w.joined
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}

	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("the executor process never reported: %v\nnode log:\n%s", err, r.log)
	}
	var rep ghAppAgentReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Err != "" {
		t.Fatalf("the executor process could not reach the fake GitHub: %s", rep.Err)
	}
	if !rep.EnvSet {
		t.Error("the executor process's environment carried no GH_TOKEN")
	}
	want := map[string]int{"acme/widgets": http.StatusOK, "acme/gadgets": http.StatusOK, "acme/outside": http.StatusNotFound}
	for repo, code := range want {
		if rep.Statuses[repo] != code {
			t.Errorf("the executor process's token answered %d for %s, want %d", rep.Statuses[repo], repo, code)
		}
	}
	if n := fake.Count(http.MethodPost, "/secrets/GH_TOKEN"); n != 1 {
		t.Errorf("GH_TOKEN reads = %d, want 1 (join's)", n)
	}
	st := fake.Statuses()
	if len(st) == 0 || st[len(st)-1]["run_state"] != "concluded" || st[len(st)-1]["outcome"] == verb.OutcomeReject {
		t.Errorf("last status = %v, want the run concluded without a reject", st)
	}

	assertTokenGoneAfterTheFire(t, gh, fake)
	assertTokenNowhere(t, r, fake)
	// The scrub is not vacuous: the process's output was kept, with the token
	// redacted by name. GH_TOKEN is in no binding's env, so only the engine's
	// process secrets (SetProcessSecrets in nodeDriveOptions) can have redacted it.
	if !treeContains(t, []byte("[redacted:GH_TOKEN]"), r.nodeRoot, os.Getenv("SATELLE_HOME")) {
		t.Errorf("no kept record carries the redacted token: the executor's output was not scrubbed by the process secrets\nnode log:\n%s", r.log)
	}
}
