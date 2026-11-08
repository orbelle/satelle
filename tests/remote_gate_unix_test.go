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
	"time"
)

// A check gate placed on a docker-local remote (sto_da5c52e7): the fake docker
// of remote_docker_local_unix_test.go maps each node's container to a dir and
// runs its execs on this host (git is the host's), so the node fetches the
// pushed head from the bundle it is handed and runs the check there.

const fence = "```"

// gateWorkflow moves backlog -> done through the gate suite, a check placed on
// remote gatebox. extra is added to the gate's table.
func gateWorkflow(extra string) string {
	return `name = "default"

[states.backlog]
start = true

[states.done]
terminal = true

[gates.suite]
skill = "suite-check"
placement = "remote:gatebox"
` + extra + `

[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["suite"] }]
`
}

func checkSkillText(script string) string {
	return "---\nname: suite-check\ntype: check\n---\n\n" + fence + "sh\n" + script + "\n" + fence + "\n"
}

// gateboxDef is a docker-local definition with the given extra tables.
func gateboxDef(extra string) string {
	return `version = 1
name = "gatebox"
provider = "docker-local"
satelle_version = "1.0.0"

[machine]
image = "fake/node:1"

[drive]
kind = "item"

[lifecycle]
scope = "run"
` + extra
}

// gateRemote is one gate.run with where it ran.
type gateRemote struct {
	Transition string `json:"transition"`
	Gate       string `json:"gate"`
	Result     string `json:"result"`
	Notes      string `json:"notes"`
	Location   string `json:"location"`
}

func (s *sandbox) remoteRuns(id string) []gateRemote {
	s.t.Helper()
	var out []gateRemote
	for _, e := range s.ledger(id, "gate.run") {
		var g gateRemote
		if err := json.Unmarshal(e.Payload, &g); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, g)
	}
	return out
}

// gateSandbox is a repo with an origin, the gated workflow, the check skill
// and the gatebox definition committed and pushed on main, and the fake docker
// first on PATH (when withFake). host is a test-owned dir the check may use.
func gateSandbox(t *testing.T, gateExtra, script, def string, withFake bool) (s *sandbox, fake string) {
	t.Helper()
	s = newSandbox(t)
	origin := realDir(t, t.TempDir())
	s.exec("git", "", "init", "-q", "--bare", origin)
	s.git("remote", "add", "origin", origin)
	s.git("checkout", "-q", "-B", "main")
	s.must("init")
	s.write(".satelle/workflows/default.toml", gateWorkflow(gateExtra))
	s.write(".satelle/skills/suite-check.md", checkSkillText(script))
	s.write(".satelle/remotes/gatebox/remote.toml", def)
	s.must("validate")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "gated")
	s.git("push", "-q", "-u", "origin", "main")
	if withFake {
		fake = t.TempDir()
		script := strings.NewReplacer("@FAKE@", fake, "@SATELLE@", satelleBin).Replace(fakeDocker)
		writeFile(t, filepath.Join(fake, "docker"), script, 0o755)
		s.path = append([]string{fake}, s.path...)
	}
	return s, fake
}

