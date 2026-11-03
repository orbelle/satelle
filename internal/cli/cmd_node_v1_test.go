//go:build !windows

package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// v1Keys are the keys the v1 task definition below declares, with the values the
// fake serves for them: a [secrets] name, a github-app override and a satelle:
// team override.
var v1Keys = map[string]string{"API_KEY": "api-SENTINEL-v1", "GH_TOKEN": "ghs_SENTINEL-v1", "SAT": "sat-SENTINEL-v1"}

// unsetKeys clears keys from the test's environment, so their presence later
// means the node put them there.
func unsetKeys(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, had := os.LookupEnv(k); had {
			t.Setenv(k, "")
			_ = os.Unsetenv(k)
		}
	}
}

// v1TaskRig is a task rig whose v1 definition declares [setup] on_create and
// post_start commands that append a and b to order, a [secrets] name, a
// github-app override and a satelle: override, and the rig's one [[repo]].
func v1TaskRig(t *testing.T) (r *nodeRig, subject *subjectStory, order string) {
	t.Helper()
	r, subject = taskRig(t, "nightly-sweep", true)
	order = filepath.Join(t.TempDir(), "order")
	r.setSetup([]string{"echo a >> " + order}, []string{"echo b >> " + order})
	r.bundle["remote.toml"] += "\n[secrets]\nnames = [\"API_KEY\"]\n"
	r.addOverride("GH_TOKEN", ghAppOverride)
	r.addOverride("SAT", `"satelle:SAT_VALUE"`)
	setup := r.fakeSetup
	r.fakeSetup = func(f *devtest.Node) {
		setup(f)
		for k, v := range v1Keys {
			f.Secrets[k] = v
		}
	}
	unsetKeys(t, "API_KEY", "GH_TOKEN", "SAT")
	return r, subject, order
}

// AC1, AC2: an old-shape definition is refused at join before anything of it is
// acted on: no secret is read, no setup runs, the failed start names the old key
// and its v1 replacement, and the node ends once.
func TestNodeJoinRefusesAnOldShapeDefinitionBeforeAnySecretRead(t *testing.T) {
	r := newNodeRig(t)
	r.bundle["remote.toml"] = `satelle_version = "0.0.702"
remote_id = "rmt_abc123"
lifecycle = "idle"

[[secret]]
key = "CLAUDE_CODE_OAUTH_TOKEN"
scope = "team"
from = "env"

[[repo]]
url = "https://github.com/acme/widgets.git"
ref = "main"
`
	fake := r.serve("nod_a", "node-token", r.encode())
	jerr := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if jerr == nil || !strings.Contains(jerr.Error(), "secret[0]: [[secret]] was replaced in v1 by [secrets]") {
		t.Fatalf("join ended with %v, want the old key named with its v1 replacement", jerr)
	}
	if n := fake.Count(http.MethodPost, "/secrets/CLAUDE_CODE_OAUTH_TOKEN"); n != 0 {
		t.Errorf("secret reads = %d, want none", n)
	}
	assertStartFailureStatuses(t, fake, 0)
	st := fake.Statuses()
	if last := st[len(st)-1]; !strings.Contains(last["findings"], "secret[0]") {
		t.Errorf("the failed start's findings = %q, want secret[0] named", last["findings"])
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
}

// stubSwapRunner replaces the node's swap runner with record, which sees each
// argv and succeeds, so a definition that declares swap joins without root.
func stubSwapRunner(t *testing.T, record func(argv []string)) {
	t.Helper()
	prev := remotenode.SwapRunner
	remotenode.SwapRunner = func(_ context.Context, argv []string) error {
		record(argv)
		return nil
	}
	t.Cleanup(func() { remotenode.SwapRunner = prev })
}

// swapRecorder replaces the node's swap runner with one that records each argv,
// and the task step with one that records "wake" in the same log, so the order of
// the swap's lifecycle around the job is visible.
func swapRecorder(t *testing.T) (log func() [][]string) {
	t.Helper()
	var (
		mu  sync.Mutex
		got [][]string
	)
	stubSwapRunner(t, func(argv []string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, argv)
	})
	useTaskNode(t, &taskPerformers{step: func(item workitem.Item) error {
		mu.Lock()
		got = append(got, []string{"wake"})
		mu.Unlock()
		return dispatchInto(context.Background(), "execution-record",
			map[string]any{"id": item.ID, "output": "swapped", "outcome": verb.OutcomeAccept}, &struct{}{})
	}}, 0)
	return func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), got...)
	}
}
