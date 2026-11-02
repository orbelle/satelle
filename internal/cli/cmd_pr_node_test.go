//go:build !windows

package cli

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/forge"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A node joined on the shipped pr-approve bundle — its repo pointed at the rig's
// widgets repo and its placeholder constitution replaced by the repo's own — runs
// the embedded tsk_pr-approve for its wake, reports exactly the bundle's
// allowlist and one repo (AC5), resolves `doc get principles repo-constitution`
// to the bundle's file rather than anything init scaffolded (AC9), and hands the
// run its definition, from which `satelle pr` reads the repo and the token's
// variable.
func TestNodeJoinedOnThePRApproveBundle(t *testing.T) {
	const constitution = "REPO-CONSTITUTION-SENTINEL-4e1d"
	files, ok := config.EmbeddedRemote("pr-approve")
	if !ok {
		t.Fatal("the binary ships no pr-approve remote bundle")
	}
	r, _ := taskRig(t, "pr-approve", false)
	r.bundle["remote.toml"] = strings.Replace(files["remote.toml"], "https://github.com/OWNER/NAME.git", "https://github.com/acme/widgets.git", 1)
	r.bundle["principles/"+forge.RepoConstitution+".md"] = "---\nname: repo-constitution\ntype: principle\ndescription: the repo's constitution\n---\n\n# Constitution\n\n" + constitution + "\n"
	nodeFetchEnv = append(append([]string(nil), r.repo.Env...),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url."+r.repo.URL+".insteadOf",
		"GIT_CONFIG_VALUE_0=https://github.com/acme/widgets.git")
	setup := r.fakeSetup
	r.fakeSetup = func(f *devtest.Node) {
		setup(f)
		f.Secrets["GH_TOKEN"] = "ghs_pr-approve-node"
	}
	if _, had := os.LookupEnv("GH_TOKEN"); had {
		t.Setenv("GH_TOKEN", "")
		_ = os.Unsetenv("GH_TOKEN")
	}

	var (
		mu     sync.Mutex
		doc    docindex.Doc
		docErr error
		target prTarget
		tgtErr error
		task   string
	)
	useTaskNode(t, &taskPerformers{step: func(item workitem.Item) error {
		mu.Lock()
		defer mu.Unlock()
		task = item.ParentID
		docErr = dispatchInto(context.Background(), "doc-get", map[string]any{"kind": "principles", "name": forge.RepoConstitution}, &doc)
		target, tgtErr = resolvePRTarget("")
		return dispatchInto(context.Background(), "execution-record",
			map[string]any{"id": item.ID, "output": "checked", "outcome": verb.OutcomeAccept}, &struct{}{})
	}}, 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	// With no relay end, the ready report is the node's log line.
	if !regexp.MustCompile(`join: ready: definition=\S* allow=api\.github\.com repos=widgets@[0-9a-f]+ `).MatchString(r.log.String()) {
		t.Errorf("node log does not report exactly the bundle's allowlist and the widgets repo:\n%s", r.log)
	}

	mu.Lock()
	defer mu.Unlock()
	if task != "tsk_pr-approve" {
		t.Fatalf("the wake ran %q, want the embedded tsk_pr-approve\nnode log:\n%s", task, r.log)
	}
	if docErr != nil || !strings.Contains(doc.Body, constitution) {
		t.Errorf("doc get principles repo-constitution = %q, %v; want the bundle's constitution", doc.Body, docErr)
	}
	if tgtErr != nil || target.repo != "acme/widgets" || target.host != "github.com" || target.tokenEnv != "GH_TOKEN" {
		t.Errorf("the run's PR target = %+v, %v; want acme/widgets on github.com with GH_TOKEN", target, tgtErr)
	}
}
