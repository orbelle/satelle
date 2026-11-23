//go:build integration && !windows

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const fakeCommandNode = `#!/bin/sh
ROOT='@ROOT@'
echo "$*" >> "$ROOT/log"
{ echo "== $*"; env; } >> "$ROOT/env.log"
cmd=$1; shift
case "$cmd" in
  check)
    [ -f "$ROOT/fail-check" ] && { echo "fixture check failed: $ROOT/fail-check present"; exit 1; }
    exit 0 ;;
  start) mkdir -p "$ROOT/$1/node" "$ROOT/$1/home" ;;
  network)
    cat > "$ROOT/network.$1"
    if [ -f "$ROOT/network-report" ]; then cat "$ROOT/network-report"; else cat "$ROOT/network.$1"; fi ;;
  copy_in)
    dst="$ROOT/$1$3"
    mkdir -p "$(dirname "$dst")" && cp -R "$2" "$dst" ;;
  exec)
    node=$1; shift
    [ -d "$ROOT/$node" ] || { echo "fixture: no node $node" >&2; exit 1; }
    if [ "$1 $2 $3" = "satelle node run-local" ] && [ -f "$ROOT/fail-run-local" ]; then
      cat > /dev/null
      echo "the node broke" >&2
      exit 1
    fi
    n=$#
    for a in "$@"; do
      case "$a" in /node*|/usr/local/bin*) a="$ROOT/$node$a" ;; esac
      set -- "$@" "$a"
    done
    shift $n
    PATH="$ROOT/$node/usr/local/bin:$PATH" HOME="$ROOT/$node/home" exec "$@" ;;
  stop) rm -rf "$ROOT/$1" ;;
  *) echo "fixture: $cmd is not a command" >&2; exit 2 ;;
esac
`

const fixtureNode = "satelle-fixture-node"

const commandConnect = `
[connect]
check = ["satelle-fixture-node", "check"]
start = ["satelle-fixture-node", "start", "{node}"]
network = ["satelle-fixture-node", "network", "{node}"]
copy_in = ["satelle-fixture-node", "copy_in", "{node}", "{src}", "{dst}"]
exec = ["satelle-fixture-node", "exec", "{node}"]
stop = ["satelle-fixture-node", "stop", "{node}"]
`

var commandNodeLine = regexp.MustCompile(`(?m)^node (nod_[0-9a-f]{12}) started by \[connect\] start$`)

type commandBox struct {
	s     *sandbox
	task  string
	root  string
	guard string
}

func commandDefinition(name, task, scope, extra string) string {
	return `version = 1
name = "` + name + `"
provider = "command"
satelle_version = "1.0.21"
` + extra + `
[drive]
kind = "task"
task = "` + task + `"

[setup]
on_create = ["satelle version"]

[lifecycle]
scope = "` + scope + `"
` + commandConnect
}

func installCommandNode(t *testing.T, s *sandbox) (root, guard string) {
	t.Helper()
	root, bin := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(bin, fixtureNode), strings.ReplaceAll(fakeCommandNode, "@ROOT@", root), 0o755)
	guard = filepath.Join(t.TempDir(), "docker.log")
	writeFile(t, filepath.Join(bin, "docker"), "#!/bin/sh\necho \"$*\" >> '"+guard+"'\nexit 1\n", 0o755)
	s.path = append([]string{bin}, s.path...)
	return root, guard
}

