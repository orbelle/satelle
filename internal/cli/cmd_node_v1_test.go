//go:build !windows

package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

// AC2: a node joined on a v1 task definition runs it end to end against the
// fake node API: on_create then post_start run in order, every [secrets] name
// and override is read once at join by its key, the checkout is exactly the
// declared [[repo]], and the task runs and concludes accept with the keys in its
// environment.
func TestNodeJoinRunsV1TaskDefinition(t *testing.T) {
	r, subject, order := v1TaskRig(t)
	var (
		mu  sync.Mutex
		env map[string]string
	)
	useTaskNode(t, &taskPerformers{step: func(item workitem.Item) error {
		mu.Lock()
		env = map[string]string{}
		for k := range v1Keys {
			env[k] = os.Getenv(k)
		}
		mu.Unlock()
		return dispatchInto(context.Background(), "execution-record",
			map[string]any{"id": item.ID, "output": "ran on v1", "outcome": verb.OutcomeAccept}, &struct{}{})
	}}, 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if got := readFile(t, order); got != "a\nb\n" {
		t.Errorf("setup order = %q, want on_create then post_start, once each", got)
	}
	for key := range v1Keys {
		if n := w.fake.Count(http.MethodPost, "/secrets/"+key); n != 1 {
			t.Errorf("%s reads = %d, want exactly 1, at join, by its key", key, n)
		}
	}
	if n := w.fake.Count(http.MethodPost, "/secrets/SAT_VALUE"); n != 0 {
		t.Errorf("the node read the satelle: override by its store name %d times; it reads by key", n)
	}
	mu.Lock()
	if !reflect.DeepEqual(env, v1Keys) {
		t.Errorf("the task's environment carried %v of the declared keys, want all", redactedKeys(env, v1Keys))
	}
	mu.Unlock()
	if got := dirNames(t, filepath.Join(r.nodeRoot, "repos")); !reflect.DeepEqual(got, []string{"widgets"}) {
		t.Errorf("checked out %v, want exactly the declared [[repo]] widgets", got)
	}
	if !regexp.MustCompile(`join: ready: definition=\S* allow= repos=widgets@[0-9a-f]+ `).MatchString(r.log.String()) {
		t.Errorf("the ready line does not report exactly the declared repo:\n%s", r.log)
	}
	if got, want := runSteps(t, w.fake, subject.ID()), []string{"queued//", "running/in_progress/", "running/done/", "concluded//accept"}; !reflect.DeepEqual(got, want) {
		t.Errorf("run steps = %v, want %v", got, want)
	}
	for k, v := range v1Keys {
		if strings.Contains(r.log.String(), v) {
			t.Errorf("the node log carries the value of %s", k)
		}
	}
	for _, line := range []string{"join: secret API_KEY read (backend)", "join: secret GH_TOKEN read (github-app github.com)", "join: secret SAT read (satelle:SAT_VALUE)"} {
		if !strings.Contains(r.log.String(), line) {
			t.Errorf("the join log lacks %q", line)
		}
	}
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

// swapRecorder replaces the node's swap runner with one that records each argv,
// and the task step with one that records "wake" in the same log, so the order of
// the swap's lifecycle around the job is visible.
func swapRecorder(t *testing.T) (log func() [][]string) {
	t.Helper()
	var (
		mu  sync.Mutex
		got [][]string
	)
	prev := remotenode.SwapRunner
	remotenode.SwapRunner = func(_ context.Context, argv []string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, argv)
		return nil
	}
	t.Cleanup(func() { remotenode.SwapRunner = prev })
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

// AC5: [machine] swap is carried unchanged to join, which makes a fresh
// swapfile of that size for the job and removes it after; with no swap nothing
// of it runs.
func TestNodeJoinSwapPerJob(t *testing.T) {
	t.Run("with swap", func(t *testing.T) {
		r, _ := taskRig(t, "nightly-sweep", true)
		r.bundle["remote.toml"] += "\n[machine]\nswap = \"2G\"\n"
		log := swapRecorder(t)
		w := r.wake(t)
		if err := waitJoin(t, w.joined); err != nil {
			t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
		}
		got := log()
		if len(got) != 8 {
			t.Fatalf("argv = %v, want the swap made, the wake, then the swap removed", got)
		}
		file := got[1][3]
		want := [][]string{
			{"mkdir", "-p", remotenode.SwapDir},
			{"fallocate", "-l", "2G", file},
			{"chmod", "600", file},
			{"mkswap", file},
			{"swapon", file},
			{"wake"},
			{"swapoff", file},
			{"rm", "-f", file},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("argv = %v, want %v", got, want)
		}
	})
	t.Run("without swap", func(t *testing.T) {
		r, _ := taskRig(t, "nightly-sweep", true)
		log := swapRecorder(t)
		w := r.wake(t)
		if err := waitJoin(t, w.joined); err != nil {
			t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
		}
		if got := log(); !reflect.DeepEqual(got, [][]string{{"wake"}}) {
			t.Errorf("argv = %v, want only the wake", got)
		}
	})
}
