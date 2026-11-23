//go:build integration && !windows

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fireDetached(t *testing.T) (s *sandbox, id, fake, node, name string) {
	t.Helper()
	s, id, fake = dockerLocalSandbox(t, true)
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
		return strings.HasPrefix(l, "exec -d "+name+" sh -c satelle node run-local --dir /node")
	})
	if copyIn < 0 || detached < copyIn || logIndex(lines, 0, func(l string) bool { return strings.Contains(l, "exec -i ") }) >= 0 {
		t.Fatalf("the node was not given its work and started detached (cp %d, exec -d %d):\n%s", copyIn, detached, strings.Join(lines, "\n"))
	}
	if n := len(seatsOf(s, id)); n != 1 {
		t.Fatalf("seats while the node works = %d, want the node's", n)
	}

	r := s.exec("sh", "", "-c", satelleBin+" node run-local --dir "+nodeDir+" </dev/null >"+nodeDir+"/out/node.log 2>&1; echo $? >"+nodeDir+"/out/exit")
	if r.code != 0 {
		t.Fatalf("the simulated node: %s", r.out)
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

func TestDockerLocalFireDetachRefusesACommandRemote(t *testing.T) {
	t.Parallel()
	s, id, _ := dockerLocalSandbox(t, false)
	s.write(".satelle/remotes/cmd-probe/remote.toml", `version = 1
name = "cmd-probe"
provider = "command"
satelle_version = "1.0.21"

[drive]
kind = "item"

[connect]
start = ["true"]
network = ["true"]
copy_in = ["cp", "{src}", "{dst}"]
exec = ["true"]
stop = ["true"]
`)
	r := s.run("remote", "fire", "cmd-probe", id, "--detach")
	if r.code == 0 || !strings.Contains(r.err, "--detach applies to a docker-local remote") {
		t.Fatalf("fire --detach on a command remote = %d:\n%s%s", r.code, r.out, r.err)
	}
}
