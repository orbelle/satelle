package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
)

func stampNodeRun(t *testing.T, substrate, tree string) (runDir, runtime string) {
	t.Helper()
	anc := procgroup.Ancestors(os.Getpid())
	if len(anc) == 0 {
		t.Skip("this platform reports no process ancestry")
	}
	runDir = canonical(t.TempDir())
	runtime = filepath.Join(runDir, "runtime")
	if err := os.MkdirAll(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.SubstrateDigest(substrate)
	if err != nil {
		t.Fatal(err)
	}
	s := config.RuntimeStamp{Kind: config.RuntimeNode, Node: "n1", Substrate: canonical(substrate), Tree: canonical(tree), Digest: digest, Process: anc[0]}
	if err := config.WriteRuntimeStamp(runtime, s); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.NodeRunEnv, runDir)
	return runDir, runtime
}

func namedSubstrate(t *testing.T) string {
	t.Helper()
	sub := filepath.Join(canonical(t.TempDir()), "substrate")
	if err := kernel.Init(sub); err != nil {
		t.Fatal(err)
	}
	return sub
}

func gitTree(t *testing.T) string {
	t.Helper()
	dir := canonical(t.TempDir())
	gittest.Git(t, dir, "init", "-q")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestResolveRepoNodeRun(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	t.Run("the tree's own substrate", func(t *testing.T) {
		_, runtime := stampNodeRun(t, filepath.Join(dir, ".satelle"), dir)
		r, err := resolveRepo("", os.Environ(), true)
		if err != nil {
			t.Fatal(err)
		}
		if r.DataDir != filepath.Join(dir, ".satelle") || r.Tree != dir || r.Runtime != runtime || r.Node != "n1" {
			t.Fatalf("resolveRepo = %+v, want the node runtime %s", r, runtime)
		}
	})
	t.Run("a tree with no .satelle", func(t *testing.T) {
		tree, sub := gitTree(t), namedSubstrate(t)
		_, runtime := stampNodeRun(t, sub, tree)
		r, err := resolveRepo(tree, os.Environ(), true)
		if err != nil {
			t.Fatal(err)
		}
		if r.DataDir != sub || r.Tree != tree || r.Runtime != runtime {
			t.Fatalf("resolveRepo = %+v, want %s over %s on %s", r, sub, tree, runtime)
		}
		if err := os.WriteFile(filepath.Join(sub, "agents.local.toml"), []byte("# planted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveRepo(tree, os.Environ(), true); err == nil || !strings.Contains(err.Error(), "digest") {
			t.Fatalf("a named substrate changed after the stamp: %v, want a digest refusal", err)
		}
	})
	t.Run("outside the node's tree", func(t *testing.T) {
		stampNodeRun(t, namedSubstrate(t), gitTree(t))
		r, err := resolveRepo(dir, os.Environ(), true)
		if err != nil {
			t.Fatal(err)
		}
		if r.Node != "" || r.DataDir != filepath.Join(dir, ".satelle") {
			t.Fatalf("resolveRepo = %+v, want the tree's own substrate on the machine runtime", r)
		}
	})
	t.Run("not node-aware", func(t *testing.T) {
		stampNodeRun(t, namedSubstrate(t), dir)
		r, err := findOwnRepo(dir)
		if err != nil {
			t.Fatal(err)
		}
		if r.Node != "" || r.DataDir != filepath.Join(dir, ".satelle") || r.Runtime != config.RuntimeDir(dir) {
			t.Fatalf("findOwnRepo = %+v, want the tree's own substrate on %s", r, config.RuntimeDir(dir))
		}
	})
}

func TestResolveRepoTrackedNodeShapeDoesNotShadow(t *testing.T) {
	isolate(t)
	tree, sub := gitTree(t), namedSubstrate(t)
	fake := filepath.Join(tree, "fake")
	if err := kernel.Init(filepath.Join(fake, ".satelle")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fake, "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteRuntimeStamp(filepath.Join(fake, "runtime"), config.RuntimeStamp{Kind: config.RuntimeNode, Substrate: filepath.Join(fake, ".satelle"), Tree: tree, Digest: "x", Process: procgroup.Self()}); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, tree, "add", ".")
	gittest.Git(t, tree, "commit", "-q", "-m", "node-shaped")
	_, runtime := stampNodeRun(t, sub, tree)
	r, err := resolveRepo(fake, os.Environ(), true)
	if err != nil {
		t.Fatal(err)
	}
	if r.DataDir != sub || r.Runtime != runtime {
		t.Fatalf("resolveRepo in a tracked node-shaped directory = %+v, want %s on %s", r, sub, runtime)
	}
}

func TestResolveRepoWithholdsBeforeGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the git recorder is a shell script")
	}
	dir := isolate(t)
	must(t, "init")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "git.env")
	script := "#!/bin/sh\nenv >> '" + log + "'\necho --- >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_PROBE_VALUE", "s3cret")
	t.Setenv(config.NodeWithholdEnv, "GIT_PROBE_VALUE")
	check := func(t *testing.T, name string, r repo) {
		t.Helper()
		if r.Node == "" {
			if err := config.WriteRepoPathMarkerEnv(r.Runtime, filepath.Dir(r.DataDir), r.gitEnv); err != nil {
				t.Fatal(err)
			}
		}
		b, err := os.ReadFile(log)
		if os.IsNotExist(err) && r.Node != "" {
			return
		}
		if err != nil {
			t.Fatalf("%s: git never ran: %v", name, err)
		}
		if strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "GIT_PROBE_VALUE=") {
			t.Fatalf("%s: a git command resolution ran saw the withheld secret:\n%s", name, b)
		}
		_ = os.Remove(log)
	}
	t.Run("own path", func(t *testing.T) {
		t.Setenv(config.NodeRunEnv, "")
		r, err := resolveRepo(dir, os.Environ(), true)
		if err != nil {
			t.Fatal(err)
		}
		check(t, "own path", r)
	})
	t.Run("node path", func(t *testing.T) {
		stampNodeRun(t, filepath.Join(dir, ".satelle"), dir)
		r, err := resolveRepo(dir, os.Environ(), true)
		if err != nil || r.Node == "" {
			t.Fatalf("resolveRepo = %+v, %v; want the node runtime", r, err)
		}
		check(t, "node path", r)
	})
}
