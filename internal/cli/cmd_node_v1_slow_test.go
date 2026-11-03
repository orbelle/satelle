//go:build clislow && !windows

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
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// AC2: a node joined on a v1 task definition runs it end to end against the
// fake node API: on_create then post_start run in order, every [secrets] name
// and override is read once at join by its key, the checkout is exactly the
// declared [[repo]], and the task runs and concludes accept with the keys in its
// environment.
func TestSlowNodeJoinRunsV1TaskDefinition(t *testing.T) {
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

// AC5: [machine] swap is carried unchanged to join, which makes a fresh
// swapfile of that size for the job and removes it after; with no swap nothing
// of it runs.
func TestSlowNodeJoinSwapPerJob(t *testing.T) {
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
