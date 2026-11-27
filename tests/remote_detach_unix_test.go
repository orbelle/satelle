//go:build integration && !windows

package tests

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fireDetached(t *testing.T) (s *sandbox, id, fake, node, name string) {
	t.Helper()
	s, id, fake = dockerLocalSandbox(t, true)
	writeFile(t, filepath.Join(fake, "hold-detached"), "", 0o644)
	out := s.must("remote", "fire", "docker-probe", id, "--detach")
	t.Logf("fire --detach:\n%s", out)
	m := nodeLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("fire --detach names no node:\n%s", out)
	}
	node, name = m[1], m[2]
	if !strings.Contains(out, "bring it back with satelle remote status docker-probe "+id) ||
		!strings.Contains(out, "docker-probe on "+id+": running (node "+node+")") {
		t.Fatalf("fire --detach output:\n%s", out)
	}
	return s, id, fake, node, name
}

func fakeLines(t *testing.T, fake string) []string {
	t.Helper()
	return strings.Split(strings.TrimSpace(fakeLog(t, fake)), "\n")
}

func logIndex(lines []string, from int, ok func(string) bool) int {
	for i := from; i < len(lines); i++ {
		if ok(lines[i]) {
			return i
		}
	}
	return -1
}

func TestDockerLocalFireDetachReturnsBeforeTheNodeEnds(t *testing.T) {
	t.Parallel()
	s, id, fake, node, name := fireDetached(t)

	var st struct {
		State string `json:"state"`
		Node  string `json:"node"`
	}
	s.mustJSON(&st, "remote", "status", "docker-probe", id)
	if st.State != "running" || st.Node != node {
		t.Fatalf("status while the node works = %+v", st)
	}
	nodeDir := filepath.Join(fake, name, "node")
	if _, err := os.Stat(filepath.Join(nodeDir, "out", "outcome.json")); err == nil {
		t.Fatal("the node concluded before the fire returned")
	}
	lines := fakeLines(t, fake)
	copyIn := logIndex(lines, 0, func(l string) bool { return strings.HasPrefix(l, "cp ") && strings.HasSuffix(l, " "+name+":/node/in") })
	detached := logIndex(lines, 0, func(l string) bool {
		return l == "exec -i "+name+" satelle node run-local --dir /node --detach --progress"
	})
	if copyIn < 0 || detached < copyIn || logIndex(lines, 0, func(l string) bool { return strings.HasPrefix(l, "exec -d ") }) >= 0 {
		t.Fatalf("the node was not given its work and started detached with exec -i (cp %d, exec -i %d):\n%s", copyIn, detached, strings.Join(lines, "\n"))
	}
	stdin := filepath.Join(fake, name, "detach.stdin")
	var given map[string]string
	if b, err := os.ReadFile(stdin); err != nil || json.Unmarshal(b, &given) != nil {
		t.Fatalf("the detached start's stdin = %q, %v", b, err)
	}
	if k, err := hex.DecodeString(given["satelle.seal"]); err != nil || len(k) != ed25519.PrivateKeySize {
		t.Fatalf("the detached start's stdin carries no seal key: %q", given["satelle.seal"])
	}
	if n := len(seatsOf(s, id)); n != 1 {
		t.Fatalf("seats while the node works = %d, want the node's", n)
	}

	r := s.exec("sh", "", "-c", satelleBin+" node run-local --dir "+nodeDir+" --detach --progress <"+stdin)
	if r.code != 0 {
		t.Fatalf("the simulated node: %s", r.out)
	}
	deadline := time.Now().Add(2 * time.Minute)
	if d, ok := t.Deadline(); ok {
		deadline = d
	}
	for {
		if b, err := os.ReadFile(filepath.Join(nodeDir, "out", "exit")); err == nil && strings.HasSuffix(string(b), "\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the detached node wrote no exit")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if b, err := os.ReadFile(filepath.Join(nodeDir, "out", "exit")); err != nil || strings.TrimSpace(string(b)) != "0" {
		log, _ := os.ReadFile(filepath.Join(nodeDir, "out", "node.log"))
		t.Fatalf("the node exited %q, %v:\n%s", b, err, log)
	}
	before := len(fakeLines(t, fake))
	out := s.must("remote", "status", "docker-probe", id)
	t.Logf("status:\n%s", out)
	if !strings.HasPrefix(out, "docker-probe on "+id+": concluded (node "+node+")\nverdict: accept\n") || !strings.Contains(out, "imported ") {
		t.Fatalf("status of the concluded node:\n%s", out)
	}
	if len(seatsOf(s, id)) != 0 {
		t.Fatalf("a seat is held after the bring-back: %v", seatsOf(s, id))
	}
	seatOrder(t, chainOf(s, id), node, true)
	lines = fakeLines(t, fake)
	ls := logIndex(lines, before, func(l string) bool { return l == "exec "+name+" ls -A /node/out" })
	cp := logIndex(lines, before, func(l string) bool { return l == "cp "+name+":/node/out/outcome.json -" })
	rm := logIndex(lines, before, func(l string) bool { return strings.HasPrefix(l, "rm ") && strings.Contains(l, name) })
	if ls < 0 || cp < ls || rm < cp {
		t.Fatalf("bring-back order (ls %d, cp %d, rm %d):\n%s", ls, cp, rm, strings.Join(lines[before:], "\n"))
	}
	if v := s.must("ledger", "verify"); !strings.Contains(v, "chains intact") {
		t.Fatalf("ledger verify: %s", v)
	}
	events := len(chainOf(s, id))
	again := s.must("remote", "status", "docker-probe", id)
	if !strings.HasPrefix(again, "docker-probe on "+id+": concluded (node "+node+")") || len(chainOf(s, id)) != events {
		t.Fatalf("a second status:\n%s", again)
	}
}

func TestDockerLocalDetachedRunWhoseContainerIsGone(t *testing.T) {
	t.Parallel()
	s, id, fake, node, name := fireDetached(t)
	if err := os.RemoveAll(filepath.Join(fake, name)); err != nil {
		t.Fatal(err)
	}
	out := s.must("remote", "status", "docker-probe", id)
	if !strings.Contains(out, ": stopped (node "+node+")") || !strings.Contains(out, "the node's container is gone") {
		t.Fatalf("status with the container gone:\n%s", out)
	}
	if len(seatsOf(s, id)) != 0 {
		t.Fatalf("a seat is held after the stop: %v", seatsOf(s, id))
	}
	released := false
	for _, e := range chainOf(s, id) {
		released = released || e.Kind == "seat.released" && payloadField(t, e, "session") == node
	}
	if !released {
		t.Fatal("no seat.released for the node")
	}
}

func serverRemoteSandbox(t *testing.T) (*sandbox, string) {
	t.Helper()
	s, id, _ := dockerLocalSandbox(t, false)
	s.write(".satelle/remotes/srv-probe/remote.toml", `version = 1
name = "srv-probe"
provider = "fly-sprite"
satelle_version = "1.0.21"

[drive]
kind = "item"
`)
	return s, id
}

func TestRemoteFireDetachRefusesAServerRemote(t *testing.T) {
	t.Parallel()
	s, id := serverRemoteSandbox(t)
	r := s.run("remote", "fire", "srv-probe", id, "--detach")
	if r.code == 0 || !strings.Contains(r.err, "--detach applies to a remote that runs here (docker-local or command)") {
		t.Fatalf("fire --detach on a server remote = %d:\n%s%s", r.code, r.out, r.err)
	}
}

func TestRemoteStopRefusesAServerRemote(t *testing.T) {
	t.Parallel()
	s, id := serverRemoteSandbox(t)
	r := s.run("remote", "stop", "srv-probe", id)
	if r.code == 0 || !strings.Contains(r.err, "remote stop applies to a remote that runs here (docker-local or command)") {
		t.Fatalf("remote stop on a server remote = %d:\n%s%s", r.code, r.out, r.err)
	}
}
func TestDockerLocalRemoteStopEndsADetachedNode(t *testing.T) {
	t.Parallel()
	s, id, fake, node, name := fireDetached(t)
	before := len(fakeLines(t, fake))
	out := s.must("remote", "stop", "docker-probe", id, "--session", "stop-it")
	t.Logf("stop:\n%s", out)
	if !strings.HasPrefix(out, "docker-probe on "+id+": stopped (node "+node+")") || !strings.Contains(out, "stopped by stop-it") {
		t.Fatalf("remote stop output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(fake, name)); !os.IsNotExist(err) {
		t.Fatalf("the node's container is still there: %v", err)
	}
	lines := fakeLines(t, fake)
	if logIndex(lines, before, func(l string) bool { return strings.HasPrefix(l, "rm -f "+name) }) < 0 {
		t.Fatalf("the container was not removed:\n%s", strings.Join(lines[before:], "\n"))
	}
	var st struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	s.mustJSON(&st, "remote", "status", "docker-probe", id)
	if st.State != "stopped" || !strings.Contains(st.Reason, "stopped by stop-it") {
		t.Fatalf("status after the stop = %+v", st)
	}
	if len(seatsOf(s, id)) != 0 {
		t.Fatalf("a seat is held after the stop: %v", seatsOf(s, id))
	}
	released := false
	for _, e := range chainOf(s, id) {
		released = released || e.Kind == "seat.released" && payloadField(t, e, "session") == node && e.Actor == "stop-it"
	}
	if !released {
		t.Fatal("no seat.released for the node by stop-it")
	}
	if v := s.must("ledger", "verify"); !strings.Contains(v, "chains intact") {
		t.Fatalf("ledger verify: %s", v)
	}
}