// branchWork makes branch from main with one commit and pushes it unless
// local, leaving it checked out; it returns the commit.
func (s *sandbox) branchWork(branch string, push bool) string {
	s.t.Helper()
	s.git("checkout", "-q", "-B", branch, "main")
	s.write("work/"+branch+".txt", branch+"\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "work on "+branch)
	if push {
		s.git("push", "-q", "-u", "origin", branch)
	}
	return strings.TrimSpace(s.git("rev-parse", "HEAD"))
}

func fakeLog(t *testing.T, fake string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fake, "log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// presentDone presents id's move to done and returns the exit code and output.
func (s *sandbox) presentDone(id string) result {
	s.t.Helper()
	return s.run("story", "set", id, "--status", "done")
}

var nodeLoc = regexp.MustCompile(`^remote:(nod_[0-9a-f]{12})$`)

// AC1, AC4: the check runs on a node against the pushed head; the gate.run
// records the node, the head, the exit and the check's output and stdin; the
// node is started with no token and no server, set up before the fetch, and
// removed after the accept (scope run).
func TestRemoteGateRunsOnTheNodeAgainstThePushedHead(t *testing.T) {
	t.Parallel()
	host := realDir(t, t.TempDir())
	script := `[ -f ` + host + `/svc ] || { echo "no service"; exit 9; }
echo "head=$(git rev-parse HEAD)"
echo "stdin=$(cat)"
echo "env=$SATELLE_ITEM $SATELLE_FROM $SATELLE_TO"`
	def := gateboxDef("\n[setup]\non_create = [\"echo up > " + host + "/svc\"]\n")
	s, fake := gateSandbox(t, "", script, def, true)
	head := s.branchWork("feat", true)
	id := s.create("story", "--title", "remote gate").ID

	r := s.presentDone(id)
	if r.code != 0 {
		t.Fatalf("set: exit %d\n%s%s\nlog:\n%s", r.code, r.out, r.err, fakeLog(t, fake))
	}
	if st := s.get(id).Status; st != "done" {
		t.Fatalf("status %s", st)
	}
	runs := s.remoteRuns(id)
	if len(runs) != 1 || runs[0].Gate != "suite" || runs[0].Result != "accept" {
		t.Fatalf("gate runs %+v", runs)
	}
	g := runs[0]
	m := nodeLoc.FindStringSubmatch(g.Location)
	if m == nil {
		t.Fatalf("location %q", g.Location)
	}
	node := m[1]
	for _, want := range []string{
		"remote gatebox: node " + node + " at " + head[:12] + ": exit 0",
		"head=" + head,
		`"id":"` + id + `"`, `"from":"backlog"`, `"to":"done"`,
		"env=" + id + " backlog done",
	} {
		if !strings.Contains(g.Notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, g.Notes)
		}
	}
	log := fakeLog(t, fake)
	name := "satelle-node-" + node
	if strings.Contains(log, "SATELLE_NODE_TOKEN") || !strings.Contains(log, "-e SATELLE_SERVER= ") {
		t.Fatalf("the node was given a token or a server:\n%s", log)
	}
	order := []string{
		"run -d --name " + name + " ",
		"exec " + name + " sh -c echo up > " + host + "/svc",
		"cp ",
		"exec " + name + " git -C /node/work fetch -q /node/in/head.bundle refs/remotes/origin/feat",
		"exec " + name + " git -C /node/work checkout -q --detach " + head,
		"exec " + name + " sh /node/in/run-check /node",
		"cp " + name + ":/node/out/exit ",
		"rm -f " + name,
	}
	at := 0
	for _, want := range order {
		i := strings.Index(log[at:], want)
		if i < 0 {
			t.Fatalf("log lacks %q after offset %d:\n%s", want, at, log)
		}
		at += i + len(want)
	}
	if _, err := os.Stat(filepath.Join(fake, name)); !os.IsNotExist(err) {
		t.Fatalf("the accepted node's container is left: %v", err)
	}
}

// AC1: a check that exits 3 is a reject verdict with the exit and the output;
// the item does not move and the container is left, with how to remove it.
func TestRemoteGateRejectsOnTheCheckExit(t *testing.T) {
	t.Parallel()
	s, fake := gateSandbox(t, "", `echo "suite failed: 2 tests"; exit 3`, gateboxDef(""), true)
	s.branchWork("feat", true)
	id := s.create("story", "--title", "red suite").ID
	if r := s.presentDone(id); r.code == 0 {
		t.Fatalf("set passed:\n%s", r.out)
	}
	if st := s.get(id).Status; st != "backlog" {
		t.Fatalf("moved to %s", st)
	}
	g := s.remoteRuns(id)[0]
	node := nodeLoc.FindStringSubmatch(g.Location)
	if g.Result != "reject" || node == nil || !strings.Contains(g.Notes, ": exit 3\nsuite failed: 2 tests") ||
		!strings.Contains(g.Notes, "docker rm -f satelle-node-"+node[1]) {
		t.Fatalf("%+v", g)
	}
	if _, err := os.Stat(filepath.Join(fake, "satelle-node-"+node[1])); err != nil {
		t.Fatalf("the rejected node's container is gone: %v", err)
	}
}

// AC1: an unpushed head and a dirty tree each reject naming why, and no node
// starts.
func TestRemoteGateNeedsAPushedCleanHead(t *testing.T) {
	t.Parallel()
	s, fake := gateSandbox(t, "", "exit 0", gateboxDef(""), true)
	head := s.branchWork("local-only", false)
	id := s.create("story", "--title", "unpushed").ID
	r := s.presentDone(id)
	g := s.remoteRuns(id)
	if r.code == 0 || len(g) != 1 || g[0].Result != "reject" ||
		!strings.Contains(g[0].Notes, "remote gatebox: head "+head[:12]+" is not pushed to origin/local-only: push it first") {
		t.Fatalf("unpushed: %d %+v", r.code, g)
	}
	s.git("push", "-q", "-u", "origin", "local-only")
	s.write("stray.txt", "not committed\n")
	s.presentDone(id)
	g = s.remoteRuns(id)
	if len(g) != 2 || g[1].Result != "reject" || !strings.Contains(g[1].Notes, "uncommitted changes, which the node would not test") {
		t.Fatalf("dirty: %+v", g)
	}
	// Committed on top of the pushed branch, and not pushed: ahead of origin.
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "ahead")
	ahead := strings.TrimSpace(s.git("rev-parse", "HEAD"))
	s.presentDone(id)
	g = s.remoteRuns(id)
	if len(g) != 3 || g[2].Result != "reject" ||
		!strings.Contains(g[2].Notes, "head "+ahead[:12]+" is not pushed to origin/local-only: push it first") {
		t.Fatalf("ahead of origin: %+v", g)
	}
	if log := fakeLog(t, fake); strings.Contains(log, "run -d") {
		t.Fatalf("a node started:\n%s", log)
	}
	if st := s.get(id).Status; st != "backlog" {
		t.Fatalf("moved to %s", st)
	}
}

