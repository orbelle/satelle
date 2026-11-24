//go:build integration && !windows

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const commandSecretProbe = "command-secret-probe"

var detachedProcess = regexp.MustCompile(`node run started detached as process (\d+)`)

type detachStatus struct {
	State   string `json:"state"`
	Node    string `json:"node"`
	Verdict string `json:"verdict"`
	Notes   string `json:"notes"`
	Reason  string `json:"reason"`
}

func blockingSecretProbe(t *testing.T, sb *secretBox, root string) {
	t.Helper()
	p := filepath.Join(sb.s.dir, ".satelle", "remotes", commandSecretProbe, "remote.toml")
	def, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	wait := "post_start = [\"while [ ! -f '" + root + "/release' ]; do sleep 0.1; done\"]\n"
	body := strings.Replace(string(def), "\n[lifecycle]", "\n"+wait+"\n[lifecycle]", 1)
	if body == string(def) {
		t.Fatalf("no [lifecycle] to put post_start before:\n%s", def)
	}
	sb.s.write(".satelle/remotes/"+commandSecretProbe+"/remote.toml", body)
	sb.s.git("add", ".satelle")
	sb.s.git("commit", "-q", "-m", "a probe that waits for release")
}

func endDetachedChild(t *testing.T, root, node, out string) {
	t.Helper()
	m := detachedProcess.FindStringSubmatch(out)
	if m == nil {
		return
	}
	pid, _ := strconv.Atoi(m[1])
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "release"), nil, 0o644)
		exit := filepath.Join(root, node, "node", "out", "exit")
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if _, err := os.Stat(exit); err == nil || os.IsNotExist(statDir(filepath.Join(root, node))) {
				break
			}
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	})
}

func statDir(p string) error {
	_, err := os.Stat(p)
	return err
}

func (b *secretBox) withValue(args ...string) result {
	b.s.t.Helper()
	b.s.env[secretName] = b.value
	defer delete(b.s.env, secretName)
	return b.s.run(args...)
}

func (b *secretBox) fireDetached(t *testing.T, root string, withValue bool) (node string, r result) {
	t.Helper()
	args := []string{"remote", "fire", commandSecretProbe, b.task, "--detach"}
	if withValue {
		r = b.withValue(args...)
	} else {
		r = b.s.run(args...)
	}
	if r.code != 0 {
		t.Fatalf("fire --detach: exit %d\n%s%s", r.code, r.out, r.err)
	}
	node = nodeOf(t, r.out)
	endDetachedChild(t, root, node, r.out)
	for _, want := range []string{"is running on " + b.task + "; bring it back with satelle remote status", "node run started detached as process",
		commandSecretProbe + " on " + b.task + ": running (node " + node + ")"} {
		if !strings.Contains(r.out, want) {
			t.Fatalf("fire --detach output misses %q:\n%s%s", want, r.out, r.err)
		}
	}
	if strings.Contains(r.out+r.err, b.value) {
		t.Error("the fire's output holds the value")
	}
	return node, r
}

func (b *secretBox) status(t *testing.T, withValue bool, args ...string) (detachStatus, result) {
	t.Helper()
	full := append([]string{"remote", "status", commandSecretProbe, b.task, "--json"}, args...)
	var r result
	if withValue {
		r = b.withValue(full...)
	} else {
		r = b.s.run(full...)
	}
	var st detachStatus
	if r.code == 0 {
		if err := json.Unmarshal([]byte(r.out), &st); err != nil {
			t.Fatalf("remote status --json: %v\n%s", err, r.out)
		}
	}
	if strings.Contains(r.out+r.err, b.value) {
		t.Error("the status's output holds the value")
	}
	return st, r
}

