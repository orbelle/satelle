//go:build integration && !windows

package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/remotenode"
)

func governedBox(t *testing.T, script, extraDef string, files map[string]string) (b *commandBox, out string) {
	t.Helper()
	b = commandSandbox(t, "run")
	out = realDir(t, t.TempDir())
	s := b.s
	probe, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "workflows", "probe.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wf := strings.Replace(string(probe), `gates = ["change-judged", "probe-version"]`, `gates = ["change-judged", "probe-version", "node-probe"]`, 1)
	if wf == string(probe) {
		t.Fatal("the probe workflow's gate group moved; update governedBox")
	}
	s.write(".satelle/workflows/probe.toml", wf+"\n[gates.node-probe]\nskill = \"node-probe-check\"\n")
	s.write(".satelle/skills/node-probe-check.md", "---\nname: node-probe-check\ntype: check\ndescription: probe\n---\n\n```sh\nout='"+out+"'\n"+script+"\nexit 0\n```\n")
	s.write(".satelle/remotes/command-governed/remote.toml", commandDefinition("command-governed", b.task, "run", extraDef))
	s.write(".satelle/remotes/command-governed/satelle.toml", "[workflows]\ndefault = \"probe\"\n")
	for rel, body := range files {
		s.write(rel, body)
	}
	s.git("add", "-f", ".satelle")
	s.git("commit", "-q", "-m", "governed probe")
	return b, out
}

