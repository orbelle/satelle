//go:build integration && !windows

package tests

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeDocker = `#!/bin/sh
FAKE='@FAKE@'
SATELLE='@SATELLE@'
echo "$*" >> "$FAKE/log"
env >> "$FAKE/docker-env.log"
map() {
  case "$1" in
    satelle-node-*:*) echo "$FAKE/${1%%:*}${1#*:}" ;;
    *) echo "$1" ;;
  esac
}
last() { for a; do l=$a; done; echo "$l"; }
cmd=$1; shift
case "$cmd" in
  build|info) exit 0 ;;
  image) exit 1 ;;
  run)
    [ -f "$FAKE/fail-run" ] && { echo "Error: the fake daemon refused the run" >&2; exit 1; }
    name=""
    label=""
    while [ $# -gt 0 ]; do
      [ "$1" = --name ] && name=$2
      [ "$1" = --label ] && label=$2
      shift
    done
    mkdir -p "$FAKE/$name"
    echo "$label" > "$FAKE/$name/.label"
    echo "$name" ;;
  container) [ -d "$FAKE/$(last "$@")" ] ;;
  rm) rm -rf "$FAKE/$(last "$@")" ;;
  cp)
    if [ "$2" = - ]; then
      src=$(map "$1")
      [ -e "$src" ] || { echo "Error: no such file: $1" >&2; exit 1; }
      tmp=$(mktemp -d)
      cp -R "$src" "$tmp/" || exit 1
      if [ -f "$FAKE/bad-chain" ]; then
        case "$1" in */node/runtime/ledger)
          for f in "$tmp"/ledger/exe_*.jsonl; do [ -f "$f" ] && sed -i '1s/Probe a/probe a/' "$f"; done ;;
        esac
      fi
      tar -C "$tmp" -cf - "$(basename "$src")"; rc=$?
      rm -rf "$tmp"
      exit $rc
    fi
    cp -R "$(map "$1")" "$(map "$2")" || exit 1
    [ -f "$FAKE/bad-bundle" ] && [ -f "$(map "$2")/head.bundle" ] && : > "$(map "$2")/head.bundle"
    exit 0 ;;
  exec)
    if [ "$1" = -d ]; then [ -d "$FAKE/$2" ] || exit 1; exit 0; fi
    [ "$1" = -i ] && shift
    name=$1; shift
    [ -d "$FAKE/$name" ] || { echo "Error: no such container: $name" >&2; exit 1; }
    prog=$1; shift
    case "$prog $1" in sh\ /node/in/run-check) [ -f "$FAKE/no-exit" ] && exit 0 ;; esac
    if [ "$prog" = satelle ] && [ "$1 $2" = "node run-local" ]; then
      [ -f "$FAKE/fail-run-local" ] && { echo "the node broke" >&2; exit 1; }
      [ -f "$FAKE/crash-run-local" ] && { kill -9 $PPID; exit 1; }
      [ -f "$FAKE/hold-run-local" ] && { : > "$FAKE/held"; exec sleep 120; }
      if [ -f "$FAKE/hold-detached" ]; then
        case " $* " in *" --detach "*) cat > "$FAKE/$name/detach.stdin"; echo "node run started detached as process 0"; exit 0 ;; esac
      fi
    fi
    [ "$prog" = satelle ] && prog=$SATELLE
    n=$#
    for a in "$@"; do
      case "$a" in /node*) a="$FAKE/$name$a" ;; esac
      set -- "$@" "$a"
    done
    shift $n
    if [ -n "$FAKE_LEAK_BLOB" ] && [ "$prog" = "$SATELLE" ]; then
      "$prog" "$@"; rc=$?
      blobs="$FAKE/$name/node/runtime/blobs"
      mkdir -p "$blobs"
      printf '%s' "$FAKE_LEAK_BLOB" > "$blobs/leak.tmp"
      sum=$(sha256sum "$blobs/leak.tmp" | cut -d' ' -f1)
      mv "$blobs/leak.tmp" "$blobs/$sum"
      exit $rc
    fi
    exec "$prog" "$@" ;;
  *) echo "fake docker: $cmd not faked" >&2; exit 1 ;;
esac
`

