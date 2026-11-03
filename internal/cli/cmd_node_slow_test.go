//go:build clislow && !windows

package cli

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_2dabc0f6 AC1 and AC3: a woken task node reads every secret its definition
// declares at join, a github-app minted token and a from = "env" team secret
// alike, each exactly once, and holds both in its process environment, so the
// task drive's performer (and any process it starts) inherits them. The join log
// names each key and its source and never carries a value.
func TestSlowNodeTaskWakeReadsEverySecretItDeclaresAtJoin(t *testing.T) {
	r, _, _, _ := ghAppTaskRig(t)
	r.addOverride("CLAUDE_CODE_OAUTH_TOKEN", claudeTeamOverride)
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN"} {
		if _, had := os.LookupEnv(k); had {
			t.Setenv(k, "")
			_ = os.Unsetenv(k)
		}
	}

	var (
		mu                 sync.Mutex
		ran                bool
		inProcess, inChild map[string]string
	)
	useTaskNode(t, &taskPerformers{step: func(item workitem.Item) error {
		mu.Lock()
		defer mu.Unlock()
		ran = true
		inProcess = map[string]string{"GH_TOKEN": os.Getenv("GH_TOKEN"), "CLAUDE_CODE_OAUTH_TOKEN": os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")}
		// A process the performer starts inherits the node's environment.
		out, err := exec.Command("sh", "-c", `printf '%s\n%s' "$GH_TOKEN" "$CLAUDE_CODE_OAUTH_TOKEN"`).Output()
		if err != nil {
			return err
		}
		gh, claude, _ := strings.Cut(string(out), "\n")
		inChild = map[string]string{"GH_TOKEN": gh, "CLAUDE_CODE_OAUTH_TOKEN": claude}
		return dispatchInto(context.Background(), "execution-record",
			map[string]any{"id": item.ID, "output": "read the environment", "outcome": verb.OutcomeAccept}, &struct{}{})
	}}, 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}

	mu.Lock()
	defer mu.Unlock()
	if !ran {
		t.Fatalf("the performer never ran\nnode log:\n%s", r.log)
	}
	want := map[string]string{"GH_TOKEN": ghAppToken, "CLAUDE_CODE_OAUTH_TOKEN": devtest.SecretValue}
	if !reflect.DeepEqual(inProcess, want) {
		t.Errorf("the performer's environment carried %v of the declared secrets, want both", redactedKeys(inProcess, want))
	}
	if !reflect.DeepEqual(inChild, want) {
		t.Errorf("a process the performer started inherited %v of the declared secrets, want both", redactedKeys(inChild, want))
	}
	for key := range want {
		if n := w.fake.Count(http.MethodPost, "/secrets/"+key); n != 1 {
			t.Errorf("%s reads = %d, want exactly 1 (join's)", key, n)
		}
	}

	// AC3: each key is named with its source; no value is in the log.
	log := r.log.String()
	for _, line := range []string{"join: secret CLAUDE_CODE_OAUTH_TOKEN read (satelle:CLAUDE_CODE_OAUTH_TOKEN)", "join: secret GH_TOKEN read (github-app github.com)"} {
		if !strings.Contains(log, line) {
			t.Errorf("the join log lacks %q\nnode log:\n%s", line, log)
		}
	}
	for key, value := range want {
		if strings.Contains(log, value) {
			t.Errorf("the join log carries the value of %s", key)
		}
	}
	if currentNodeProcessSecrets() != nil {
		t.Error("the node still holds its process secrets after the fire")
	}
	if v := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); v != "" {
		t.Error("the node's environment still carries CLAUDE_CODE_OAUTH_TOKEN after the fire")
	}
}

// AC3 and AC7: a definition that does not check out stops the join naming the
// cause, installs nothing, and the node still ends itself exactly once.
func TestSlowNodeJoinWithABadDefinitionInstallsNothingAndEndsOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(r *nodeRig, fake *devtest.Node)
		serve func(r *nodeRig) []byte
		want  string
	}{
		{"digest mismatch", func(_ *nodeRig, f *devtest.Node) { f.ETag = strings.Repeat("0", 64) }, nil, "does not match its ETag"},
		{"another format", nil, func(*nodeRig) []byte { return []byte(`{"format":2,"files":[]}`) }, "unknown bundle format 2"},
		{"a path outside the roots", nil, func(r *nodeRig) []byte { r.bundle["notes/x.md"] = "x"; return r.encode() }, "outside the bundle's roots"},
		{"malformed remote.toml", nil, func(r *nodeRig) []byte { r.bundle["remote.toml"] = "[[secret\n"; return r.encode() }, "remote.toml"},
		{"wrong-typed field", nil, func(r *nodeRig) []byte { r.bundle["remote.toml"] = "[network]\nallow = \"x\"\n"; return r.encode() }, "remote.toml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newNodeRig(t)
			bundle := r.encode()
			if tc.serve != nil {
				bundle = tc.serve(r)
			}
			fake := r.serve("nod_a", "node-token", bundle)
			if tc.setup != nil {
				tc.setup(r, fake)
			}

			err := r.joinNow(r.relay.URL(), "nod_a", "node-token")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("join = %v, want an error containing %q", err, tc.want)
			}
			if n := fake.Count(http.MethodPost, "/end"); n != 1 {
				t.Errorf("end calls = %d, want 1", n)
			}
			if _, serr := os.Stat(filepath.Join(r.nodeRoot, ".satelle", "skills", "remote-work.md")); serr == nil {
				t.Error("a refused definition installed its skills")
			}
			if r.relay.Attempts() != 0 {
				t.Error("the node joined the relay before it had a definition")
			}
		})
	}
}