// AC2: two stories' remote gates run at the same time on two nodes, each on
// its own collected branch's head, and the session's other commands answer
// while they run.
func TestRemoteGatesRunAtOnceOnTwoNodes(t *testing.T) {
	t.Parallel()
	host := realDir(t, t.TempDir())
	script := `echo "head=$(git rev-parse HEAD)"
touch ` + host + `/started-$SATELLE_ITEM
i=0
while [ ! -f ` + host + `/release ] && [ $i -lt 600 ]; do sleep 0.1; i=$((i+1)); done
[ -f ` + host + `/release ] || { echo "never released"; exit 4; }`
	s, fake := gateSandbox(t, "", script, gateboxDef(""), true)
	heads := map[string]string{}
	ids := map[string]string{}
	for _, b := range []string{"one", "two"} {
		heads[b] = s.branchWork(b, true)
	}
	s.git("checkout", "-q", "main")
	for _, b := range []string{"one", "two"} {
		ids[b] = s.create("story", "--title", "story "+b).ID
		s.must("story", "collect", ids[b], "--branch", b)
	}
	release := func() { _ = os.WriteFile(filepath.Join(host, "release"), nil, 0o644) }
	t.Cleanup(release)

	type done struct {
		b   string
		err error
		out string
	}
	exited := make(chan done, 2)
	for _, b := range []string{"one", "two"} {
		cmd := exec.Command(satelleBin, "story", "set", ids[b], "--status", "done")
		cmd.Dir, cmd.Env = s.dir, s.environ()
		go func() {
			out, err := cmd.CombinedOutput()
			exited <- done{b, err, string(out)}
		}()
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, e1 := os.Stat(filepath.Join(host, "started-"+ids["one"]))
		_, e2 := os.Stat(filepath.Join(host, "started-"+ids["two"]))
		if e1 == nil && e2 == nil {
			break
		}
		select {
		case d := <-exited:
			t.Fatalf("%s returned before both checks ran: %v\n%s\nlog:\n%s", d.b, d.err, d.out, fakeLog(t, fake))
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("both checks never ran at once\nlog:\n%s", fakeLog(t, fake))
		}
	}
	// Both checks are in flight: two node containers exist at once.
	entries, err := os.ReadDir(fake)
	if err != nil {
		t.Fatal(err)
	}
	var nodes []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "satelle-node-nod_") {
			nodes = append(nodes, e.Name())
		}
	}
	if len(nodes) != 2 {
		t.Fatalf("containers in flight %v", nodes)
	}
	// The session is not blocked.
	for _, args := range [][]string{{"story", "list"}, {"story", "get", ids["one"]}} {
		start := time.Now()
		s.must(args...)
		if d := time.Since(start); d > 10*time.Second {
			t.Fatalf("satelle %v took %v while the gates ran", args, d)
		}
	}
	release()
	for range 2 {
		d := <-exited
		if d.err != nil {
			t.Fatalf("%s: %v\n%s", d.b, d.err, d.out)
		}
	}
	locs := map[string]bool{}
	for b, other := range map[string]string{"one": "two", "two": "one"} {
		g := s.remoteRuns(ids[b])
		if len(g) != 1 || g[0].Result != "accept" || !strings.Contains(g[0].Notes, "head="+heads[b]) ||
			strings.Contains(g[0].Notes, heads[other]) || nodeLoc.FindString(g[0].Location) == "" {
			t.Fatalf("%s: %+v", b, g)
		}
		locs[g[0].Location] = true
		if st := s.get(ids[b]).Status; st != "done" {
			t.Fatalf("%s is %s", b, st)
		}
	}
	if len(locs) != 2 {
		t.Fatalf("both gates ran on one node: %v", locs)
	}
}

