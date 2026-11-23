//go:build integration && !windows

package tests

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const secretName = "SATELLE_PROBE_SECRET"

const gitShimScript = `#!/bin/sh
echo "$*" >> '@LOG@/git-argv.log'
case "$(pwd) $*" in
  *'@NODE@'*) ;;
  *) { echo "== $(pwd) $*"; env; } >> '@LOG@/git-env.log' ;;
esac
exec '@GIT@' "$@"
`

type secretBox struct {
	s     *sandbox
	task  string
	fake  string
	logs  string
	value string
}

func randomSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func secretSandbox(t *testing.T, withFake bool, edit func(def string) string) *secretBox {
	t.Helper()
	s, _, fake := dockerLocalSandbox(t, withFake)
	for _, rel := range []string{
		"workflows/secret-probe.toml",
		"skills/satelle-probe-secret-check.md",
		"skills/satelle-change-judged-check.md",
		"remotes/docker-secret-probe/satelle.toml",
	} {
		b, err := os.ReadFile(filepath.Join("..", ".satelle", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		s.write(".satelle/"+rel, string(b))
	}
	task := s.create("task", "--title", "Probe a docker-local secret", "--input-kind", "story", "--input-tag", "secret-probe").ID
	def, err := os.ReadFile(filepath.Join("..", ".satelle", "remotes", "docker-secret-probe", "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	pinned := regexp.MustCompile(`(?m)^task = ".*"$`).ReplaceAllString(string(def), `task = "`+task+`"`)
	if edit != nil {
		pinned = edit(pinned)
	}
	s.write(".satelle/remotes/docker-secret-probe/remote.toml", pinned)
	s.git("add", ".satelle")
	s.git("commit", "-q", "-m", "secret probe")

	logs := t.TempDir()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	node := fake
	if node == "" {
		node = "/nonexistent-satelle-node-dir"
	}
	writeFile(t, filepath.Join(shimDir, "git"), strings.NewReplacer("@LOG@", logs, "@GIT@", realGit, "@NODE@", node).Replace(gitShimScript), 0o755)
	s.path = append([]string{shimDir}, s.path...)
	return &secretBox{s: s, task: task, fake: fake, logs: logs, value: randomSecret(t)}
}

func (b *secretBox) fire() result {
	b.s.t.Helper()
	b.s.env[secretName] = b.value
	defer delete(b.s.env, secretName)
	return b.s.run("remote", "fire", "docker-secret-probe", b.task)
}

func filesHolding(t *testing.T, root, value string) []string {
	t.Helper()
	var hits []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), value) {
			hits = append(hits, p)
		}
		return nil
	})
	return hits
}

func (b *secretBox) assertNowhere(t *testing.T, roots map[string]string) {
	t.Helper()
	for what, root := range roots {
		if root == "" {
			continue
		}
		if hits := filesHolding(t, root, b.value); len(hits) > 0 {
			t.Errorf("the value is in %s: %v", what, hits)
		}
	}
}

func (b *secretBox) assertEnvLogsClean(t *testing.T, envLogs ...string) {
	t.Helper()
	for _, p := range envLogs {
		got, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(got), b.value) || strings.Contains(string(got), secretName+"=") {
			var ctx []string
			for _, l := range strings.Split(string(got), "\n") {
				if strings.HasPrefix(l, "== ") || strings.Contains(l, secretName+"=") {
					ctx = append(ctx, l)
				}
			}
			t.Errorf("%s carries the secret's name or value:\n%s", p, strings.Join(ctx, "\n"))
		}
	}
}

func (b *secretBox) probeNotes(t *testing.T) string {
	t.Helper()
	execs := executionsOf(b.s, b.task)
	if len(execs) != 1 {
		t.Fatalf("executions of %s = %+v, want one", b.task, execs)
	}
	var notes []string
	for _, e := range chainOf(b.s, execs[0].ID) {
		if e.Kind == "gate.run" && gateOf(t, e) == "secret-probe" {
			notes = append(notes, payloadField(t, e, "notes"))
		}
	}
	if len(notes) != 1 {
		t.Fatalf("the execution has %d gate.run events of secret-probe", len(notes))
	}
	return notes[0]
}