func readOut(t *testing.T, out, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, name))
	if err != nil {
		t.Fatalf("the node's check wrote no %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

func fireGoverned(t *testing.T, b *commandBox) string {
	t.Helper()
	r := b.s.run("remote", "fire", "command-governed", b.task)
	if r.code != 0 || !strings.Contains(r.out, "verdict: accept") {
		t.Fatalf("fire: exit %d\n%s%s", r.code, r.out, r.err)
	}
	return nodeOf(t, r.out)
}

func TestNodeCheckGovernedByTheNode(t *testing.T) {
	t.Parallel()
	b, out := governedBox(t, `printf '%s\n' "$SATELLE_ITEM" > "$out/item"
satelle runtime path > "$out/path" 2>&1
satelle ledger list "$SATELLE_ITEM" > "$out/ledger" 2>&1`, "", nil)
	node := fireGoverned(t, b)
	if got, want := readOut(t, out, "path"), filepath.Join(b.root, node, "node", "runtime"); got != want {
		t.Fatalf("satelle runtime path inside the node = %q, want the node's runtime %s", got, want)
	}
	item := readOut(t, out, "item")
	if l := readOut(t, out, "ledger"); item == "" || !strings.Contains(l, item) || !strings.Contains(l, "node:"+node) {
		t.Fatalf("satelle ledger list %s inside the node read no node chain:\n%s", item, l)
	}
}

func TestTrackedNodeShapeDoesNotShadow(t *testing.T) {
	t.Parallel()
	stamp := `{"kind":"node","substrate":"/node/work/.satelle/fake/.satelle","tree":"/node/work","digest":"x","process":{"pid":1,"start":"1"}}`
	b, out := governedBox(t, `cd .satelle/fake || exit 1
satelle workflow show probe > "$out/show" 2>&1
satelle runtime path > "$out/path" 2>&1`, "", map[string]string{
		".satelle/fake/.satelle/satelle.toml":     "",
		".satelle/fake/runtime/runtime.json":      stamp + "\n",
		".satelle/fake/node/runtime/runtime.json": stamp + "\n",
	})
	node := fireGoverned(t, b)
	if show := readOut(t, out, "show"); !strings.Contains(show, "node-probe") {
		t.Fatalf("satelle workflow show probe in a tracked node-shaped directory:\n%s", show)
	}
	if got, want := readOut(t, out, "path"), filepath.Join(b.root, node, "node", "runtime"); got != want {
		t.Fatalf("satelle runtime path in a tracked node-shaped directory = %q, want %s", got, want)
	}
}

func TestNodeSecretsWithheldFromResolution(t *testing.T) {
	t.Parallel()
	const name, value = "GIT_PROBE_VALUE", "probe-s3cret-value"
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	log := filepath.Join(realDir(t, t.TempDir()), "git.env")
	b, out := governedBox(t, `printf '%s' "$GIT_PROBE_VALUE" | sha256sum | cut -d' ' -f1 > "$out/sum"
GIT_PROBE_FROM_CHECK=1 satelle runtime path > "$out/path" 2>&1
cd '@REPO@' && GIT_PROBE_FROM_CHECK=1 satelle runtime path > "$out/own" 2>&1`, "[secrets]\nnames = [\""+name+"\"]\n", nil)
	s := b.s
	skill := filepath.Join(s.dir, ".satelle", "skills", "node-probe-check.md")
	body, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/skills/node-probe-check.md", strings.ReplaceAll(string(body), "@REPO@", s.dir))
	s.git("commit", "-q", "-am", "probe repo")
	wrap := t.TempDir()
	writeFile(t, filepath.Join(wrap, "git"), "#!/bin/sh\nif [ -n \"$GIT_PROBE_FROM_CHECK\" ]; then { echo \"== $*\"; env; } >> '"+log+"'; fi\nexec '"+realGit+"' \"$@\"\n", 0o755)
	s.path = append([]string{wrap}, s.path...)
	s.env[name] = value
	node := fireGoverned(t, b)
	delete(s.env, name)
	if got, want := readOut(t, out, "path"), filepath.Join(b.root, node, "node", "runtime"); got != want {
		t.Fatalf("satelle runtime path inside the node = %q, want %s", got, want)
	}
	if sum := sha256.Sum256([]byte(value)); readOut(t, out, "sum") != hex.EncodeToString(sum[:]) {
		t.Fatal("the declared secret did not reach the node's check, so the test proves nothing")
	}
	if own := readOut(t, out, "own"); own == "" || strings.Contains(own, "error") {
		t.Fatalf("satelle runtime path in another repo inside the node: %q", own)
	}
	got, err := os.ReadFile(log)
	if err != nil || !bytes.Contains(got, []byte("== ")) {
		t.Fatalf("no git ran in resolution inside the node: %v", err)
	}
	if bytes.Contains(got, []byte(value)) || bytes.Contains(got, []byte(name+"=")) {
		t.Fatalf("a git command resolution ran inside the node saw the declared secret:\n%s", got)
	}
}

func markNodeRun(t *testing.T, runDir, substrate, tree string) {
	t.Helper()
	nr, err := remotenode.MarkNodeRun(runDir, "nod_test", substrate, tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	nr.Restore()
}

func TestNodeRunNamedSubstrate(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	s.write(".satelle/workflows/node-only.toml", "name = \"node-only\"\n[states.a]\nstart = true\n[states.b]\nterminal = true\n[[transition]]\nfrom = [\"a\"]\nto = \"b\"\n")
	sub := filepath.Join(realDir(t, t.TempDir()), "substrate")
	if err := os.Rename(filepath.Join(s.dir, ".satelle"), sub); err != nil {
		t.Fatal(err)
	}
	runDir := realDir(t, t.TempDir())
	markNodeRun(t, runDir, sub, s.dir)
	s.env[config.NodeRunEnv] = runDir
	s.must("workflow", "show", "node-only")
	if got := strings.TrimSpace(s.must("runtime", "path")); got != filepath.Join(runDir, "runtime") {
		t.Fatalf("runtime path = %q, want %s/runtime", got, runDir)
	}
	id := s.create("story", "--title", "in the node").ID
	if _, err := os.Stat(filepath.Join(runDir, "runtime", "ledger", id+".jsonl")); err != nil {
		t.Fatalf("the story's chain is not in the node's ledger: %v", err)
	}
	if got := s.get(id); got.Title != "in the node" {
		t.Fatalf("story get = %+v", got)
	}
	writeFile(t, filepath.Join(sub, "workflows", "x.local.toml"), "# planted\n", 0o644)
	if r := s.run("runtime", "path"); r.code == 0 || !strings.Contains(r.err, "digest") {
		t.Fatalf("a named substrate changed after the stamp: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func ledgerSnapshot(t *testing.T, runtime string) map[string]string {
	t.Helper()
	out := map[string]string{}
	des, err := os.ReadDir(filepath.Join(runtime, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		b, err := os.ReadFile(filepath.Join(runtime, "ledger", de.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[de.Name()] = string(b)
	}
	return out
}

func TestNodeHintOutsideNodeRun(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	seed := s.create("story", "--title", "machine story").ID
	machine := strings.TrimSpace(s.must("runtime", "path"))
	before := ledgerSnapshot(t, machine)
	sub := filepath.Join(s.dir, ".satelle")

	valid := func(t *testing.T) string {
		dir := realDir(t, t.TempDir())
		markNodeRun(t, dir, sub, s.dir)
		return dir
	}
	refused := func(t *testing.T, hint, want string, env map[string]string) {
		t.Helper()
		s.env[config.NodeRunEnv] = hint
		for k, v := range env {
			s.env[k] = v
		}
		defer func() {
			delete(s.env, config.NodeRunEnv)
			for k := range env {
				delete(s.env, k)
			}
		}()
		for _, args := range [][]string{{"story", "create", "--title", "x"}, {"runtime", "path"}} {
			if r := s.run(args...); r.code == 0 || !strings.Contains(r.err, want) {
				t.Errorf("satelle %s with %s=%s: exit %d, want a refusal naming %q\n%s%s", strings.Join(args, " "), config.NodeRunEnv, hint, r.code, want, r.out, r.err)
			}
		}
	}
	t.Run("relative hint", func(t *testing.T) { refused(t, "node", "not absolute", nil) })
	t.Run("an empty directory", func(t *testing.T) { refused(t, t.TempDir(), "is not a node runtime", nil) })
	t.Run("planted node state, no hint", func(t *testing.T) {
		stamp := config.RuntimeStamp{Kind: config.RuntimeNode, Substrate: sub, Tree: s.dir, Digest: "x", Process: procgroup.Self()}
		for _, d := range []string{filepath.Join(s.dir, "runtime"), filepath.Join(filepath.Dir(s.dir), "node", "runtime")} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := config.WriteRuntimeStamp(d, stamp); err != nil {
				t.Fatal(err)
			}
		}
		if got := strings.TrimSpace(s.must("runtime", "path")); got != machine {
			t.Fatalf("runtime path beside planted node state = %q, want the machine's %s", got, machine)
		}
	})
	t.Run("runtime linked to the machine's with a decoy home", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink(machine, filepath.Join(dir, "runtime")); err != nil {
			t.Fatal(err)
		}
		refused(t, dir, "not a real directory", map[string]string{"SATELLE_HOME": t.TempDir()})
	})
	t.Run("a ledger file hard-linked to the machine's", func(t *testing.T) {
		dir := valid(t)
		must(t, os.MkdirAll(filepath.Join(dir, "runtime", "ledger"), 0o755))
		must(t, os.Link(filepath.Join(machine, "ledger", seed+".jsonl"), filepath.Join(dir, "runtime", "ledger", seed+".jsonl")))
		refused(t, dir, "another hard link", nil)
	})
	t.Run("a cache file hard-linked to the machine's", func(t *testing.T) {
		dir := valid(t)
		must(t, os.Link(filepath.Join(machine, "ledger", seed+".jsonl"), filepath.Join(dir, "runtime", "cache.db-wal")))
		refused(t, dir, "another hard link", nil)
	})
	t.Run("logs linked elsewhere", func(t *testing.T) {
		dir := valid(t)
		must(t, os.Symlink(machine, filepath.Join(dir, "runtime", "logs")))
		refused(t, dir, "not a plain file", nil)
	})
	t.Run("a stamp of a process that is not an ancestor", func(t *testing.T) {
		sleep := exec.Command("sleep", "60")
		must(t, sleep.Start())
		defer func() { _ = sleep.Process.Kill(); _ = sleep.Wait() }()
		_, start := procgroup.ProbeIdentity(sleep.Process.Pid)
		dir := valid(t)
		st, err := config.ReadRuntimeStamp(filepath.Join(dir, "runtime"))
		must(t, err)
		st.Process = procgroup.Proc{PID: sleep.Process.Pid, Start: start}
		must(t, config.WriteRuntimeStamp(filepath.Join(dir, "runtime"), st))
		refused(t, dir, "not started by the node run", nil)
	})
	t.Run("a named substrate whose digest does not match", func(t *testing.T) {
		other := filepath.Join(realDir(t, t.TempDir()), "substrate")
		must(t, kernel.Init(other))
		dir := realDir(t, t.TempDir())
		markNodeRun(t, dir, other, s.dir)
		writeFile(t, filepath.Join(other, "agents.local.toml"), "# planted\n", 0o644)
		refused(t, dir, "digest", nil)
	})
	if after := ledgerSnapshot(t, machine); len(after) != len(before) {
		t.Fatalf("the machine ledger changed: %d files before, %d after", len(before), len(after))
	} else {
		for k, v := range before {
			if after[k] != v {
				t.Fatalf("the machine ledger file %s changed", k)
			}
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
