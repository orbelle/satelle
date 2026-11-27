package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/substrate"
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
			if err := config.WriteRepoPathMarker(r.Runtime, filepath.Dir(r.DataDir), r.gitEnv); err != nil {
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

func TestNodeHintRefusalPropagates(t *testing.T) {
	sites := []struct {
		name string
		call func(dir string) error
	}{
		{"loadDoctorInput", func(string) error { _, err := loadDoctorInput(""); return err }},
		{"projectServer", func(string) error { _, err := projectServer(""); return err }},
		{"repoArg", func(string) error { _, err := repoArg(nil); return err }},
		{"registeredRepos", func(string) error { _, err := registeredRepos(); return err }},
		{"localProjects", func(string) error { _, err := localProjects(); return err }},
		{"seedUI", func(dir string) error {
			c := &cobra.Command{}
			c.SetContext(context.Background())
			c.SetOut(io.Discard)
			return seedUI(c, dir)
		}},
	}
	register := func(t *testing.T, dir string) {
		t.Helper()
		gc, err := config.LoadGlobal()
		if err != nil {
			t.Fatal(err)
		}
		gc.Projects.Add(dir)
		if err := config.SaveGlobal(gc); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range sites {
		t.Run(s.name+" under a refused hint", func(t *testing.T) {
			dir := isolate(t)
			must(t, "init")
			register(t, dir)
			t.Setenv(config.NodeRunEnv, t.TempDir())
			if err := s.call(dir); !errors.Is(err, config.ErrNodeHint) {
				t.Fatalf("%s = %v, want the refused hint", s.name, err)
			}
		})
		t.Run(s.name+" with no hint and no .satelle", func(t *testing.T) {
			dir := isolate(t)
			register(t, dir)
			t.Setenv(config.NodeRunEnv, "")
			if err := s.call(dir); errors.Is(err, config.ErrNodeHint) {
				t.Fatalf("%s = %v, want today's no-repo reading", s.name, err)
			}
		})
	}
}

func TestCommandRefsUnderNodeHint(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	keyed := func(t *testing.T, sub, name, commands string) {
		t.Helper()
		writeFile(t, filepath.Join(sub, "principles", name+".md"), "---\nname: "+name+"\ndescription: d\ncommands: "+commands+"\n---\n\nbody\n")
	}
	root := NewRootCmd()
	find := func(t *testing.T, args ...string) *cobra.Command {
		t.Helper()
		c, _, err := root.Find(args)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	names := func(refs []substrate.Reference) []string {
		var out []string
		for _, r := range refs {
			out = append(out, r.Name)
		}
		return out
	}
	t.Run("a refused hint", func(t *testing.T) {
		t.Setenv(config.NodeRunEnv, t.TempDir())
		version := find(t, "version")
		if refs := commandRefs(root, version, ""); len(refs) != 0 {
			t.Fatalf("commandRefs under a refused hint = %v", names(refs))
		}
		if err := root.PersistentPostRunE(version, nil); err != nil {
			t.Fatalf("the post-run under a refused hint = %v", err)
		}
	})
	t.Run("a valid hint", func(t *testing.T) {
		keyed(t, filepath.Join(dir, ".satelle"), "own-keyed", "hook gate, ledger autosync, story list")
		sub := namedSubstrate(t)
		keyed(t, sub, "node-keyed", "hook gate, ledger autosync, story list")
		stampNodeRun(t, sub, dir)
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"hook", "gate"}, "own-keyed"},
			{[]string{"ledger", "autosync"}, "own-keyed"},
			{[]string{"story", "list"}, "node-keyed"},
		} {
			if got := names(commandRefs(root, find(t, tc.args...), "")); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("commandRefs for %s = %v, want [%s]", strings.Join(tc.args, " "), got, tc.want)
			}
		}
	})
}