func commandSandbox(t *testing.T, scope string) *commandBox {
	t.Helper()
	s, task, _, _ := taskSandbox(t, false)
	overlay, err := os.ReadFile(filepath.Join("..", ".satelle", "remotes", "docker-task-probe", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/remotes/command-task-probe/remote.toml", commandDefinition("command-task-probe", task, scope, ""))
	s.write(".satelle/remotes/command-task-probe/satelle.toml", string(overlay))
	s.write(".satelle/remotes/command-task-fail/remote.toml", commandDefinition("command-task-fail", task, scope, ""))
	s.write(".satelle/remotes/command-task-fail/satelle.toml", "[workflows]\ndefault = \"probe-fail\"\n")
	s.git("add", ".satelle")
	s.git("commit", "-q", "-m", "command remotes")
	root, guard := installCommandNode(t, s)
	return &commandBox{s: s, task: task, root: root, guard: guard}
}

func (b *commandBox) log(t *testing.T) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(b.root, "log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(got)
}

func (b *commandBox) assertNoDocker(t *testing.T) {
	t.Helper()
	if got, err := os.ReadFile(b.guard); err == nil {
		t.Fatalf("docker ran for a command remote:\n%s", got)
	}
}

func nodeOf(t *testing.T, out string) string {
	t.Helper()
	m := commandNodeLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s", out)
	}
	return m[1]
}

func assertInOrder(t *testing.T, log string, lines ...string) {
	t.Helper()
	at := 0
	for _, l := range lines {
		i := strings.Index(log[at:], l)
		if i < 0 {
			t.Fatalf("the fixture log misses %q after offset %d:\n%s", l, at, log)
		}
		at += i + len(l)
	}
}

func TestCommandRemoteFireTask(t *testing.T) {
	t.Parallel()
	b := commandSandbox(t, "run")
	s := b.s
	b.s.mustConfigValid()
	out := s.must("remote", "fire", "command-task-probe", b.task)
	t.Logf("fire:\n%s", out)
	node := nodeOf(t, out)
	if !strings.Contains(out, "command-task-probe on "+b.task+": concluded (node "+node+")") || !strings.Contains(out, "verdict: accept") {
		t.Fatalf("fire output:\n%s", out)
	}
	if !strings.Contains(out, "stop it: "+fixtureNode+" stop "+node+"\n") || !strings.Contains(out, "the node "+node+" is stopped\n") {
		t.Fatalf("fire output names no stop:\n%s", out)
	}
	b.assertNoDocker(t)
	assertInOrder(t, b.log(t),
		"check\n",
		"start "+node+"\n",
		"network "+node+"\n",
		"exec "+node+" uname -m\n",
		"copy_in "+node+" "+satelleBin+" /usr/local/bin/satelle\n",
		"exec "+node+" satelle --version\n",
		"exec "+node+" mkdir -p /node\n",
		"copy_in "+node+" ", "/in /node/in\n",
		"exec "+node+" satelle node run-local --dir /node\n",
		"exec "+node+" tar -C /node/out -cf - outcome.json\n",
		"exec "+node+" tar -C /node/runtime -cf - ledger\n",
		"stop "+node+"\n",
	)
	if _, err := os.Stat(filepath.Join(b.root, node)); !os.IsNotExist(err) {
		t.Fatalf("the node's directory is still there: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(b.root, "network."+node)); err != nil || len(got) != 0 {
		t.Fatalf("the network command of a definition with no allow got %q, %v; want deny-all (empty stdin)", got, err)
	}

	execs := executionsOf(s, b.task)
	if len(execs) != 1 || execs[0].Status != "done" {
		t.Fatalf("executions of %s = %+v, want one at done", b.task, execs)
	}
	gateRuns := 0
	for _, e := range chainOf(s, execs[0].ID) {
		if e.Actor != "node:"+node {
			t.Fatalf("execution event %s by %q, want node:%s", e.Kind, e.Actor, node)
		}
		if e.Kind == "gate.run" && gateOf(t, e) == "probe-version" {
			gateRuns++
		}
	}
	if gateRuns != 1 {
		t.Fatalf("the execution has %d gate.run events of probe-version", gateRuns)
	}
	if v := s.must("ledger", "verify"); !strings.Contains(v, "chains intact") {
		t.Fatalf("ledger verify: %s", v)
	}
	var st struct {
		Node, Verdict string
	}
	s.mustJSON(&st, "remote", "status", "command-task-probe", b.task)
	if st.Node != node || st.Verdict != "accept" {
		t.Fatalf("remote status = %+v, want node %s accept", st, node)
	}

	help := s.must("help", "remotes")
	for _, want := range []string{"## command: reached by the commands its definition names", "never through\na shell", "only on exec's stdin"} {
		if !strings.Contains(help, want) {
			t.Errorf("satelle help remotes misses %q", want)
		}
	}
}

func TestCommandRemoteStopsTheNode(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, remote, marker, verdict, state string
	}{
		{"accepted", "command-task-probe", "", "accept", "concluded"},
		{"rejected", "command-task-fail", "", "reject", "concluded"},
		{"failed", "command-task-probe", "fail-run-local", "", "stopped"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			b := commandSandbox(t, "run")
			if c.marker != "" {
				writeFile(t, filepath.Join(b.root, c.marker), "", 0o644)
			}
			r := b.s.run("remote", "fire", c.remote, b.task)
			if (r.code == 0) != (c.verdict == "accept") {
				t.Fatalf("fire: exit %d\n%s%s", r.code, r.out, r.err)
			}
			node := nodeOf(t, r.out)
			assertInOrder(t, b.log(t), "start "+node+"\n", "exec "+node+" satelle node run-local --dir /node\n", "stop "+node+"\n")
			if _, err := os.Stat(filepath.Join(b.root, node)); !os.IsNotExist(err) {
				t.Fatalf("the node's directory is still there after a %s run: %v", c.name, err)
			}
			if !strings.Contains(r.out, "the node "+node+" is stopped\n") {
				t.Fatalf("fire output:\n%s%s", r.out, r.err)
			}
			var st struct {
				State, Node, Verdict string
			}
			b.s.mustJSON(&st, "remote", "status", c.remote, b.task)
			if st.State != c.state || st.Node != node || st.Verdict != c.verdict {
				t.Fatalf("remote status = %+v, want %s %s", st, c.state, c.verdict)
			}
			b.assertNoDocker(t)
		})
	}
	t.Run("scope job", func(t *testing.T) {
		t.Parallel()
		b := commandSandbox(t, "job")
		out := b.s.must("remote", "fire", "command-task-probe", b.task)
		node := nodeOf(t, out)
		if strings.Contains(b.log(t), "stop "+node) {
			t.Fatalf("stop ran with scope job:\n%s", b.log(t))
		}
		if _, err := os.Stat(filepath.Join(b.root, node, "node")); err != nil {
			t.Fatalf("the node's directory is gone with scope job: %v", err)
		}
		if !strings.Contains(out, "the node "+node+" is left running; stop it with "+fixtureNode+" stop "+node+"\n") {
			t.Fatalf("fire output:\n%s", out)
		}
	})
}