func (b *secretBox) sidecar(t *testing.T) (map[string]any, bool) {
	t.Helper()
	hits, err := filepath.Glob(filepath.Join(b.s.home, ".satelle", "*", "remotes", commandSecretProbe, b.task+".detached.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		return nil, false
	}
	raw, err := os.ReadFile(hits[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m, true
}

func (b *secretBox) storedRun(t *testing.T) detachStatus {
	t.Helper()
	hits, err := filepath.Glob(filepath.Join(b.s.home, ".satelle", "*", "remotes", commandSecretProbe, b.task+".json"))
	if err != nil || len(hits) != 1 {
		t.Fatalf("the run file of %s: %v, %v", b.task, hits, err)
	}
	raw, err := os.ReadFile(hits[0])
	if err != nil {
		t.Fatal(err)
	}
	var st detachStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func (b *secretBox) truncateGitEnv(t *testing.T) string {
	t.Helper()
	p := filepath.Join(b.logs, "git-env.log")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (b *secretBox) assertGitEnvClean(t *testing.T, p string) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(got), "rev-parse") {
		t.Fatalf("git-env.log holds no git child of the command (%v):\n%s", err, got)
	}
	b.assertEnvLogsClean(t, p)
}

func commandLog(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func touch(t *testing.T, p string) {
	t.Helper()
	writeFile(t, p, "", 0o644)
}

func waitConcluded(t *testing.T, root, node string) {
	t.Helper()
	exit := filepath.Join(root, node, "node", "out", "exit")
	for end := time.Now().Add(3 * time.Minute); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(exit); err == nil {
			return
		}
	}
	t.Fatalf("the node %s wrote no exit status", node)
}

func (b *secretBox) assertConcludedAndStopped(t *testing.T, root, node string, st detachStatus, r result) {
	t.Helper()
	if r.code != 0 || st.State != "concluded" || st.Verdict != "accept" || st.Node != node {
		t.Fatalf("status of the concluded run = %+v: exit %d\n%s%s", st, r.code, r.out, r.err)
	}
	notes := b.probeNotes(t)
	for _, line := range []string{"step sha256:", "setup sha256:", "argv clean: "} {
		if !strings.Contains(notes, line) {
			t.Errorf("the probe's notes miss %q:\n%s", line, notes)
		}
	}
	execs := executionsOf(b.s, b.task)
	if len(execs) != 1 || execs[0].Status != "done" {
		t.Fatalf("executions of %s = %+v, want one at done", b.task, execs)
	}
	for _, e := range chainOf(b.s, execs[0].ID) {
		if e.Actor != "node:"+node {
			t.Fatalf("execution event %s by %q, want node:%s", e.Kind, e.Actor, node)
		}
	}
	if v := b.s.must("ledger", "verify"); !strings.Contains(v, "chains intact") {
		t.Fatalf("ledger verify: %s", v)
	}
	assertInOrder(t, commandLog(t, root), "exec "+node+" satelle node run-local --dir /node --detach\n", "stop "+node+"\n")
	if _, err := os.Stat(filepath.Join(root, node)); !os.IsNotExist(err) {
		t.Fatalf("the node's directory is still there: %v", err)
	}
	if _, ok := b.sidecar(t); ok {
		t.Fatal("the detached record is kept after the node was stopped")
	}
}

func (b *secretBox) assertNoValueRecorded(t *testing.T, root string) {
	t.Helper()
	if strings.Contains(commandLog(t, root), b.value) {
		t.Error("a [connect] command's argv holds the value")
	}
	b.assertEnvLogsClean(t, filepath.Join(root, "env.log"), filepath.Join(b.logs, "git-env.log"))
	b.assertNowhere(t, map[string]string{"the fixture node's files": root, "the sandbox": b.s.dir, "the sandbox home": b.s.home,
		"the definition": filepath.Join(b.s.dir, ".satelle", "remotes", commandSecretProbe)})
	exec := executionsOf(b.s, b.task)[0].ID
	read := b.s.must("ledger", "list", exec, "--json") + b.s.must("ledger", "list", b.task, "--json")
	var atts []struct {
		Name string `json:"name"`
	}
	b.s.mustJSON(&atts, "execution", "attachments", exec)
	for _, a := range atts {
		read += b.s.must("execution", "attachment", exec, a.Name)
	}
	if strings.Contains(read, b.value) {
		t.Error("the ledger or the run's attachments hold the value")
	}
}

func TestCommandRemoteDetachSecret(t *testing.T) {
	t.Parallel()
	sb, root, guard := commandSecretSandbox(t)
	blockingSecretProbe(t, sb, root)
	node, _ := sb.fireDetached(t, root, true)
	if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
		t.Fatal("the run was released before the fire returned")
	}
	st, r := sb.status(t, true)
	if r.code != 0 || st.State != "running" || st.Node != node || st.Notes != "" {
		t.Fatalf("status while the node works = %+v: exit %d\n%s", st, r.code, r.err)
	}
	log := commandLog(t, root)
	if !strings.Contains(log, "exec "+node+" satelle node run-local --dir /node --detach\n") || strings.Contains(log, "--progress") {
		t.Fatalf("the node was not started detached without --progress:\n%s", log)
	}
	side, ok := sb.sidecar(t)
	if !ok || !slices.Equal(anyStrings(side["secret_names"]), []string{secretName}) {
		t.Fatalf("the detached record = %v", side)
	}
	sb.assertEnvLogsClean(t, filepath.Join(sb.logs, "git-env.log"))
	if _, err := os.ReadFile(guard); err == nil {
		t.Fatal("docker ran for a command remote")
	}

	gitEnv := sb.truncateGitEnv(t)
	touch(t, filepath.Join(root, "release"))
	st, r = sb.status(t, true, "--wait", "3m")
	sb.assertConcludedAndStopped(t, root, node, st, r)
	sum := sb.probeNotes(t)
	if !strings.Contains(sum, "step sha256:"+sha256Of(sb.value)) || !strings.Contains(sum, "setup sha256:"+sha256Of(sb.value)) {
		t.Fatalf("the value did not reach the run:\n%s", sum)
	}
	sb.assertGitEnvClean(t, gitEnv)
	stops := strings.Count(commandLog(t, root), "stop "+node+"\n")
	again, r := sb.status(t, true)
	if r.code != 0 || again.State != "concluded" || again.Verdict != "accept" || strings.Count(commandLog(t, root), "stop "+node+"\n") != stops {
		t.Fatalf("a second status = %+v: exit %d\n%s%s", again, r.code, r.out, r.err)
	}
	sb.assertNoValueRecorded(t, root)
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func anyStrings(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			s, _ := x.(string)
			out = append(out, s)
		}
	}
	return out
}

func TestCommandRemoteDetachSecretFromRepoEnvAtStatus(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	sb.repoEnv(t, true, secretName+"='"+sb.value+"'\n")
	node, _ := sb.fireDetached(t, root, false)
	waitConcluded(t, root, node)
	st, r := sb.status(t, false)
	sb.assertConcludedAndStopped(t, root, node, st, r)
	if err := os.Remove(filepath.Join(sb.s.dir, ".satelle", ".env")); err != nil {
		t.Fatal(err)
	}
	sb.assertNoValueRecorded(t, root)
}

func TestCommandRemoteDetachStatusRefusesWithoutTheSecret(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	node, _ := sb.fireDetached(t, root, true)
	waitConcluded(t, root, node)
	log := commandLog(t, root)
	for _, args := range [][]string{{"remote", "status", commandSecretProbe, sb.task}, {"remote", "stop", commandSecretProbe, sb.task}} {
		r := sb.s.run(args...)
		if r.code == 0 || !strings.Contains(r.err, "secret "+secretName+" was handed to the node") {
			t.Fatalf("%s without the secret: exit %d\n%s%s", args[1], r.code, r.out, r.err)
		}
		if got := commandLog(t, root); got != log {
			t.Fatalf("%s without the secret ran a [connect] command:\n%s", args[1], strings.TrimPrefix(got, log))
		}
	}
	if _, ok := sb.sidecar(t); !ok {
		t.Fatal("a refused status dropped the detached record")
	}
	if execs := executionsOf(sb.s, sb.task); len(execs) != 0 {
		t.Fatalf("a refused status imported the node's executions: %+v", execs)
	}
	st, r := sb.status(t, true)
	sb.assertConcludedAndStopped(t, root, node, st, r)
}

func TestCommandRemoteDetachStatusTakesTheRecordedNames(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	node, _ := sb.fireDetached(t, root, true)
	waitConcluded(t, root, node)
	const other = "SATELLE_PROBE_OTHER"
	p := filepath.Join(sb.s.dir, ".satelle", "remotes", commandSecretProbe, "remote.toml")
	def, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sb.s.write(".satelle/remotes/"+commandSecretProbe+"/remote.toml", strings.Replace(string(def), `names = ["`+secretName+`"]`, `names = ["`+other+`"]`, 1))
	sb.s.git("add", ".satelle")
	sb.s.git("commit", "-q", "-m", "declare another secret")

	sb.s.env[other] = "other-" + randomSecret(t)
	r := sb.s.run("remote", "status", commandSecretProbe, sb.task)
	delete(sb.s.env, other)
	if r.code == 0 || !strings.Contains(r.err, "secret "+secretName+" was handed to the node") {
		t.Fatalf("status with only the declared name set: exit %d\n%s%s", r.code, r.out, r.err)
	}
	gitEnv := sb.truncateGitEnv(t)
	st, r := sb.status(t, true)
	sb.assertConcludedAndStopped(t, root, node, st, r)
	sb.assertGitEnvClean(t, gitEnv)
	sb.assertNowhere(t, map[string]string{"the fixture node's files": root, "the sandbox": sb.s.dir, "the sandbox home": sb.s.home})
}

func TestCommandRemoteDetachConcludedStopRetried(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	node, _ := sb.fireDetached(t, root, true)
	waitConcluded(t, root, node)
	touch(t, filepath.Join(root, "fail-stop"))
	_, r := sb.status(t, true)
	if r.code == 0 || !strings.Contains(r.err, "[connect] stop") || !strings.Contains(r.err, "stop it with "+fixtureNode+" stop "+node) {
		t.Fatalf("status with a failing stop: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if _, ok := sb.sidecar(t); !ok {
		t.Fatal("the detached record was dropped although the stop failed")
	}
	if stored := sb.storedRun(t); stored.State != "concluded" || stored.Verdict != "accept" {
		t.Fatalf("the recorded run = %+v", stored)
	}
	refire := sb.withValue("remote", "fire", commandSecretProbe, sb.task, "--detach")
	if refire.code == 0 || !strings.Contains(refire.err, "was not stopped; stop it with satelle remote stop") {
		t.Fatalf("a re-fire while the node is not stopped: exit %d\n%s%s", refire.code, refire.out, refire.err)
	}
	if err := os.Remove(filepath.Join(root, "fail-stop")); err != nil {
		t.Fatal(err)
	}
	st, r := sb.status(t, true)
	if r.code != 0 || st.State != "concluded" || st.Verdict != "accept" {
		t.Fatalf("status after the stop works = %+v: exit %d\n%s%s", st, r.code, r.out, r.err)
	}
	if _, err := os.Stat(filepath.Join(root, node)); !os.IsNotExist(err) {
		t.Fatalf("the node's directory is still there: %v", err)
	}
	if _, ok := sb.sidecar(t); ok {
		t.Fatal("the detached record is kept after the node was stopped")
	}
}

func TestCommandRemoteDetachWithoutSecrets(t *testing.T) {
	t.Parallel()
	b := commandSandbox(t, "run")
	r := b.s.run("remote", "fire", "command-task-probe", b.task, "--detach")
	if r.code != 0 {
		t.Fatalf("fire --detach: exit %d\n%s%s", r.code, r.out, r.err)
	}
	node := nodeOf(t, r.out)
	endDetachedChild(t, b.root, node, r.out)
	if !strings.Contains(b.log(t), "exec "+node+" satelle node run-local --dir /node --detach --progress\n") {
		t.Fatalf("the node was not started detached with --progress:\n%s", b.log(t))
	}
	var st detachStatus
	b.s.mustJSON(&st, "remote", "status", "command-task-probe", b.task, "--wait", "3m")
	if st.State != "concluded" || st.Verdict != "accept" || st.Node != node {
		t.Fatalf("status = %+v", st)
	}
	assertInOrder(t, b.log(t), "exec "+node+" satelle node run-local --dir /node --detach --progress\n", "stop "+node+"\n")
	b.assertNoDocker(t)
}

func TestCommandRemoteDetachSecretFailedIsStopped(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	p := filepath.Join(sb.s.dir, ".satelle", "remotes", commandSecretProbe, "remote.toml")
	def, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	failing := regexp.MustCompile(`(?m)^on_create = .*$`).ReplaceAllString(string(def), `on_create = ["echo leaked:$`+secretName+`; false"]`)
	sb.s.write(".satelle/remotes/"+commandSecretProbe+"/remote.toml", failing)
	sb.s.git("add", ".satelle")
	sb.s.git("commit", "-q", "-m", "a setup that prints the value and fails")
	node, _ := sb.fireDetached(t, root, true)
	waitConcluded(t, root, node)
	st, r := sb.status(t, true)
	if r.code != 0 || st.State != "stopped" || !strings.Contains(st.Reason, "the node failed: exit") {
		t.Fatalf("status of a failed node = %+v: exit %d\n%s%s", st, r.code, r.out, r.err)
	}
	if !strings.Contains(commandLog(t, root), "stop "+node+"\n") {
		t.Fatalf("the failed node was not stopped:\n%s", commandLog(t, root))
	}
	if _, err := os.Stat(filepath.Join(root, node)); !os.IsNotExist(err) {
		t.Fatalf("the node's directory is still there: %v", err)
	}
	var task struct {
		Seats []json.RawMessage `json:"seats"`
	}
	sb.s.mustJSON(&task, "task", "get", sb.task)
	if len(task.Seats) != 0 {
		t.Fatalf("seats after the stop = %d", len(task.Seats))
	}
	if strings.Contains(st.Reason, sb.value) {
		t.Error("the reason holds the value")
	}
	sb.assertNowhere(t, map[string]string{"the fixture node's files": root, "the sandbox": sb.s.dir, "the sandbox home": sb.s.home})
}

func waitRunning(t *testing.T, root, node string) {
	t.Helper()
	for end := time.Now().Add(3 * time.Minute); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(root, node, "home", ".satelle-secret-probe-setup.sha256")); err == nil {
			return
		}
	}
	t.Fatalf("the node %s never reached its post_start wait", node)
}

