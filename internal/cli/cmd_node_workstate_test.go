//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// recordingPerformers is a task node's performer that records outcome accept with
// an output, as a real performer would.
func recordingPerformers() *taskPerformers {
	return &taskPerformers{step: func(item workitem.Item) error {
		return dispatchInto(context.Background(), "execution-record",
			map[string]any{"id": item.ID, "output": "swept the lot", "outcome": verb.OutcomeAccept}, &struct{}{})
	}}
}

// A concluded task wake pushes its execution, the execution's ledger rows, the
// subject's link row and the run-output doc in one POST to the node's workstate
// route, under the node token, before the concluded heartbeat; the project the
// server answers with is where the fake recorded it.
func TestNodeTaskWakePushesWorkstate(t *testing.T) {
	r, subject := taskRig(t, "nightly-sweep", true)
	setup := r.fakeSetup
	r.fakeSetup = func(f *devtest.Node) {
		setup(f)
		f.Project = "acme-team"
	}
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	fake, joined, spec := w.fake, w.joined, w.spec
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}

	if n := fake.Count(http.MethodPost, "/workstate"); n != 1 {
		t.Fatalf("workstate pushes = %d, want exactly 1\nnode log:\n%s", n, r.log)
	}
	pushed := fake.Pushed()
	if len(pushed) != 1 {
		t.Fatalf("recorded batches = %d, want 1", len(pushed))
	}
	b := pushed[0]
	if b.Auth != "Bearer "+spec.Token {
		t.Errorf("push Authorization = %q, want the node token as bearer", b.Auth)
	}
	if b.Project != "acme-team" {
		t.Errorf("recorded in project %q, want the one the server answered with", b.Project)
	}

	var exec string
	if len(b.Items) != 1 {
		t.Fatalf("items = %d, want exactly the execution", len(b.Items))
	}
	var item struct{ ID, Kind string }
	if err := json.Unmarshal(b.Items[0], &item); err != nil || item.Kind != string(workitem.KindExecution) {
		t.Fatalf("item = %s (%v), want an execution", b.Items[0], err)
	}
	exec = item.ID

	var links, own int
	for _, raw := range b.Ledger {
		var row struct {
			StoryID string          `json:"story_id"`
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		switch {
		case row.Kind == ledger.KindExecutionLink:
			links++
			var p ledger.ExecutionLinkPayload
			if err := json.Unmarshal(row.Payload, &p); err != nil {
				t.Fatal(err)
			}
			want := ledger.ExecutionLinkPayload{Execution: exec, Task: "tsk_nightly-sweep", Remote: spec.Session, Outcome: verb.OutcomeAccept}
			if row.StoryID != subject.ID() || p != want {
				t.Errorf("link row = story %q %+v, want story %q %+v", row.StoryID, p, subject.ID(), want)
			}
		case row.StoryID == exec:
			own++
		default:
			t.Errorf("pushed a ledger row that is neither the execution's nor the link: %s", raw)
		}
	}
	if links != 1 || own == 0 {
		t.Errorf("ledger rows: %d link, %d of the execution, want one link and the execution's own", links, own)
	}

	if len(b.Outputs) != 1 || b.Outputs[0].Execution != exec || b.Outputs[0].Name != "output-"+exec+".md" ||
		!strings.Contains(b.Outputs[0].Body, "swept the lot") {
		t.Errorf("outputs = %+v, want the run-output doc of %s", b.Outputs, exec)
	}

	// Pushed after the run and before it was reported concluded and the token ended.
	var order []string
	for _, c := range fake.Calls() {
		switch {
		case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/workstate"):
			order = append(order, "push")
		case c.Method == http.MethodPut && strings.HasSuffix(c.Path, "/status"):
			order = append(order, "status")
		case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/end"):
			order = append(order, "end")
		}
	}
	got := strings.Join(order, " ")
	if !strings.HasSuffix(got, "push status end") {
		t.Errorf("call order = %s, want the push, then the concluded heartbeat, then end", got)
	}
}

// The push needs no satelle login: the node's home holds no stored credentials and
// no [sync] project is bound, yet the run is pushed, and every request the fake saw
// carried only the node token.
func TestNodeWorkstatePushNeedsNoLogin(t *testing.T) {
	r, _ := taskRig(t, "nightly-sweep", true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	fake, joined, spec := w.fake, w.joined, w.spec
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if n := len(fake.Pushed()); n != 1 {
		t.Fatalf("pushes = %d, want 1 without any login\nnode log:\n%s", n, r.log)
	}
	for _, c := range fake.Calls() {
		if c.Auth != "Bearer "+spec.Token {
			t.Errorf("%s %s carried Authorization %q, want only the node token", c.Method, c.Path, c.Auth)
		}
	}
	// Nothing of a login was stored in the empty home, and the node's own config
	// binds no project.
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("the node wrote %s under its home", p)
		}
		return nil
	})
	if cfg, err := os.ReadFile(filepath.Join(r.nodeRoot, ".satelle", "satelle.toml")); err == nil {
		for _, ln := range strings.Split(string(cfg), "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "project") {
				t.Errorf("the node's config binds a project: %q", ln)
			}
		}
	}
}

// A task node whose root is marked pull-only (sty_07eefabe) runs its task and
// pushes no workstate, saying why.
func TestPullOnlyNodeWorkstatePushSkipped(t *testing.T) {
	r, _ := taskRig(t, "nightly-sweep", true)
	writeRepoFile(t, r.nodeRoot, ".satelle/"+config.LocalConfigName, "[sync]\npull_only = true\n")
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	fake, joined := w.fake, w.joined
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if n := fake.Count(http.MethodPost, "/workstate"); n != 0 {
		t.Errorf("workstate pushes = %d from a pull-only node, want 0\nnode log:\n%s", n, r.log)
	}
	if log := r.log.String(); !strings.Contains(log, "workstate push skipped") || !strings.Contains(log, "pull_only") {
		t.Errorf("node log does not report the skipped push naming pull_only:\n%s", log)
	}
}