func TestCommandRemoteSecretOnStdin(t *testing.T) {
	t.Parallel()
	sb := secretSandbox(t, false, nil)
	s := sb.s
	def, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "remotes", "docker-secret-probe", "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.NewReplacer(`name = "docker-secret-probe"`, `name = "command-secret-probe"`,
		`provider = "docker-local"`, `provider = "command"`, `scope = "run"`, `scope = "job"`).Replace(string(def)) + commandConnect
	if !strings.Contains(body, `provider = "command"`) || !strings.Contains(body, `scope = "job"`) {
		t.Fatalf("the definition was not rewritten:\n%s", body)
	}
	overlay, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "remotes", "docker-secret-probe", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/remotes/command-secret-probe/remote.toml", body)
	s.write(".satelle/remotes/command-secret-probe/satelle.toml", string(overlay))
	s.git("add", ".satelle")
	s.git("commit", "-q", "-m", "command secret probe")
	root, guard := installCommandNode(t, s)

	s.env[secretName] = sb.value
	r := s.run("remote", "fire", "command-secret-probe", sb.task)
	delete(s.env, secretName)
	if r.code != 0 {
		t.Fatalf("fire: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if strings.Contains(r.out+r.err, sb.value) {
		t.Error("the fire's output holds the value")
	}
	if _, err := os.ReadFile(guard); err == nil {
		t.Fatal("docker ran for a command remote")
	}
	node := nodeOf(t, r.out)
	notes := sb.probeNotes(t)
	sum := sha256.Sum256([]byte(sb.value))
	want := hex.EncodeToString(sum[:])
	for _, line := range []string{"step sha256:" + want, "setup sha256:" + want, "argv clean: "} {
		if !strings.Contains(notes, line) {
			t.Errorf("the probe's notes miss %q:\n%s", line, notes)
		}
	}

	log, err := os.ReadFile(filepath.Join(root, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "exec "+node+" satelle node run-local --dir /node\n") {
		t.Fatalf("the node was not run over exec:\n%s", log)
	}
	if strings.Contains(string(log), sb.value) {
		t.Error("a command's argv holds the value")
	}
	env, err := os.ReadFile(filepath.Join(root, "env.log"))
	if err != nil || !strings.Contains(string(env), "== exec "+node+" satelle node run-local") {
		t.Fatalf("the fixture recorded no environment of the node's run: %v", err)
	}
	sb.assertEnvLogsClean(t, filepath.Join(root, "env.log"))
	setup, err := os.ReadFile(filepath.Join(root, node, "home", ".satelle-secret-probe-setup.sha256"))
	if err != nil || strings.TrimSpace(string(setup)) != want {
		t.Fatalf("the node's setup file = %q, %v", setup, err)
	}
	sb.assertNowhere(t, map[string]string{"the fixture node's files": root, "the sandbox": s.dir, "the sandbox home": s.home})
	ledger := s.must("ledger", "list", executionsOf(s, sb.task)[0].ID, "--json") + s.must("ledger", "list", sb.task, "--json")
	if strings.Contains(ledger, sb.value) {
		t.Error("ledger list holds the value")
	}
}

func TestCommandRemoteCheckRefuses(t *testing.T) {
	t.Parallel()
	b := commandSandbox(t, "run")
	writeFile(t, filepath.Join(b.root, "fail-check"), "", 0o644)
	r := b.s.run("remote", "fire", "command-task-probe", b.task)
	if r.code == 0 {
		t.Fatalf("a fire whose check fails exited 0:\n%s", r.out)
	}
	for _, want := range []string{"[connect] check " + fixtureNode + " check failed", "fixture check failed: " + b.root + "/fail-check present"} {
		if !strings.Contains(r.err, want) {
			t.Fatalf("the refusal misses %q:\n%s", want, r.err)
		}
	}
	if log := b.log(t); log != "check\n" {
		t.Fatalf("a command ran after the check failed:\n%s", log)
	}
	if st := b.s.run("remote", "status", "command-task-probe", b.task); st.code == 0 || !strings.Contains(st.err, "no run of command-task-probe") {
		t.Fatalf("remote status after a refused fire: exit %d\n%s%s", st.code, st.out, st.err)
	}
	if execs := executionsOf(b.s, b.task); len(execs) != 0 {
		t.Fatalf("executions of a refused fire: %+v", execs)
	}
	b.assertNoDocker(t)
}

func TestCommandRemoteDefinitionRefused(t *testing.T) {
	t.Parallel()
	b := commandSandbox(t, "run")
	good := commandDefinition("command-bad", b.task, "run", "")
	const noNetwork = `network = ["satelle-fixture-node", "network", "{node}"]` + "\n"
	for _, c := range []struct {
		key, body string
	}{
		{"connect.start", strings.Replace(good, `start = ["satelle-fixture-node", "start", "{node}"]`+"\n", "", 1)},
		{"connect.exec", strings.Replace(good, `exec = ["satelle-fixture-node", "exec", "{node}"]`+"\n", "", 1)},
		{"connect.stop", strings.Replace(good, `stop = ["satelle-fixture-node", "stop", "{node}"]`+"\n", "", 1)},
		{"connect.network", strings.Replace(good, noNetwork, "", 1)},
		{"connect.network", strings.Replace(commandDefinition("command-bad", b.task, "run", "\n[network]\nallow = [\"example.com\"]\n"), noNetwork, "", 1)},
	} {
		if c.body == good {
			t.Fatalf("%s: the definition was not changed", c.key)
		}
		b.s.write(".satelle/remotes/command-bad/remote.toml", c.body)
		r := b.s.run("validate", "--json")
		var v struct {
			Valid    bool     `json:"valid"`
			Problems []string `json:"problems"`
		}
		if err := json.Unmarshal([]byte(r.out), &v); err != nil {
			t.Fatalf("%s: validate --json: %v\n%s%s", c.key, err, r.out, r.err)
		}
		found := false
		for _, p := range v.Problems {
			found = found || strings.Contains(p, "remotes/command-bad/remote.toml: "+c.key+": ")
		}
		if r.code == 0 || v.Valid || !found {
			t.Fatalf("%s: validate exit %d, valid %v, problems %v", c.key, r.code, v.Valid, v.Problems)
		}
		if r := b.s.run("validate"); r.code == 0 || !strings.Contains(r.out+r.err, "remotes/command-bad/remote.toml: "+c.key) {
			t.Fatalf("%s: validate exit %d\n%s%s", c.key, r.code, r.out, r.err)
		}
		f := b.s.run("remote", "fire", "command-bad", b.task)
		if f.code == 0 || !strings.Contains(f.err, c.key) {
			t.Fatalf("%s: fire exit %d\n%s%s", c.key, f.code, f.out, f.err)
		}
		if log := b.log(t); log != "" {
			t.Fatalf("%s: a command ran for a refused definition:\n%s", c.key, log)
		}
	}
	b.assertNoDocker(t)
}

func TestCommandRemoteNetworkAllow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, report string
	}{
		{"applied", ""},
		{"mismatch", "evil.example.com\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			b := commandSandbox(t, "run")
			overlay, err := os.ReadFile(filepath.Join(b.s.dir, ".satelle", "remotes", "command-task-probe", "satelle.toml"))
			if err != nil {
				t.Fatal(err)
			}
			b.s.write(".satelle/remotes/command-net/remote.toml", commandDefinition("command-net", b.task, "run", "\n[network]\nallow = [\"api.example.com\", \"*.pkg.dev\"]\n"))
			b.s.write(".satelle/remotes/command-net/satelle.toml", string(overlay))
			b.s.git("add", ".satelle")
			b.s.git("commit", "-q", "-m", "command-net")
			if c.report != "" {
				writeFile(t, filepath.Join(b.root, "network-report"), c.report, 0o644)
			}
			r := b.s.run("remote", "fire", "command-net", b.task)
			node := nodeOf(t, r.out)
			got, err := os.ReadFile(filepath.Join(b.root, "network."+node))
			if err != nil || string(got) != "api.example.com\n*.pkg.dev\n" {
				t.Fatalf("the network command got %q, %v; want the definition's list", got, err)
			}
			log := b.log(t)
			if c.report == "" {
				if r.code != 0 || !strings.Contains(r.out, "verdict: accept") {
					t.Fatalf("fire: exit %d\n%s%s", r.code, r.out, r.err)
				}
				assertInOrder(t, log, "start "+node+"\n", "network "+node+"\n", "copy_in "+node+" ")
				if strings.Index(log, "copy_in ") < strings.Index(log, "network ") {
					t.Fatalf("copy_in ran before network:\n%s", log)
				}
				return
			}
			if r.code == 0 {
				t.Fatalf("a fire whose node reports another list exited 0:\n%s", r.out)
			}
			for _, want := range []string{"the node " + node + " allows [evil.example.com]", "[network] allow is [*.pkg.dev, api.example.com]"} {
				if !strings.Contains(r.err, want) {
					t.Fatalf("the refusal misses %q:\n%s", want, r.err)
				}
			}
			if log != "check\nstart "+node+"\nnetwork "+node+"\nstop "+node+"\n" {
				t.Fatalf("the fixture log of a refused network:\n%s", log)
			}
			if _, err := os.Stat(filepath.Join(b.root, node)); !os.IsNotExist(err) {
				t.Fatalf("the node's directory is still there: %v", err)
			}
			var st struct{ State, Node string }
			b.s.mustJSON(&st, "remote", "status", "command-net", b.task)
			if st.State != "stopped" || st.Node != node {
				t.Fatalf("remote status = %+v, want stopped on %s", st, node)
			}
			if execs := executionsOf(b.s, b.task); len(execs) != 0 {
				t.Fatalf("executions of a refused network: %+v", execs)
			}
			b.assertNoDocker(t)
		})
	}
}