func (b *secretBox) assertProbe(t *testing.T, r result) string {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("fire: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if strings.Contains(r.out+r.err, b.value) {
		t.Error("the fire's output holds the value")
	}
	notes := b.probeNotes(t)
	sum := sha256.Sum256([]byte(b.value))
	want := hex.EncodeToString(sum[:])
	for _, line := range []string{"step sha256:" + want, "setup sha256:" + want, "argv clean: "} {
		if !strings.Contains(notes, line) {
			t.Errorf("the probe's notes miss %q:\n%s", line, notes)
		}
	}
	if strings.Contains(notes, b.value) {
		t.Error("the probe's notes hold the value")
	}
	ledger := b.s.must("ledger", "list", executionsOf(b.s, b.task)[0].ID, "--json") + b.s.must("ledger", "list", b.task, "--json")
	if strings.Contains(ledger, b.value) {
		t.Error("ledger list holds the value")
	}
	st := b.s.must("remote", "status", "docker-secret-probe", b.task, "--json")
	if strings.Contains(st, b.value) || !strings.Contains(st, "accept") {
		t.Errorf("remote status:\n%s", st)
	}
	return notes
}

func TestDockerLocalFireSecretProbe(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	b.assertProbe(t, b.fire())
	log, err := os.ReadFile(filepath.Join(b.fake, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^exec -i satelle-node-nod_[0-9a-f]+ satelle node run-local`).Match(log) {
		t.Fatalf("the node was not run over stdin:\n%s", log)
	}
	if strings.Contains(string(log), "sh -c") {
		t.Fatalf("setup ran through docker exec argv, not in the node:\n%s", log)
	}
	b.assertNowhere(t, map[string]string{
		"the sandbox (definition, ledger, tree)": b.s.dir,
		"the home (runtime dir, run record)":     b.s.home,
		"the docker argv log":                    filepath.Join(b.fake, "log"),
		"the git argv log":                       filepath.Join(b.logs, "git-argv.log"),
	})
	nodes, _ := filepath.Glob(filepath.Join(b.fake, "satelle-node-*", "node"))
	for _, n := range nodes {
		b.assertNowhere(t, map[string]string{"the node's directories": n})
	}
	b.assertEnvLogsClean(t, filepath.Join(b.fake, "docker-env.log"), filepath.Join(b.logs, "git-env.log"))
	if _, err := os.Stat(filepath.Join(b.logs, "git-env.log")); err != nil {
		t.Fatal("the git shim never ran: the git children were not observed")
	}
}

func TestDockerLocalFireSecretUnset(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	r := b.s.run("remote", "fire", "docker-secret-probe", b.task)
	if r.code == 0 || !strings.Contains(r.err, secretName) || !strings.Contains(r.err, "not set in the firing shell's environment or in .satelle/.env") {
		t.Fatalf("fire with the secret unset: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if log, _ := os.ReadFile(filepath.Join(b.fake, "log")); strings.Contains(string(log), "run -d") || strings.Contains(string(log), "build") {
		t.Fatalf("docker ran for a refused fire:\n%s", log)
	}
}

func TestDockerLocalFireSecretLeakRefused(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	b.s.env["FAKE_LEAK_BLOB"] = "a step wrote " + b.value
	r := b.fire()
	delete(b.s.env, "FAKE_LEAK_BLOB")
	if r.code == 0 {
		t.Fatalf("a fire whose record carries the value exited 0:\n%s", r.out)
	}
	st := b.s.must("remote", "status", "docker-secret-probe", b.task)
	if !strings.Contains(st, "verdict: reject") || !strings.Contains(st, "declared secret "+secretName) || !strings.Contains(st, "nothing of the run was imported") {
		t.Fatalf("status:\n%s", st)
	}
	if execs := executionsOf(b.s, b.task); len(execs) != 0 {
		t.Fatalf("executions imported from a leaking run: %+v", execs)
	}
	b.assertNowhere(t, map[string]string{"the sandbox": b.s.dir, "the home": b.s.home})
}

func TestDockerLocalFireSecretEchoRedacted(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	skill := filepath.Join(b.s.dir, ".satelle", "skills", "satelle-probe-secret-check.md")
	body, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	echo := strings.Replace(string(body), "set -u\n", "set -u\nprintf 'the step read %s\\n' \"$SATELLE_PROBE_SECRET\"\n", 1)
	b.s.write(".satelle/skills/satelle-probe-secret-check.md", echo)
	b.s.git("commit", "-q", "-am", "echo the secret")
	notes := b.assertProbe(t, b.fire())
	if !strings.Contains(notes, "the step read [secret]") {
		t.Fatalf("the echoed value was not redacted at the source:\n%s", notes)
	}
	b.assertNowhere(t, map[string]string{"the sandbox": b.s.dir, "the home": b.s.home})
}

func TestDockerLocalFireSecretRedactsFailure(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, func(def string) string {
		return regexp.MustCompile(`(?m)^on_create = .*$`).ReplaceAllLiteralString(def, `on_create = ["printf 'setup saw %s\\n' \"$SATELLE_PROBE_SECRET\"; exit 1"]`)
	})
	r := b.fire()
	if r.code == 0 {
		t.Fatalf("a fire whose setup fails exited 0:\n%s", r.out)
	}
	if strings.Contains(r.out+r.err, b.value) {
		t.Error("the fire's output holds the value")
	}
	st := b.s.must("remote", "status", "docker-secret-probe", b.task)
	if !strings.Contains(st, "[secret]") || strings.Contains(st, b.value) {
		t.Fatalf("status:\n%s", st)
	}
	b.assertNowhere(t, map[string]string{"the sandbox": b.s.dir, "the home": b.s.home})
}

func TestDockerLocalFireSecretOnDaemon(t *testing.T) {
	real, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker is not installed")
	}
	if out, err := exec.Command(real, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unavailable: %v: %s", err, out)
	}
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	b := secretSandbox(t, false, nil)
	gate := daemonGate(t, b, real, repo)
	writeFile(t, filepath.Join(gate, "hold-rm"), "", 0o644)
	other := "SATELLE_SECRET_TEST_OTHER_" + randomSecret(t)[:8]
	b.s.env[other] = "x"
	var name string
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(gate, "rm-release"), nil, 0o644)
		if name != "" {
			_ = exec.Command(real, "rm", "-f", name).Run()
		}
	})
	f := b.startFire(t)
	f.waitFor(t, filepath.Join(gate, "removing"))
	if m := nodeLine.FindStringSubmatch(f.stdout.String()); m != nil {
		name = m[2]
	}
	if name == "" {
		t.Fatalf("no container named:\n%s", f.stdout.String())
	}
	for what, args := range map[string][]string{
		"docker inspect of the container": {"inspect", name},
		"docker image inspect":            {"image", "inspect", "satelle-node:local"},
		"docker history":                  {"history", "--no-trunc", "satelle-node:local"},
	} {
		out, err := exec.Command(real, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", what, err, out)
		}
		if strings.Contains(string(out), b.value) {
			t.Errorf("the value is in %s", what)
		}
	}
	nodeCopy := t.TempDir()
	if out, err := exec.Command(real, "cp", name+":/node", nodeCopy).CombinedOutput(); err != nil {
		t.Fatalf("docker cp: %v: %s", err, out)
	}
	writeFile(t, filepath.Join(gate, "rm-release"), "", 0o644)
	r := f.wait(t)
	notes := b.assertProbe(t, r)
	t.Logf("probe notes:\n%s", notes)
	names := ""
	for _, l := range strings.Split(notes, "\n") {
		if strings.HasPrefix(l, "env names: ") {
			names = l
		}
	}
	if !strings.Contains(names, secretName) || strings.Contains(names, other) {
		t.Errorf("the node's environment names: %q", names)
	}
	assertNoNodeOnDaemon(t, real, nodeLine.FindStringSubmatch(r.out)[1])
	b.assertNowhere(t, map[string]string{
		"the sandbox (definition, ledger, tree)": b.s.dir,
		"the home (runtime dir, run record)":     b.s.home,
		"the docker argv log":                    filepath.Join(gate, "log"),
		"the git argv log":                       filepath.Join(b.logs, "git-argv.log"),
		"the node's directories":                 nodeCopy,
	})
	b.assertEnvLogsClean(t, filepath.Join(gate, "docker-env.log"), filepath.Join(b.logs, "git-env.log"))
}

func daemonGate(t *testing.T, b *secretBox, real, repo string) string {
	t.Helper()
	gate, shimDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(gate, "release"), "", 0o644)
	shim := strings.NewReplacer("@GATE@", gate, "@REAL@", real, "@REPO@", repo, "@SANDBOX@", b.s.dir).Replace(gateShim)
	writeFile(t, filepath.Join(shimDir, "docker"), shim, 0o755)
	b.s.path = append([]string{shimDir}, b.s.path...)
	return gate
}

func assertNoNodeOnDaemon(t *testing.T, real, node string) {
	t.Helper()
	for what, args := range map[string][]string{
		"container": {"ps", "-a", "-q", "--filter", "label=satelle.node=" + node},
		"network":   {"network", "ls", "-q", "--filter", "label=satelle.node=" + node},
	} {
		out, err := exec.Command(real, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Errorf("a %s labelled satelle.node=%s remains: %s", what, node, out)
		}
	}
}

type asyncFire struct {
	cmd            *exec.Cmd
	exited         chan error
	stdout, stderr *lockedBuffer
}

func (b *secretBox) startFire(t *testing.T) *asyncFire {
	t.Helper()
	f := &asyncFire{exited: make(chan error, 1), stdout: &lockedBuffer{}, stderr: &lockedBuffer{}}
	f.cmd = exec.Command(satelleBin, "remote", "fire", "docker-secret-probe", b.task)
	f.cmd.Dir, f.cmd.Stdout, f.cmd.Stderr = b.s.dir, f.stdout, f.stderr
	f.cmd.Env = append(b.s.environ(), secretName+"="+b.value)
	if err := f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { f.exited <- f.cmd.Wait() }()
	t.Cleanup(func() { _ = f.cmd.Process.Kill() })
	return f
}

func (f *asyncFire) waitFor(t *testing.T, file string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	if d, ok := t.Deadline(); ok {
		deadline = d.Add(-30 * time.Second)
	}
	for {
		if _, err := os.Stat(file); err == nil {
			return
		}
		select {
		case err := <-f.exited:
			f.exited <- err
			t.Fatalf("the fire returned before %s: %v\n%s%s", file, err, f.stdout.String(), f.stderr.String())
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s before the deadline\n%s%s", file, f.stdout.String(), f.stderr.String())
		}
	}
}

func (f *asyncFire) wait(t *testing.T) result {
	t.Helper()
	err := <-f.exited
	r := result{out: f.stdout.String(), err: f.stderr.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return r
}

func labelledNodes(t *testing.T, fake, node string) []string {
	t.Helper()
	labels, _ := filepath.Glob(filepath.Join(fake, "*", ".label"))
	var left []string
	for _, l := range labels {
		if b, err := os.ReadFile(l); err == nil && strings.TrimSpace(string(b)) == "satelle.node="+node {
			left = append(left, filepath.Dir(l))
		}
	}
	return left
}

func (b *secretBox) assertNodeRemoved(t *testing.T, r result) {
	t.Helper()
	if r.code == 0 {
		t.Fatalf("a failed fire exited 0:\n%s", r.out)
	}
	m := nodeLine.FindStringSubmatch(r.out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s%s", r.out, r.err)
	}
	assertRemoved(t, b.fake, m[2], r.out)
	if strings.Contains(r.out, "left to inspect") {
		t.Errorf("a node that was given a secret was left to inspect:\n%s", r.out)
	}
	if left := labelledNodes(t, b.fake, m[1]); len(left) != 0 {
		t.Errorf("containers labelled with the node remain: %v", left)
	}
	if st := b.s.must("remote", "status", "docker-secret-probe", b.task); !strings.Contains(st, ": stopped (node "+m[1]+")") {
		t.Errorf("status:\n%s", st)
	}
	if strings.Contains(r.out+r.err, b.value) {
		t.Error("the fire's output holds the value")
	}
}

func TestDockerLocalFireSecretRemovedOnFailure(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	writeFile(t, filepath.Join(b.fake, "fail-run-local"), "", 0o644)
	b.assertNodeRemoved(t, b.fire())
}

func TestDockerLocalFireSecretRemovedOnInterrupt(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	writeFile(t, filepath.Join(b.fake, "hold-run-local"), "", 0o644)
	f := b.startFire(t)
	f.waitFor(t, filepath.Join(b.fake, "held"))
	if err := f.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	b.assertNodeRemoved(t, f.wait(t))
}

func TestDockerLocalSecretJobScopeRefused(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, func(def string) string {
		return strings.Replace(def, `scope = "run"`, `scope = "job"`, 1)
	})
	names := func(what, s, where string) {
		t.Helper()
		for _, want := range []string{where, "lifecycle.scope", "[lifecycle] scope", "[secrets]"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s misses %q:\n%s", what, want, s)
			}
		}
	}
	v := b.s.run("validate")
	if v.code == 0 {
		t.Fatalf("validate accepted [secrets] with scope job:\n%s", v.out)
	}
	names("validate", v.out+v.err, "remotes/docker-secret-probe/remote.toml")
	r := b.fire()
	if r.code == 0 {
		t.Fatalf("the fire accepted [secrets] with scope job:\n%s", r.out)
	}
	names("the fire", r.err, "remote docker-secret-probe: remote.toml")
	if log, _ := os.ReadFile(filepath.Join(b.fake, "log")); strings.Contains(string(log), "run -d") || strings.Contains(string(log), "build") {
		t.Fatalf("docker ran for a refused fire:\n%s", log)
	}
}

func TestDockerLocalFireSecretFailureOnDaemon(t *testing.T) {
	real, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker is not installed")
	}
	if out, err := exec.Command(real, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unavailable: %v: %s", err, out)
	}
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	b := secretSandbox(t, false, func(def string) string {
		return regexp.MustCompile(`(?m)^on_create = .*$`).ReplaceAllLiteralString(def, `on_create = ["exit 1"]`)
	})
	daemonGate(t, b, real, repo)
	r := b.fire()
	m := nodeLine.FindStringSubmatch(r.out)
	if m != nil {
		t.Cleanup(func() { _ = exec.Command(real, "rm", "-f", m[2]).Run() })
	}
	if r.code == 0 || m == nil {
		t.Fatalf("a fire whose setup fails: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if !strings.Contains(r.out, "the container "+m[2]+" is removed") {
		t.Errorf("the fire did not say the container is removed:\n%s", r.out)
	}
	assertNoNodeOnDaemon(t, real, m[1])
}

// repoEnv writes the sandbox's .satelle/.env; ignore commits a .gitignore rule
// for .env first.
func (b *secretBox) repoEnv(t *testing.T, ignore bool, body string) {
	t.Helper()
	if ignore {
		b.s.write(".gitignore", ".env\n")
		b.s.git("add", ".gitignore")
		b.s.git("commit", "-q", "-m", "ignore .env")
	}
	b.s.write(".satelle/.env", body)
}

func (b *secretBox) assertRefusedBeforeNode(t *testing.T, r result, wants ...string) {
	t.Helper()
	if r.code == 0 {
		t.Fatalf("the fire was not refused:\n%s", r.out)
	}
	for _, w := range wants {
		if !strings.Contains(r.err, w) {
			t.Errorf("the refusal misses %q:\n%s", w, r.err)
		}
	}
	if log, _ := os.ReadFile(filepath.Join(b.fake, "log")); strings.Contains(string(log), "run -d") || strings.Contains(string(log), "build") {
		t.Fatalf("docker ran for a refused fire:\n%s", log)
	}
}

func TestDockerLocalFireSecretFromRepoEnv(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	other := randomSecret(t)
	b.repoEnv(t, true, "# the probe's secret\n"+secretName+"=\""+b.value+"\"\nOTHER_IN_FILE="+other+"\n")
	notes := b.assertProbe(t, b.s.run("remote", "fire", "docker-secret-probe", b.task))
	if strings.Contains(notes, other) {
		t.Error("an undeclared line of the file reached the node's notes")
	}
	envFile := filepath.Join(b.s.dir, ".satelle", ".env")
	for _, root := range []string{b.s.dir, b.s.home, filepath.Join(b.fake, "log")} {
		for _, hit := range filesHolding(t, root, other) {
			if hit != envFile {
				t.Errorf("an undeclared value of the file is in %s", hit)
			}
		}
	}
	nodes, _ := filepath.Glob(filepath.Join(b.fake, "satelle-node-*", "node"))
	for _, n := range nodes {
		b.assertNowhere(t, map[string]string{"the node's directories": n})
		if hits := filesHolding(t, n, other); len(hits) > 0 {
			t.Errorf("an undeclared value of the file is in the node: %v", hits)
		}
	}
	for _, hit := range filesHolding(t, b.s.dir, b.value) {
		if hit != envFile {
			t.Errorf("the value is in %s", hit)
		}
	}
	b.assertNowhere(t, map[string]string{
		"the home (runtime dir, run record)": b.s.home,
		"the docker argv log":                filepath.Join(b.fake, "log"),
		"the git argv log":                   filepath.Join(b.logs, "git-argv.log"),
	})
	b.assertEnvLogsClean(t, filepath.Join(b.fake, "docker-env.log"), filepath.Join(b.logs, "git-env.log"))
	if _, err := os.Stat(filepath.Join(b.logs, "git-env.log")); err != nil {
		t.Fatal("the git shim never ran: the git children were not observed")
	}
}

func TestDockerLocalFireSecretShellWinsOverRepoEnv(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	decoy := randomSecret(t)
	b.repoEnv(t, true, secretName+"="+decoy+"\n")
	notes := b.assertProbe(t, b.fire())
	sum := sha256.Sum256([]byte(decoy))
	if strings.Contains(notes, hex.EncodeToString(sum[:])) {
		t.Errorf("the node was given the file's value, not the shell's:\n%s", notes)
	}
	b.assertEnvLogsClean(t, filepath.Join(b.fake, "docker-env.log"), filepath.Join(b.logs, "git-env.log"))
	if _, err := os.Stat(filepath.Join(b.logs, "git-env.log")); err != nil {
		t.Fatal("the git shim never ran: the git children were not observed")
	}
}

func TestDockerLocalFireSecretMalformedRepoEnv(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	const bad = "SATELLE_MALFORMED_CONTENT_xyz"
	b.repoEnv(t, true, secretName+"=dummy-value\n"+bad+"\n")
	r := b.s.run("remote", "fire", "docker-secret-probe", b.task)
	b.assertRefusedBeforeNode(t, r, ".satelle/.env", "line 2")
	if strings.Contains(r.out+r.err, bad) {
		t.Errorf("the refusal holds the line's content:\n%s", r.err)
	}
}

func TestDockerLocalFireSecretRepoEnvNotIgnored(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	b.repoEnv(t, false, secretName+"=dummy-value\n")
	if r := b.s.exec("git", "", "check-ignore", "-q", ".satelle/.env"); r.code != 1 {
		t.Fatalf("precondition: git check-ignore .satelle/.env exited %d, want 1 (not ignored)", r.code)
	}
	b.assertRefusedBeforeNode(t, b.s.run("remote", "fire", "docker-secret-probe", b.task), ".satelle/.env", "not ignored")
}

func TestDockerLocalFireSecretRepoEnvTracked(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, nil)
	b.repoEnv(t, true, secretName+"=dummy-value\n")
	b.s.git("add", "-f", ".satelle/.env")
	b.assertRefusedBeforeNode(t, b.s.run("remote", "fire", "docker-secret-probe", b.task), ".satelle/.env", "tracked")
}
