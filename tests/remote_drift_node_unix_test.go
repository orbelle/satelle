//go:build integration && !windows

package tests

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func driftNodeSandbox(t *testing.T, withFake bool) (s *sandbox, task, fake string) {
	t.Helper()
	s, _, fake = dockerLocalSandbox(t, withFake)
	for _, rel := range []string{
		"scripts/drift-check.sh",
		".satelle/workflows/drift-node.toml",
		".satelle/skills/satelle-drift-node-check.md",
		".satelle/skills/satelle-change-judged-check.md",
		".satelle/remotes/docker-drift-check/remote.toml",
		".satelle/remotes/docker-drift-check/satelle.toml",
	} {
		b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		s.write(rel, string(b))
	}
	task = s.create("task", "--title", "Run the drift check on a docker-local node", "--input-kind", "story", "--input-tag", "docker-drift-check").ID
	def, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "remotes", "docker-drift-check", "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/remotes/docker-drift-check/remote.toml",
		regexp.MustCompile(`(?m)^task = ".*"$`).ReplaceAllString(string(def), `task = "`+task+`"`))
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "drift node")
	return s, task, fake
}

type nodeGateRun struct {
	Gate, Result, Notes, Actor string
}

func gateRunsOf(t *testing.T, s *sandbox, id string) []nodeGateRun {
	t.Helper()
	var runs []nodeGateRun
	for _, e := range chainOf(s, id) {
		if e.Kind != "gate.run" {
			continue
		}
		var p struct {
			Gate   string `json:"gate"`
			Result string `json:"result"`
			Notes  string `json:"notes"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, nodeGateRun{Gate: p.Gate, Result: p.Result, Notes: p.Notes, Actor: e.Actor})
	}
	return runs
}

func assertRemoved(t *testing.T, fake, container, out string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fake, container)); err == nil {
		t.Errorf("the container %s is still there after the run concluded", container)
	}
	b, err := os.ReadFile(filepath.Join(fake, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "rm -f "+container) {
		t.Errorf("docker log has no rm -f %s:\n%s", container, b)
	}
	if !strings.Contains(out, "the container "+container+" is removed") {
		t.Errorf("fire output does not say the container is removed:\n%s", out)
	}
}

func TestDockerLocalFireDriftCheck(t *testing.T) {
	t.Parallel()
	s, task, fake := driftNodeSandbox(t, true)
	s.write(".satelle/skills/stale.md", "---\nname: stale\ntype: skill\ndescription: s\n---\n\nRun `satelle nosuchcmd <id>`.\n")

	out := s.must("remote", "fire", "docker-drift-check", task)
	t.Logf("fire:\n%s", out)
	m := nodeLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s", out)
	}
	node, container := m[1], m[2]
	if !strings.Contains(out, "docker-drift-check on "+task+": concluded (node "+node+")") || !strings.Contains(out, "verdict: accept") {
		t.Fatalf("fire output:\n%s", out)
	}
	b, err := os.ReadFile(filepath.Join(fake, "log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	if !regexp.MustCompile(`(?m)^run -d .*--network none`).MatchString(log) {
		t.Fatalf("the node was not started with no network:\n%s", log)
	}
	co, rl := strings.Index(log, "exec "+container+" git -C /node/work checkout"), strings.Index(log, "exec -i "+container+" satelle node run-local")
	if co < 0 || rl < 0 || co > rl {
		t.Fatalf("docker log: checkout at %d, run-local at %d\n%s", co, rl, log)
	}
	def, err := os.ReadFile(filepath.Join("..", ".satelle", "remotes", "docker-drift-check", "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(def), "[secrets") || strings.Contains(string(def), "[network") {
		t.Fatalf("docker-drift-check declares a secret or a network allow:\n%s", def)
	}

	execs := executionsOf(s, task)
	if len(execs) != 1 || execs[0].Status != "done" {
		t.Fatalf("executions of %s = %+v, want one at done", task, execs)
	}
	runs := gateRunsOf(t, s, execs[0].ID)
	if len(runs) != 2 || runs[0].Gate != "change-judged" || runs[0].Result != "accept" || runs[0].Notes != "no judge recorded a change" ||
		runs[1].Gate != "drift-check" || runs[1].Result != "accept" {
		t.Fatalf("gate.runs = %+v", runs)
	}
	for _, r := range runs {
		if r.Actor != "node:"+node {
			t.Fatalf("gate %s by %q, want node:%s", r.Gate, r.Actor, node)
		}
	}
	notes := runs[1].Notes
	if !strings.HasPrefix(notes, "satelle: ") || !strings.HasSuffix(notes, "drift-check exit 0") || strings.Contains(notes, "nosuchcmd") {
		t.Fatalf("drift-check notes:\n%s", notes)
	}

	st := s.must("remote", "status", "docker-drift-check", task)
	cj, dc := strings.Index(st, "gate change-judged accept:\nno judge recorded a change"), strings.Index(st, "gate drift-check accept:\nsatelle: ")
	if cj < 0 || dc < cj || !strings.Contains(st[dc:], "drift-check exit 0") {
		t.Fatalf("status:\n%s", st)
	}
	assertRemoved(t, fake, container, out)
	if v := s.must("ledger", "verify"); !strings.Contains(v, "chains intact") {
		t.Fatalf("ledger verify: %s", v)
	}
}

func TestDockerLocalFireDriftCheckRejects(t *testing.T) {
	t.Parallel()
	s, task, fake := driftNodeSandbox(t, true)
	s.write(".satelle/skills/stale.md", "---\nname: stale\ntype: skill\ndescription: s\n---\n\nRun `satelle nosuchcmd <id>`.\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "drift")

	r := s.run("remote", "fire", "docker-drift-check", task)
	t.Logf("fire:\n%s%s", r.out, r.err)
	if r.code == 0 {
		t.Fatalf("a fire whose drift check finds drift exited 0:\n%s", r.out)
	}
	m := nodeLine.FindStringSubmatch(r.out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s", r.out)
	}
	st := s.must("remote", "status", "docker-drift-check", task)
	for _, want := range []string{": concluded (node " + m[1] + ")", "verdict: reject", "gate drift-check reject:",
		".satelle/skills/stale.md:", "command: satelle nosuchcmd", "drift-check exit 1"} {
		if !strings.Contains(st, want) {
			t.Fatalf("status misses %q:\n%s", want, st)
		}
	}
	execs := executionsOf(s, task)
	if len(execs) != 1 || execs[0].Status != "backlog" {
		t.Fatalf("executions of %s = %+v, want one at backlog", task, execs)
	}
	n := 0
	for _, g := range gateRunsOf(t, s, execs[0].ID) {
		if g.Gate == "drift-check" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the drift check ran %d times", n)
	}
	if c := runLocalCount(t, fake); c != 1 {
		t.Fatalf("the node ran %d times", c)
	}
	assertRemoved(t, fake, m[2], r.out)
	s.must("ledger", "verify")
}

func TestDockerLocalFireDriftCheckOnDaemon(t *testing.T) {
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
	s, task, _ := driftNodeSandbox(t, false)
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
	r := s.run("remote", "fire", "docker-drift-check", task)
	if m := nodeLine.FindStringSubmatch(r.out); m != nil {
		name = m[2]
	}
	t.Logf("fire: exit %d\n%s%s", r.code, r.out, r.err)
	if name == "" {
		t.Fatalf("fire output names no node:\n%s%s", r.out, r.err)
	}
	st := s.must("remote", "status", "docker-drift-check", task)
	if !strings.Contains(st, ": concluded (node ") || !strings.Contains(st, "satelle: satelle ") ||
		!strings.Contains(st, "drift-check exit 0") && !strings.Contains(st, "drift-check exit 1") {
		t.Fatalf("status:\n%s", st)
	}
	for _, bad := range []string{"drift-check exit 2", "bash is not on PATH", "bash: not found"} {
		if strings.Contains(st, bad) {
			t.Fatalf("status carries %q:\n%s", bad, st)
		}
	}
	if ps, err := exec.Command(real, "ps", "-a", "--filter", "name=^"+name+"$", "-q").Output(); err != nil || strings.TrimSpace(string(ps)) != "" {
		t.Fatalf("docker ps -a still shows %s (%q, %v)", name, ps, err)
	}
	s.must("ledger", "verify")
}

func TestDriftReviewStepOneFiresTheNode(t *testing.T) {
	t.Parallel()
	block := shBlock(t, ".satelle/skills/drift-review.md", "## 1. Substrate against the binary", "tas_a45ba066")
	for _, want := range []string{
		"satelle remote fire docker-drift-check",
		`grep -o 'execution exe_[0-9a-f]*' "$rd/node-fire.txt"`,
		`select(.gate == "drift-check")`,
		`echo "drift-check exit $code" | tee "$rd/substrate.exit"`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("drift-review step 1 misses %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "bash scripts/drift-check.sh") {
		t.Errorf("drift-review step 1 still runs drift-check on the host:\n%s", block)
	}
}