func dockerLocalSandbox(t *testing.T, withFake bool) (s *sandbox, id, fake string) {
	t.Helper()
	s = newSandbox(t)
	s.must("init")
	probe, err := os.ReadFile(filepath.Join("..", ".satelle", "remotes", "docker-probe", "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/remotes/docker-probe/remote.toml", string(probe))
	s.git("add", ".satelle")
	s.git("commit", "-q", "-m", "satelle")
	id = s.create("story", "--title", "probe me").ID
	if withFake {
		fake = t.TempDir()
		script := strings.NewReplacer("@FAKE@", fake, "@SATELLE@", satelleBin).Replace(fakeDocker)
		writeFile(t, filepath.Join(fake, "docker"), script, 0o755)
		s.path = append([]string{fake}, s.path...)
	}
	return s, id, fake
}

type chainEvent struct {
	Kind    string          `json:"kind"`
	Actor   string          `json:"actor"`
	Payload json.RawMessage `json:"payload"`
}

func gateOf(t *testing.T, e chainEvent) string {
	t.Helper()
	var p struct {
		Gate string `json:"gate"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("%s payload: %v", e.Kind, err)
	}
	return p.Gate
}

func seatsOf(s *sandbox, id string) []json.RawMessage {
	s.t.Helper()
	var got struct {
		Seats []json.RawMessage `json:"seats"`
	}
	s.mustJSON(&got, "story", "get", id)
	return got.Seats
}

func chainOf(s *sandbox, id string) []chainEvent {
	s.t.Helper()
	var evs []chainEvent
	s.mustJSON(&evs, "ledger", "list", id)
	return evs
}

func payloadField(t *testing.T, e chainEvent, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		t.Fatal(err)
	}
	v, _ := m[key].(string)
	return v
}

func seatOrder(t *testing.T, evs []chainEvent, node string, nodeEvents bool) {
	t.Helper()
	bound, released, first, lastNode := -1, -1, -1, -1
	for i, e := range evs {
		switch {
		case e.Kind == "seat.bound" && payloadField(t, e, "session") == node:
			if payloadField(t, e, "location") != "remote:"+node {
				t.Fatalf("seat at %q, want remote:%s", payloadField(t, e, "location"), node)
			}
			bound = i
		case e.Kind == "seat.released" && payloadField(t, e, "session") == node:
			released = i
		case e.Actor == "node:"+node:
			if first < 0 {
				first = i
			}
			lastNode = i
		}
	}
	if bound < 0 || released < 0 || bound > released {
		t.Fatalf("seat of %s: bound at %d, released at %d\n%+v", node, bound, released, evs)
	}
	if nodeEvents && (first < 0 || first < bound || lastNode > released) {
		t.Fatalf("node events %d..%d not between the seat %d and its release %d", first, lastNode, bound, released)
	}
	if !nodeEvents && first >= 0 {
		t.Fatalf("the node appended events at %d on a failed run", first)
	}
}

var nodeLine = regexp.MustCompile(`(?m)^node (nod_[0-9a-f]{12}) in container (satelle-node-nod_[0-9a-f]{12})$`)

func TestDockerLocalFireRoundTrip(t *testing.T) {
	t.Parallel()
	s, id, fake := dockerLocalSandbox(t, true)
	before := s.get(id).Status
	out := s.must("remote", "fire", "docker-probe", id, "--steps", "1")
	t.Logf("fire:\n%s", out)
	m := nodeLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s", out)
	}
	node, name := m[1], m[2]
	if name != "satelle-node-"+node {
		t.Fatalf("container %s for node %s", name, node)
	}
	hint := "inspect it: docker exec -it " + name + " sh"
	if !strings.Contains(out, hint+"\n") {
		t.Fatalf("no docker exec hint:\n%s", out)
	}
	if !strings.Contains(out, "docker-probe on "+id+": concluded (node "+node+")") || !strings.Contains(out, "verdict: accept") {
		t.Fatalf("fire output:\n%s", out)
	}

	b, err := os.ReadFile(filepath.Join(fake, "log"))
	if err != nil {
		t.Fatal(err)
	}
	log := strings.Split(strings.TrimSpace(string(b)), "\n")
	run, runLocal := -1, -1
	for i, l := range log {
		switch {
		case strings.HasPrefix(l, "run -d --name "+name+" "):
			run = i
		case strings.HasPrefix(l, "exec -i "+name+" satelle node run-local"):
			runLocal = i
		case strings.HasPrefix(l, "rm ") && runLocal < 0:
			t.Fatalf("rm before the node ran: %q", l)
		}
	}
	if run < 0 || runLocal < 0 || run > runLocal {
		t.Fatalf("docker log: run at %d, run-local at %d\n%s", run, runLocal, b)
	}
	if !strings.HasPrefix(log[0], "build -t satelle-node:local -f "+s.dir+"/Dockerfile "+s.dir) {
		t.Fatalf("the node image is not built from the repo's Dockerfile first: %q", log[0])
	}
	if strings.Index(out, hint) > strings.Index(out, "running the node on "+id) {
		t.Fatalf("the hint follows the node's run:\n%s", out)
	}
	removed := false
	for _, l := range log[runLocal+1:] {
		removed = removed || (strings.HasPrefix(l, "rm ") && strings.Contains(l, name))
	}
	if !removed {
		t.Fatalf("the accepted run did not remove %s (scope run):\n%s", name, b)
	}

	after := s.get(id)
	if after.Status == before {
		t.Fatalf("the item did not move from %s", before)
	}
	if len(seatsOf(s, id)) != 0 {
		t.Fatalf("a seat is held after fire: %v", seatsOf(s, id))
	}
	seatOrder(t, chainOf(s, id), node, true)
	st := s.must("remote", "status", "docker-probe", id)
	if !strings.HasPrefix(st, "docker-probe on "+id+": concluded (node "+node+")\nverdict: accept\n") ||
		!strings.Contains(st, "stopped after 1 step(s)") || !strings.Contains(st, "imported ") {
		t.Fatalf("status:\n%s", st)
	}
	if v := s.must("ledger", "verify"); !strings.Contains(v, "chains intact") {
		t.Fatalf("ledger verify: %s", v)
	}
	help := s.must("help", "remotes")
	for _, want := range []string{"satelle remote fire docker-probe <item> --steps 1", "runs locally"} {
		if !strings.Contains(help, want) {
			t.Errorf("satelle help remotes misses %q", want)
		}
	}
	if r := s.run("remote", "fire", "docker-probe", id, "--steps", "-1"); r.code == 0 {
		t.Fatal("--steps -1 accepted")
	}
}

func TestDockerLocalFireReleasesOnFailure(t *testing.T) {
	t.Parallel()
	s, id, fake := dockerLocalSandbox(t, true)
	writeFile(t, filepath.Join(fake, "fail-run-local"), "", 0o644)
	r := s.run("remote", "fire", "docker-probe", id, "--steps", "1")
	if r.code == 0 {
		t.Fatalf("fire of a failing node exited 0:\n%s", r.out)
	}
	m := nodeLine.FindStringSubmatch(r.out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s%s", r.out, r.err)
	}
	node, name := m[1], m[2]
	seatOrder(t, chainOf(s, id), node, false)
	if len(seatsOf(s, id)) != 0 {
		t.Fatal("a seat is held after a failed fire")
	}
	st := s.must("remote", "status", "docker-probe", id)
	if !strings.HasPrefix(st, "docker-probe on "+id+": stopped (node "+node+")") || !strings.Contains(st, "the node broke") {
		t.Fatalf("status:\n%s", st)
	}
	b, _ := os.ReadFile(filepath.Join(fake, "log"))
	if strings.Contains(string(b), "\nrm ") {
		t.Fatalf("the failed node's container was removed:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(fake, name)); err != nil {
		t.Fatalf("the container is gone: %v", err)
	}
	if want := "the container " + name + " is left to inspect; remove it with docker rm -f " + name; !strings.Contains(r.out, want) {
		t.Fatalf("fire output misses %q:\n%s", want, r.out)
	}
	s.must("ledger", "verify")
}

func TestDockerLocalFireRecoversAStaleRun(t *testing.T) {
	t.Parallel()
	s, id, fake := dockerLocalSandbox(t, true)
	crash := filepath.Join(fake, "crash-run-local")
	writeFile(t, crash, "", 0o644)
	r := s.run("remote", "fire", "docker-probe", id, "--steps", "1")
	if r.code == 0 {
		t.Fatalf("the crashed fire exited 0:\n%s", r.out)
	}
	m := nodeLine.FindStringSubmatch(r.out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s%s", r.out, r.err)
	}
	old, oldName := m[1], m[2]
	if st := s.must("remote", "status", "docker-probe", id); !strings.HasPrefix(st, "docker-probe on "+id+": running (node "+old+")") {
		t.Fatalf("status after the crash:\n%s", st)
	}
	if len(seatsOf(s, id)) != 1 {
		t.Fatal("the crashed fire left no seat")
	}
	if err := os.Remove(crash); err != nil {
		t.Fatal(err)
	}
	if r := s.run("remote", "fire", "docker-probe", id, "--steps", "1"); r.code == 0 || !strings.Contains(r.err, "is running on node "+old) {
		t.Fatalf("a fire while the old container lives: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if err := os.RemoveAll(filepath.Join(fake, oldName)); err != nil {
		t.Fatal(err)
	}
	out := s.must("remote", "fire", "docker-probe", id, "--steps", "1")
	if !strings.Contains(out, "the run on node "+old+" was left behind") {
		t.Fatalf("fire output:\n%s", out)
	}
	m = nodeLine.FindStringSubmatch(out)
	if m == nil || m[1] == old {
		t.Fatalf("no new node:\n%s", out)
	}
	evs := chainOf(s, id)
	released := false
	for _, e := range evs {
		released = released || e.Kind == "seat.released" && payloadField(t, e, "session") == old
	}
	if !released {
		t.Fatal("the stale node's seat was not released")
	}
	seatOrder(t, evs, m[1], true)
	if len(seatsOf(s, id)) != 0 {
		t.Fatal("a seat is held after the recovered fire")
	}
	s.must("ledger", "verify")
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

const gateShim = `#!/bin/sh
GATE='@GATE@'
REAL='@REAL@'
REPO='@REPO@'
SANDBOX='@SANDBOX@'
echo "$*" >> "$GATE/log"
env >> "$GATE/docker-env.log"
if [ "$1" = build ]; then
  if [ "$*" = "build -t satelle-node:local -f $SANDBOX/Dockerfile $SANDBOX" ]; then
    exec "$REAL" build -t satelle-node:local -f "$REPO/Dockerfile" "$REPO"
  fi
  echo "gate shim: unexpected build: $*" >&2
  exit 2
fi
if [ "$1" = exec ] && { [ "$3 $4 $5" = "satelle node run-local" ] || [ "$4 $5 $6" = "satelle node run-local" ]; }; then
  if [ "$2" = -i ]; then echo "$3" > "$GATE/running"; else echo "$2" > "$GATE/running"; fi
  while [ ! -f "$GATE/release" ]; do sleep 0.2; done
fi
if [ "$1" = rm ] && [ -f "$GATE/hold-rm" ]; then
  : > "$GATE/removing"
  while [ ! -f "$GATE/rm-release" ]; do sleep 0.2; done
fi
exec "$REAL" "$@"
`

func versionOf(t *testing.T, repo string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(repo, ".version"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "satelle.version:"); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatal(".version has no satelle.version")
	return ""
}

func TestDockerLocalFireOnDaemon(t *testing.T) {
	if os.Getenv("SATELLE_DOCKER_BUILD") != "1" {
		t.Skip("set SATELLE_DOCKER_BUILD=1 to build the node image on a docker daemon")
	}
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
	want := versionOf(t, repo)
	s, id, _ := dockerLocalSandbox(t, false)
	gate := t.TempDir()
	shimDir := t.TempDir()
	shim := strings.NewReplacer("@GATE@", gate, "@REAL@", real, "@REPO@", repo, "@SANDBOX@", s.dir).Replace(gateShim)
	writeFile(t, filepath.Join(shimDir, "docker"), shim, 0o755)
	s.path = append([]string{shimDir}, s.path...)
	release := func() { _ = os.WriteFile(filepath.Join(gate, "release"), nil, 0o644) }
	var name string
	t.Cleanup(func() {
		release()
		if name != "" {
			_ = exec.Command(real, "rm", "-f", name).Run()
		}
	})

	var stdout, stderr lockedBuffer
	cmd := exec.Command(satelleBin, "remote", "fire", "docker-probe", id, "--steps", "1")
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = s.dir, s.environ(), &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(25 * time.Minute)
	if d, ok := t.Deadline(); ok {
		deadline = d.Add(-30 * time.Second)
	}
	shimLog := func() string { b, _ := os.ReadFile(filepath.Join(gate, "log")); return string(b) }
	for name == "" {
		if b, err := os.ReadFile(filepath.Join(gate, "running")); err == nil && len(bytes.TrimSpace(b)) > 0 {
			name = strings.TrimSpace(string(b))
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("fire returned before the node ran: %v\n%s%s\nshim log:\n%s", err, stdout.String(), stderr.String(), shimLog())
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("the node never ran before the deadline\n%s%s\nshim log:\n%s", stdout.String(), stderr.String(), shimLog())
		}
	}

	select {
	case err := <-exited:
		t.Fatalf("fire returned at the gate: %v", err)
	default:
	}
	if !strings.Contains(stdout.String(), "inspect it: docker exec -it "+name+" sh\n") {
		t.Fatalf("no docker exec hint for %s before the node ran:\n%s", name, stdout.String())
	}
	sl := shimLog()
	if r, x := strings.Index(sl, "run -d --name "+name+" "), strings.Index(sl, "exec -i "+name+" satelle node run-local"); r < 0 || x < 0 || r > x {
		t.Fatalf("shim log: run at %d, gated exec at %d\n%s", r, x, sl)
	}
	v, err := exec.Command(real, "exec", name, "satelle", "version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(v), "satelle "+want) {
		t.Fatalf("docker exec %s satelle version while fire runs = %q, %v; want satelle %s", name, v, err, want)
	}
	t.Logf("while fire runs: docker exec %s satelle version: %s", name, strings.TrimSpace(string(v)))

	release()
	if err := <-exited; err != nil {
		t.Fatalf("fire: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	st := s.must("remote", "status", "docker-probe", id)
	if !strings.Contains(st, ": concluded (node ") || !strings.Contains(st, "verdict: accept") {
		t.Fatalf("status:\n%s", st)
	}
	s.must("ledger", "verify")
}

const failCheck = `---
name: probe-fail-check
type: check
description: A check that fails, for the test of a task's failing step.
---

` + "```sh\necho probe step failed\nexit 1\n```\n"

func taskSandbox(t *testing.T, withFake bool) (s *sandbox, task, input, fake string) {
	t.Helper()
	s, _, fake = dockerLocalSandbox(t, withFake)
	for _, rel := range []string{
		"workflows/probe.toml",
		"skills/satelle-probe-version-check.md",
		"skills/satelle-change-judged-check.md",
		"remotes/docker-task-probe/remote.toml",
		"remotes/docker-task-probe/satelle.toml",
	} {
		b, err := os.ReadFile(filepath.Join("..", ".satelle", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		s.write(".satelle/"+rel, string(b))
	}
	probe, err := os.ReadFile(filepath.Join("..", ".satelle", "workflows", "probe.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/workflows/probe-fail.toml", strings.NewReplacer(`name = "probe"`, `name = "probe-fail"`,
		"probe-version", "probe-fail", "satelle-probe-version-check", "probe-fail-check").Replace(string(probe)))
	s.write(".satelle/skills/probe-fail-check.md", failCheck)
	s.write(".satelle/remotes/docker-task-fail/satelle.toml", "[workflows]\ndefault = \"probe-fail\"\n")
	task = s.create("task", "--title", "Probe a docker-local task drive", "--input-kind", "story", "--input-tag", "remote-probe").ID
	input = s.create("story", "--title", "a probe input", "--tag", "remote-probe").ID
	def, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "remotes", "docker-task-probe", "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	pinned := regexp.MustCompile(`(?m)^task = ".*"$`).ReplaceAllString(string(def), `task = "`+task+`"`)
	s.write(".satelle/remotes/docker-task-probe/remote.toml", pinned)
	s.write(".satelle/remotes/docker-task-fail/remote.toml", strings.Replace(pinned, `name = "docker-task-probe"`, `name = "docker-task-fail"`, 1))
	s.git("add", ".satelle")
	s.git("commit", "-q", "-m", "task probe")
	return s, task, input, fake
}

type execRow struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func executionsOf(s *sandbox, task string) []execRow {
	s.t.Helper()
	var rows []execRow
	s.mustJSON(&rows, "execution", "list", "--parent", task)
	return rows
}

func runLocalCount(t *testing.T, fake string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fake, "log"))
	if err != nil {
		t.Fatal(err)
	}
	return len(regexp.MustCompile(`(?m)^exec -i satelle-node-nod_[0-9a-f]+ satelle node run-local`).FindAll(b, -1))
}

func TestDockerLocalFireTask(t *testing.T) {
	t.Parallel()
	s, task, input, fake := taskSandbox(t, true)
	if r := s.run("remote", "fire", "docker-task-probe", input); r.code == 0 || !strings.Contains(r.err, "[drive] task is "+task) {
		t.Fatalf("fire at an input story: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "log")); strings.Contains(string(b), "run -d") {
		t.Fatalf("a container started for a refused fire:\n%s", b)
	}

	out := s.must("remote", "fire", "docker-task-probe", task)
	t.Logf("fire:\n%s", out)
	m := nodeLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s", out)
	}
	node := m[1]
	if !strings.Contains(out, "docker-task-probe on "+task+": concluded (node "+node+")") || !strings.Contains(out, "verdict: accept") {
		t.Fatalf("fire output:\n%s", out)
	}
	b, err := os.ReadFile(filepath.Join(fake, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if r, x := strings.Index(string(b), "run -d --name "+m[2]+" "), strings.Index(string(b), "exec -i "+m[2]+" satelle node run-local"); r < 0 || x < 0 || r > x {
		t.Fatalf("docker log: run at %d, run-local at %d\n%s", r, x, b)
	}

	execs := executionsOf(s, task)
	if len(execs) != 1 || execs[0].Status != "done" {
		t.Fatalf("executions of %s = %+v, want one at done", task, execs)
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
	st := s.must("remote", "status", "docker-task-probe", task)
	if !strings.HasPrefix(st, "docker-task-probe on "+task+": concluded (node "+node+")\nverdict: accept\n") ||
		!strings.Contains(st, "execution exe_") || !strings.Contains(st, " over 1 items") || !strings.Contains(st, "imported ") {
		t.Fatalf("status:\n%s", st)
	}
	for _, e := range chainOf(s, task) {
		if e.Kind == "seat.bound" {
			t.Fatal("the task was seated for a task drive")
		}
	}

	help := s.must("help", "remotes")
	for _, want := range []string{"A docker-local node runs a task drive", "`[network] allow` is enforced on docker through satelle's egress proxy", "`[secrets.override]` and `[[repo]]` are refused locally"} {
		if !strings.Contains(help, want) {
			t.Errorf("satelle help remotes misses %q", want)
		}
	}
	if strings.Contains(help, "a task `[drive]`") {
		t.Error("satelle help remotes still refuses a task drive")
	}
	if strings.Contains(help, "may allow no host") {
		t.Error("satelle help remotes still says a secret may allow no host")
	}
}

func TestDockerLocalFireTaskRejectsABrokenChain(t *testing.T) {
	t.Parallel()
	s, task, _, fake := taskSandbox(t, true)
	writeFile(t, filepath.Join(fake, "bad-chain"), "", 0o644)
	r := s.run("remote", "fire", "docker-task-probe", task)
	if r.code == 0 {
		t.Fatalf("a fire whose chain does not verify exited 0:\n%s", r.out)
	}
	st := s.must("remote", "status", "docker-task-probe", task)
	if !strings.Contains(st, ": concluded (node ") || !strings.Contains(st, "verdict: reject") ||
		!regexp.MustCompile(`the node's chain of exe_[0-9a-f]+ does not verify`).MatchString(st) {
		t.Fatalf("status:\n%s", st)
	}
	if execs := executionsOf(s, task); len(execs) != 0 {
		t.Fatalf("executions imported from a broken run: %+v", execs)
	}
	s.must("ledger", "verify")
}

func TestDockerLocalFireTaskStepFails(t *testing.T) {
	t.Parallel()
	s, task, _, fake := taskSandbox(t, true)
	r := s.run("remote", "fire", "docker-task-fail", task)
	if r.code == 0 {
		t.Fatalf("a fire whose step fails exited 0:\n%s", r.out)
	}
	st := s.must("remote", "status", "docker-task-fail", task)
	if !strings.Contains(st, ": concluded (node ") || !strings.Contains(st, "verdict: reject") ||
		!strings.Contains(st, "probe-fail") || !strings.Contains(st, "probe step failed") {
		t.Fatalf("status:\n%s", st)
	}
	execs := executionsOf(s, task)
	if len(execs) != 1 || execs[0].Status != "backlog" {
		t.Fatalf("executions of %s = %+v, want one at backlog", task, execs)
	}
	gateRuns := 0
	for _, e := range chainOf(s, execs[0].ID) {
		if e.Kind == "gate.run" && gateOf(t, e) == "probe-fail" {
			gateRuns++
		}
	}
	if gateRuns != 1 {
		t.Fatalf("the failing step ran %d times", gateRuns)
	}
	if n := runLocalCount(t, fake); n != 1 {
		t.Fatalf("the node ran %d times", n)
	}
	m := nodeLine.FindStringSubmatch(r.out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s", r.out)
	}
	assertRemoved(t, fake, m[2], r.out)
	s.must("ledger", "verify")
}

func TestDockerLocalFireTaskOnDaemon(t *testing.T) {
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
	s, task, _, _ := taskSandbox(t, false)
	gate, shimDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(gate, "release"), "", 0o644)
	shim := strings.NewReplacer("@GATE@", gate, "@REAL@", real, "@REPO@", repo, "@SANDBOX@", s.dir).Replace(gateShim)
	writeFile(t, filepath.Join(shimDir, "docker"), shim, 0o755)
	s.path = append([]string{shimDir}, s.path...)
	var name string
	t.Cleanup(func() {
		if name != "" {
			_ = exec.Command(real, "rm", "-f", name).Run()
		}
	})
	r := s.run("remote", "fire", "docker-task-probe", task)
	if m := nodeLine.FindStringSubmatch(r.out); m != nil {
		name = m[2]
	}
	if r.code != 0 {
		t.Fatalf("fire on the daemon: exit %d\n%s%s", r.code, r.out, r.err)
	}
	t.Logf("fire:\n%s", r.out)
	st := s.must("remote", "status", "docker-task-probe", task)
	if !strings.Contains(st, ": concluded (node ") || !strings.Contains(st, "verdict: accept") {
		t.Fatalf("status:\n%s", st)
	}
	execs := executionsOf(s, task)
	if len(execs) != 1 || execs[0].Status != "done" {
		t.Fatalf("executions of %s = %+v, want one at done", task, execs)
	}
	ranOnNode := false
	for _, e := range chainOf(s, execs[0].ID) {
		ranOnNode = ranOnNode || e.Kind == "gate.run" && strings.HasPrefix(e.Actor, "node:nod_")
	}
	if !ranOnNode {
		t.Fatal("the execution's gate did not run on the node")
	}
	s.must("ledger", "verify")
	if name != "" && exec.Command(real, "container", "inspect", name).Run() == nil {
		t.Fatalf("the container %s is left after an accepted run with scope run", name)
	}
}