// AC3, AC4: every way a node gives no result rejects naming its cause, never
// passes, and the item stays.
func TestRemoteGateFailuresReject(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, gate, script, def, flag string
		noDocker                      bool
		wants                         []string
		noRun, removed                bool
	}{
		{name: "start fails", flag: "fail-run", script: "exit 0", def: gateboxDef(""),
			wants: []string{"remote gatebox: node failed to start: docker run: ", "the fake daemon refused the run"}},
		{name: "setup fails", script: "exit 0", def: gateboxDef("\n[setup]\non_create = [\"false\"]\n"),
			wants: []string{"node failed to start: setup on_create command 1 failed", "docker rm -f satelle-node-"}},
		{name: "timeout", gate: `timeout = "2s"`, script: "sleep 30", def: gateboxDef(""), removed: true,
			wants: []string{"remote gatebox: timed out after 2s (gates.suite timeout)", "cut while running the check on node nod_", "is removed"}},
		{name: "no result", flag: "no-exit", script: "exit 0", def: gateboxDef(""),
			wants: []string{"remote gatebox: the node returned no result"}},
		{name: "head does not check out", flag: "bad-bundle", script: "exit 0", def: gateboxDef(""),
			wants: []string{"remote gatebox: the node could not check out the head", "(git fetch): docker exec: exit status"}},
		{name: "docker absent", noDocker: true, script: "exit 0", def: gateboxDef(""),
			wants: []string{"node failed to start: docker run: ", "executable file not found"}},
		{name: "secrets", script: "exit 0", def: gateboxDef("\n[secrets]\nnames = [\"API_KEY\"]\n"), noRun: true,
			wants: []string{"remote gatebox: [secrets] is not served to a local node"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, fake := gateSandbox(t, c.gate, c.script, c.def, !c.noDocker)
			if c.noDocker {
				bin := t.TempDir()
				gitPath, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
					t.Fatal(err)
				}
				s.env["PATH"] = bin
			}
			if c.flag != "" {
				writeFile(t, filepath.Join(fake, c.flag), "", 0o644)
			}
			s.branchWork("feat", true)
			id := s.create("story", "--title", c.name).ID
			start := time.Now()
			r := s.presentDone(id)
			if r.code == 0 {
				t.Fatalf("passed:\n%s", r.out)
			}
			if c.name == "timeout" && time.Since(start) > 20*time.Second {
				t.Fatalf("the timeout took %v", time.Since(start))
			}
			if st := s.get(id).Status; st != "backlog" {
				t.Fatalf("moved to %s", st)
			}
			g := s.remoteRuns(id)
			if len(g) != 1 || g[0].Result != "reject" {
				t.Fatalf("%+v", g)
			}
			for _, w := range c.wants {
				if !strings.Contains(g[0].Notes, w) {
					t.Fatalf("notes lack %q:\n%s", w, g[0].Notes)
				}
			}
			if c.noDocker {
				return
			}
			log := fakeLog(t, fake)
			if c.noRun && strings.Contains(log, "run -d") {
				t.Fatalf("a node started:\n%s", log)
			}
			if c.removed {
				m := nodeLoc.FindStringSubmatch(g[0].Location)
				if m == nil || !strings.Contains(log, "rm -f satelle-node-"+m[1]) {
					t.Fatalf("the cut node was not removed (%q):\n%s", g[0].Location, log)
				}
			}
		})
	}
}