func TestCommandRemoteDetachSecretLostIsStopped(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	blockingSecretProbe(t, sb, root)
	node, _ := sb.fireDetached(t, root, true)
	waitRunning(t, root, node)
	touch(t, filepath.Join(root, "lost"))
	st, r := sb.status(t, true)
	if r.code != 0 || st.State != "stopped" || !strings.Contains(st.Reason, "did not answer") || !strings.Contains(st.Reason, "[connect] stop ran") {
		t.Fatalf("status of a lost node = %+v: exit %d\n%s%s", st, r.code, r.out, r.err)
	}
	if !strings.Contains(commandLog(t, root), "stop "+node+"\n") {
		t.Fatalf("the lost node was not stopped:\n%s", commandLog(t, root))
	}
	if _, err := os.Stat(filepath.Join(root, node)); !os.IsNotExist(err) {
		t.Fatalf("the node's directory is still there: %v", err)
	}
}

func TestCommandRemoteDetachSecretLostStopFails(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	blockingSecretProbe(t, sb, root)
	node, _ := sb.fireDetached(t, root, true)
	waitRunning(t, root, node)
	touch(t, filepath.Join(root, "lost"))
	touch(t, filepath.Join(root, "fail-stop"))
	_, r := sb.status(t, true)
	if r.code == 0 || !strings.Contains(r.err, "stop it with "+fixtureNode+" stop "+node) {
		t.Fatalf("status of a lost node whose stop fails: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if _, ok := sb.sidecar(t); !ok {
		t.Fatal("the detached record was dropped")
	}
	for _, m := range []string{"lost", "fail-stop"} {
		if err := os.Remove(filepath.Join(root, m)); err != nil {
			t.Fatal(err)
		}
	}
	st, r := sb.status(t, true)
	if r.code != 0 || st.State != "running" {
		t.Fatalf("the run after a failed stop = %+v: exit %d\n%s%s", st, r.code, r.out, r.err)
	}
	touch(t, filepath.Join(root, "release"))
	st, r = sb.status(t, true, "--wait", "3m")
	sb.assertConcludedAndStopped(t, root, node, st, r)
}

func TestCommandRemoteDetachStopEndsTheNode(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	blockingSecretProbe(t, sb, root)
	node, _ := sb.fireDetached(t, root, true)
	touch(t, filepath.Join(root, "fail-stop"))
	stop := []string{"remote", "stop", commandSecretProbe, sb.task, "--session", "stop-it"}
	r := sb.withValue(stop...)
	if r.code == 0 || !strings.Contains(r.err, "stop it with "+fixtureNode+" stop "+node) {
		t.Fatalf("remote stop with a failing [connect] stop: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if _, ok := sb.sidecar(t); !ok {
		t.Fatal("a failed stop dropped the detached record")
	}
	if err := os.Remove(filepath.Join(root, "fail-stop")); err != nil {
		t.Fatal(err)
	}
	gitEnv := sb.truncateGitEnv(t)
	r = sb.withValue(stop...)
	if r.code != 0 || !strings.Contains(r.out, "stopped (node "+node+")") || !strings.Contains(r.out, "stopped by stop-it with satelle remote stop") {
		t.Fatalf("remote stop: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if strings.Contains(r.out+r.err, sb.value) {
		t.Error("the stop's output holds the value")
	}
	if !strings.Contains(commandLog(t, root), "stop "+node+"\n") {
		t.Fatalf("[connect] stop did not run:\n%s", commandLog(t, root))
	}
	if _, err := os.Stat(filepath.Join(root, node)); !os.IsNotExist(err) {
		t.Fatalf("the node's directory is still there: %v", err)
	}
	if _, ok := sb.sidecar(t); ok {
		t.Fatal("the detached record is kept after the stop")
	}
	sb.assertGitEnvClean(t, gitEnv)
	again, _ := sb.fireDetached(t, root, true)
	if again == node {
		t.Fatal("the re-fire reused the stopped node")
	}
}

func TestCommandRemoteDetachRefireRefused(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	blockingSecretProbe(t, sb, root)
	node, _ := sb.fireDetached(t, root, true)
	r := sb.withValue("remote", "fire", commandSecretProbe, sb.task, "--detach")
	if r.code == 0 || !strings.Contains(r.err, "is running detached on node "+node) {
		t.Fatalf("a second fire while the run goes: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestCommandRemoteDetachSecretNoProgressRead(t *testing.T) {
	t.Parallel()
	sb, root, _ := commandSecretSandbox(t)
	blockingSecretProbe(t, sb, root)
	node, _ := sb.fireDetached(t, root, true)
	writeFile(t, filepath.Join(root, node, "node", "out", "progress"), sb.value+"\n", 0o644)
	st, r := sb.status(t, true)
	if r.code != 0 || st.State != "running" || st.Notes != "" {
		t.Fatalf("status of a secret run = %+v: exit %d\n%s%s", st, r.code, r.out, r.err)
	}
	if strings.Contains(commandLog(t, root), "cat /node/out/progress") {
		t.Fatalf("the host read the node's progress:\n%s", commandLog(t, root))
	}
}
