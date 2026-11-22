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
	if r.code == 0 || !strings.Contains(r.err, secretName) || !strings.Contains(r.err, "not set in the firing shell's environment") {
		t.Fatalf("fire with the secret unset: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if log, _ := os.ReadFile(filepath.Join(b.fake, "log")); strings.Contains(string(log), "run -d") || strings.Contains(string(log), "build") {
		t.Fatalf("docker ran for a refused fire:\n%s", log)
	}
}

func TestDockerLocalFireSecretWithAllowRefused(t *testing.T) {
	t.Parallel()
	b := secretSandbox(t, true, func(def string) string { return def + "\n[network]\nallow = [\"example.com\"]\n" })
	r := b.fire()
	if r.code == 0 || !strings.Contains(r.err, "may allow no host") || !strings.Contains(r.err, "not enforced on docker") {
		t.Fatalf("fire with a secret and an allow entry: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if log, _ := os.ReadFile(filepath.Join(b.fake, "log")); strings.Contains(string(log), "run -d") {
		t.Fatalf("a container started for a refused fire:\n%s", log)
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
	b := secretSandbox(t, false, func(def string) string {
		return strings.Replace(def, `scope = "run"`, `scope = "job"`, 1)
	})
	gate, shimDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(gate, "release"), "", 0o644)
	shim := strings.NewReplacer("@GATE@", gate, "@REAL@", real, "@REPO@", repo, "@SANDBOX@", b.s.dir).Replace(gateShim)
	writeFile(t, filepath.Join(shimDir, "docker"), shim, 0o755)
	b.s.path = append([]string{shimDir}, b.s.path...)
	other := "SATELLE_SECRET_TEST_OTHER_" + randomSecret(t)[:8]
	b.s.env[other] = "x"
	var name string
	t.Cleanup(func() {
		if name != "" {
			_ = exec.Command(real, "rm", "-f", name).Run()
		}
	})
	r := b.fire()
	if m := nodeLine.FindStringSubmatch(r.out); m != nil {
		name = m[2]
	}
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
	if name == "" {
		t.Fatal("no container named")
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
	b.assertNowhere(t, map[string]string{
		"the sandbox (definition, ledger, tree)": b.s.dir,
		"the home (runtime dir, run record)":     b.s.home,
		"the docker argv log":                    filepath.Join(gate, "log"),
		"the git argv log":                       filepath.Join(b.logs, "git-argv.log"),
		"the node's directories":                 nodeCopy,
	})
	b.assertEnvLogsClean(t, filepath.Join(gate, "docker-env.log"), filepath.Join(b.logs, "git-env.log"))
}