// AC4 on a real daemon: a service the definition's setup starts is up beside
// the check, which runs against the pushed head fetched from the bundle, with
// no network. Needs a docker daemon and a local image with git that runs as
// root (the repo's satelle-node:local, or alpine/git); it never pulls.
func TestRemoteGateServiceOnDaemon(t *testing.T) {
	t.Parallel()
	real, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker is not installed")
	}
	if out, err := exec.Command(real, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unavailable: %v: %s", err, out)
	}
	image := ""
	for _, img := range []string{"satelle-node:local", "alpine/git:latest", "alpine/git"} {
		if exec.Command(real, "image", "inspect", img).Run() != nil {
			continue
		}
		probe := exec.Command(real, "run", "--rm", "--network", "none", "--entrypoint", "sh", img, "-c", `command -v git >/dev/null && [ "$(id -u)" = 0 ]`)
		if probe.Run() == nil {
			image = img
			break
		}
	}
	if image == "" {
		t.Skip("no local image with git running as root (satelle-node:local, alpine/git); not pulling one")
	}
	def := strings.Replace(gateboxDef(`
[setup]
on_create = ["nohup sh -c 'while :; do date +%s > /tmp/svc.beat; sleep 1; done' >/dev/null 2>&1 &"]
`), `image = "fake/node:1"`, `image = "`+image+`"`, 1)
	script := `b=$(cat /tmp/svc.beat) || { echo "no service"; exit 9; }
now=$(date +%s)
[ $((now - b)) -le 5 ] || { echo "stale beat $b at $now"; exit 8; }
echo "service beat fresh; head=$(git rev-parse HEAD)"`
	s, _ := gateSandbox(t, `timeout = "2m"`, script, def, false)
	head := s.branchWork("feat", true)
	id := s.create("story", "--title", "daemon gate").ID
	r := s.presentDone(id)
	g := s.remoteRuns(id)
	for _, x := range g {
		if m := nodeLoc.FindStringSubmatch(x.Location); m != nil {
			name := "satelle-node-" + m[1]
			t.Cleanup(func() { _ = exec.Command(real, "rm", "-f", name).Run() })
		}
	}
	if r.code != 0 || len(g) != 1 || g[0].Result != "accept" || !strings.Contains(g[0].Notes, "service beat fresh; head="+head) {
		t.Fatalf("exit %d\n%s%s\n%+v", r.code, r.out, r.err, g)
	}
	t.Logf("gate.run on %s: %s", image, g[0].Notes)
}

// The head is a merge commit (origin/main merged in) on a linked worktree of a
// shallow clone, as a story's worktree is: the bundle of its ref ends at the
// clone's shallow commits, and the node marks them shallow as well, so its
// fetch does not look for their parents, and the check runs on the head.
func TestRemoteGateOnAShallowWorktreeMergeHead(t *testing.T) {
	t.Parallel()
	s, fake := gateSandbox(t, "", `echo "head=$(git rev-parse HEAD)"`, gateboxDef(""), true)
	for _, n := range []string{"a", "b", "c"} {
		s.write("hist/"+n+".txt", n+"\n")
		s.git("add", "-A")
		s.git("commit", "-q", "-m", "history "+n)
	}
	s.git("push", "-q", "origin", "main")
	// Shallow in place, as a --depth clone is: its history ends at main.
	s.git("fetch", "-q", "--depth=1", "origin", "main")
	if strings.TrimSpace(s.git("rev-parse", "--is-shallow-repository")) != "true" {
		t.Fatal("the sandbox is not shallow")
	}
	wt := filepath.Join(realDir(t, t.TempDir()), "wt")
	s.git("worktree", "add", "-q", "-b", "feat", wt, "main")
	main := s.dir
	s.dir = wt
	s.write("work/feat.txt", "feat\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "work on feat")
	s.dir = main
	s.write("hist/d.txt", "d\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "main moves on")
	s.git("push", "-q", "origin", "main")
	s.dir = wt
	s.git("merge", "-q", "--no-ff", "-m", "merge main", "main")
	s.git("push", "-q", "-u", "origin", "feat")
	head := strings.TrimSpace(s.git("rev-parse", "HEAD"))
	if parents := strings.Fields(s.git("rev-list", "--parents", "-n1", "HEAD")); len(parents) != 3 {
		t.Fatalf("the head is not a merge: %v", parents)
	}
	id := s.create("story", "--title", "shallow merge head").ID
	r := s.presentDone(id)
	g := s.remoteRuns(id)
	if r.code != 0 || len(g) != 1 || g[0].Result != "accept" || !strings.Contains(g[0].Notes, "head="+head) {
		t.Fatalf("exit %d\n%s%s\n%+v\nlog:\n%s", r.code, r.out, r.err, g, fakeLog(t, fake))
	}
	if log := fakeLog(t, fake); !strings.Contains(log, " cp /node/in/shallow /node/work/.git/shallow") {
		t.Fatalf("the node was not told the shallow commits:\n%s", log)
	}
}
