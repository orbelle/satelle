//go:build !windows

package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

// setupLines matches the [setup] command lists of the rig's definition.
var setupLines = regexp.MustCompile(`(?m)^(on_create|post_start) = .*\n`)

// tomlList is cmds as a TOML array of strings.
func tomlList(cmds []string) string {
	quoted := make([]string, len(cmds))
	for i, c := range cmds {
		quoted[i] = strconv.Quote(c)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// setSetup makes the definition the rig serves declare onCreate and postStart
// as its [setup] commands; nil declares none.
func (r *nodeRig) setSetup(onCreate, postStart []string) {
	r.t.Helper()
	cur := setupLines.ReplaceAllString(r.bundle["remote.toml"], "")
	var lines string
	if len(onCreate) > 0 {
		lines += "on_create = " + tomlList(onCreate) + "\n"
	}
	if len(postStart) > 0 {
		lines += "post_start = " + tomlList(postStart) + "\n"
	}
	if lines != "" {
		if strings.Contains(cur, "\n[setup]\n") {
			cur = strings.Replace(cur, "\n[setup]\n", "\n[setup]\n"+lines, 1)
		} else {
			cur += "\n[setup]\n" + lines
		}
	}
	r.bundle["remote.toml"] = cur
}

// addOverride makes the definition the rig serves declare key in
// [secrets.override] with value, a TOML value as written.
func (r *nodeRig) addOverride(key, value string) {
	r.t.Helper()
	line := key + " = " + value + "\n"
	cur := r.bundle["remote.toml"]
	if strings.Contains(cur, "\n[secrets.override]\n") {
		cur = strings.Replace(cur, "\n[secrets.override]\n", "\n[secrets.override]\n"+line, 1)
	} else {
		cur += "\n[secrets.override]\n" + line
	}
	r.bundle["remote.toml"] = cur
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

// The definition's [setup] commands run once each, on_create then post_start,
// in order, in the node root, after the definition is installed and before the
// node reads its run, joins the relay or reports ready; the node then works as
// it does with no setup.
func TestNodeJoinRunsTheSetupInOrderBeforeTheRun(t *testing.T) {
	r := newNodeRig(t)
	scratch := t.TempDir()
	order, started, release := filepath.Join(scratch, "order"), filepath.Join(scratch, "started"), filepath.Join(scratch, "release")
	r.setSetup([]string{
		// The definition is installed already, and the command runs in the node root.
		"test -f .satelle/workflows/step.toml && echo one >> " + order + " && : > " + started +
			" && while [ ! -e " + release + " ]; do sleep 0.02; done",
	}, []string{"echo two >> " + order})
	fakes := make(chan *devtest.Node, 1)
	r.fakeSetup = func(f *devtest.Node) { fakes <- f }

	type result struct {
		run    *remotenode.Run
		joined chan error
		err    error
	}
	opened := make(chan result, 1)
	go func() {
		run, _, joined, _, err := r.tryOpen(t)
		opened <- result{run, joined, err}
	}()
	fake := <-fakes
	deadline := time.Now().Add(30 * time.Second)
	for !fileExists(started) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !fileExists(started) {
		t.Fatalf("the first setup command never ran\nnode log:\n%s", r.log)
	}
	// While the first command runs the node has done nothing but fetch its definition.
	if got := fake.Count(http.MethodGet, "/definition"); got != 1 {
		t.Errorf("definition fetches = %d, want 1", got)
	}
	for _, tail := range []string{"/run", "/end"} {
		for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPost} {
			if n := fake.Count(m, tail); n != 0 {
				t.Errorf("%s %s called %d times before the setup finished", m, tail, n)
			}
		}
	}
	// Only the node's own heartbeat: no status reports a run while it sets up.
	for _, s := range fake.Statuses() {
		if s["run_state"] != "" || s["outcome"] != "" {
			t.Errorf("status %v reports a run during the setup", s)
		}
	}
	if got := readFile(t, order); got != "one\n" {
		t.Errorf("order = %q while the first command runs, want only it", got)
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res := <-opened
	if res.err != nil {
		t.Fatalf("Attach: %v\nnode log:\n%s", res.err, r.log)
	}
	// End the node and wait for its join, so it does not outlive the test.
	if err := res.run.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	if err := waitJoin(t, res.joined); err != nil {
		t.Fatalf("join: %v\nnode log:\n%s", err, r.log)
	}
	if got := readFile(t, order); got != "one\ntwo\n" {
		t.Errorf("order = %q, want each command once, in order", got)
	}
	if n := fake.Count(http.MethodGet, "/definition"); n != 1 {
		t.Errorf("definition fetches = %d, want 1", n)
	}
}

// A failing second command fails the start: the node reports its phase, index
// and reason (and nothing of its output or command) as a concluded, rejected
// status, works no run, and ends once; post_start never runs.
func TestNodeJoinFailingSetupFailsTheStartAndReportsIt(t *testing.T) {
	r := newNodeRig(t)
	scratch := t.TempDir()
	order := filepath.Join(scratch, "order")
	r.setSetup([]string{
		"echo one >> " + order,
		"echo OUTPUT-SENTINEL; echo two >> " + order + "; exit 3 # ARGV-SENTINEL",
		"echo three >> " + order,
	}, []string{"echo post >> " + order})
	fake := r.serve("nod_a", "node-token", r.encode())
	jerr := r.joinNow(r.relay.URL(), "nod_a", "node-token")
	if jerr == nil || !strings.Contains(jerr.Error(), "setup on_create command 2: exit status 3") {
		t.Errorf("join ended with %v, want the failing command's index and reason", jerr)
	}
	if got := readFile(t, order); got != "one\ntwo\n" {
		t.Errorf("order = %q, want the commands up to the failing one", got)
	}

	// The join heartbeat may lead; the one concluded reject is the last status.
	st := fake.Statuses()
	if len(st) == 0 {
		t.Fatal("no status was sent")
	}
	last := st[len(st)-1]
	if last["run_state"] != "concluded" || last["outcome"] != "reject" ||
		!strings.Contains(last["findings"], "setup on_create command 2: exit status 3") {
		t.Fatalf("statuses = %+v, want a concluded reject carrying the failing command last", st)
	}
	for _, s := range st[:len(st)-1] {
		if s["run_state"] != "" {
			t.Errorf("status %v reports a run before the failed start", s)
		}
	}
	for k, v := range last {
		if strings.Contains(v, "SENTINEL") || strings.Contains(v, devtest.SecretValue) {
			t.Errorf("status %s = %q holds output, argv or a secret", k, v)
		}
	}
	if strings.Contains(last["findings"], "echo") {
		t.Errorf("findings = %q names the command", last["findings"])
	}
	// No run was worked: the relay was never joined, no workstate, no channel key.
	if r.relay.Attempts() != 0 {
		t.Error("the node joined the relay after a failed setup")
	}
	if n := len(fake.Pushed()); n != 0 {
		t.Errorf("workstate pushes = %d, want none", n)
	}
	if fake.ChannelKey() != nil {
		t.Error("the node published a channel key after a failed setup")
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	if strings.Contains(r.log.String(), devtest.SecretValue) {
		t.Error("the secret value is in the node's log")
	}
}

// A definition with no setup commands starts as it always has.
func TestNodeJoinWithNoSetupStartsUnchanged(t *testing.T) {
	r := newNodeRig(t)
	r.setSetup(nil, nil)
	if setupLines.MatchString(r.bundle["remote.toml"]) {
		t.Fatal("the definition still declares setup commands")
	}
	run, fake, joined, _ := r.open(t)
	if run.Report.Error != "" {
		t.Errorf("report = %+v", run.Report)
	}
	if err := run.End(); err != nil {
		t.Errorf("End: %v", err)
	}
	if err := waitJoin(t, joined); err != nil {
		t.Errorf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	// The node's own heartbeat is not a start failure: no status concludes a run.
	for _, s := range fake.Statuses() {
		if s["run_state"] != "" || s["outcome"] != "" {
			t.Errorf("status %v reports a run from a start that did not fail", s)
		}
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
}
