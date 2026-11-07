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

// fakeDocker is a docker CLI that maps each container to $FAKE/<name>/ and
// appends every argv to $FAKE/log. run makes the container's dir; cp copies,
// rewriting <name>:<path> to $FAKE/<name><path>; exec runs the command with
// every /node… argument rewritten into the container's dir, `satelle` being
// the suite's build; container inspect checks the dir exists; rm removes it;
// build only records. A file $FAKE/fail-run-local makes run-local exit 1 with
// no outcome; $FAKE/crash-run-local kills the fire (the exec's parent) there.
const fakeDocker = `#!/bin/sh
FAKE='@FAKE@'
SATELLE='@SATELLE@'
echo "$*" >> "$FAKE/log"
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
    name=""
    while [ $# -gt 0 ]; do
      [ "$1" = --name ] && name=$2
      shift
    done
    mkdir -p "$FAKE/$name"
    echo "$name" ;;
  container) [ -d "$FAKE/$(last "$@")" ] ;;
  rm) rm -rf "$FAKE/$(last "$@")" ;;
  cp) cp -R "$(map "$1")" "$(map "$2")" ;;
  exec)
    name=$1; shift
    [ -d "$FAKE/$name" ] || { echo "Error: no such container: $name" >&2; exit 1; }
    prog=$1; shift
    if [ "$prog" = satelle ] && [ "$1 $2" = "node run-local" ]; then
      [ -f "$FAKE/fail-run-local" ] && { echo "the node broke" >&2; exit 1; }
      [ -f "$FAKE/crash-run-local" ] && { kill -9 $PPID; exit 1; }
    fi
    [ "$prog" = satelle ] && prog=$SATELLE
    n=$#
    for a in "$@"; do
      case "$a" in /node*) a="$FAKE/$name$a" ;; esac
      set -- "$@" "$a"
    done
    shift $n
    exec "$prog" "$@" ;;
  *) echo "fake docker: $cmd not faked" >&2; exit 1 ;;
esac
`

// dockerLocalSandbox is a sandbox with the starter configuration committed, the
// repo's docker-probe definition copied byte for byte, and one story. fake,
// when set, is the fake docker's directory, first on PATH.
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

// chainEvent is one event of `ledger list --json`, with its actor.
type chainEvent struct {
	Kind    string          `json:"kind"`
	Actor   string          `json:"actor"`
	Payload json.RawMessage `json:"payload"`
}

// seatsOf is the seats held on id, as `story get --json` shows them.
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

// seatOrder checks the chain holds seat.bound {node, remote:<node>}, then
// node:<node> events (at least one when nodeEvents), then seat.released
// {node}, in that order.
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

// The fired node, with a fake docker: the fire names the node and its
// container, prints the docker exec hint before the node runs, the item moves
// one state, status shows the run, the chain here carries the node's events
// between the seat and its release, and it verifies. The container is left
// (scope job). The help says how to fire it.
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
		case strings.HasPrefix(l, "exec "+name+" satelle node run-local"):
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
	// The hint was printed before the node's run-local exec: fire prints it
	// as soon as the container is up, and the exec is the node's step.
	if strings.Index(out, hint) > strings.Index(out, "running the node on "+id) {
		t.Fatalf("the hint follows the node's run:\n%s", out)
	}
	if v := s.exec(filepath.Join(fake, "docker"), "", "exec", name, "satelle", "version"); v.code != 0 || !strings.HasPrefix(v.out, "satelle ") {
		t.Fatalf("docker exec after fire (scope job): %+v", v)
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

// A node that fails: fire exits non-zero, the seat is bound then released, the
// run is stopped with the reason, and the container is left to inspect.
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
	s.must("ledger", "verify")
}

// A fire that died mid-run leaves a running record and a held seat. Once its
// container is gone, the next fire marks it stopped, releases the old node's
// seat and runs; while the container lives, the next fire is refused.
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

// lockedBuffer is a bytes.Buffer safe to read while a process writes it.
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

// gateShim is a docker CLI over the real one. It logs each argv to
// $GATE/log. The sandbox's node-image build is run against this repo's
// Dockerfile and source (any other build exits 2); the node's run-local exec
// writes its container to $GATE/running and waits for $GATE/release.
const gateShim = `#!/bin/sh
GATE='@GATE@'
REAL='@REAL@'
REPO='@REPO@'
SANDBOX='@SANDBOX@'
echo "$*" >> "$GATE/log"
if [ "$1" = build ]; then
  if [ "$*" = "build -t satelle-node:local -f $SANDBOX/Dockerfile $SANDBOX" ]; then
    exec "$REAL" build -t satelle-node:local -f "$REPO/Dockerfile" "$REPO"
  fi
  echo "gate shim: unexpected build: $*" >&2
  exit 2
fi
if [ "$1" = exec ] && [ "$3 $4 $5" = "satelle node run-local" ]; then
  echo "$2" > "$GATE/running"
  while [ ! -f "$GATE/release" ]; do sleep 0.2; done
fi
exec "$REAL" "$@"
`

// versionOf is .version's satelle.version.
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

// The repo's docker-probe on a real daemon: the node image is this repo's
// source built from its Dockerfile, and while fire is held at the node's step
// the container answers `docker exec … satelle version` with .version's
// satelle.version. Released, the run concludes accepting and the chain
// verifies. Needs a docker daemon and SATELLE_DOCKER_BUILD=1, since the build
// pulls golang and alpine and the suite never pulls; run with -timeout 30m.
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

	// At the gate: fire is blocked on the node's run-local exec.
	select {
	case err := <-exited:
		t.Fatalf("fire returned at the gate: %v", err)
	default:
	}
	if !strings.Contains(stdout.String(), "inspect it: docker exec -it "+name+" sh\n") {
		t.Fatalf("no docker exec hint for %s before the node ran:\n%s", name, stdout.String())
	}
	sl := shimLog()
	if r, x := strings.Index(sl, "run -d --name "+name+" "), strings.Index(sl, "exec "+name+" satelle node run-local"); r < 0 || x < 0 || r > x {
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
