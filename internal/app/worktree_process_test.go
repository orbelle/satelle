package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/testutil"
)

// sty_ddbe2669: a linked worktree executes the main tree's process of record.
// The data dir is never copied into the worktree. Discovery stays on a literal
// .satelle.

func isolateProcessEnv(t *testing.T) string {
	t.Helper()
	home := testutil.IsolateHome(t)
	t.Setenv("SATELLE_CONFIG", "")
	_ = os.Unsetenv("SATELLE_CONFIG")
	t.Setenv("SATELLE_SERVER_ENDPOINT", "none")
	t.Setenv(config.SessionEnv, "")
	_ = os.Unsetenv(config.SessionEnv)
	return home
}

func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// linkedPair is a main tree and a linked worktree placed outside it. The
// worktree checkout contains only what was committed (a README).
func linkedPair(t *testing.T) (main, wt string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "proj")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, main, "init", "-q")
	gitAt(t, main, "config", "user.email", "t@example.com")
	gitAt(t, main, "config", "user.name", "t")
	gitAt(t, main, "add", "-A")
	gitAt(t, main, "commit", "-q", "-m", "init")
	wt = filepath.Join(base, "wt", "sty_outside")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, main, "worktree", "add", "-q", "-b", "sty_outside", wt, "HEAD")
	return main, wt
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func resolved(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		return r
	}
	return abs
}

// owningRoot is the main working tree: the parent of `git rev-parse
// --git-common-dir`, absolutised and symlink-resolved the same way
// WriteRepoPathMarker records it. filepath.Dir of the marker file is the
// runtime dir, and filepath.Dir of a nested data dir is not this root.
func owningRoot(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		cmd = exec.Command("git", "-C", repo, "rev-parse", "--git-common-dir")
		out, err = cmd.Output()
		if err != nil {
			t.Fatalf("git common dir: %v", err)
		}
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repo, common)
	}
	abs, err := filepath.Abs(common)
	if err != nil {
		t.Fatal(err)
	}
	if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = r
	}
	return filepath.Dir(filepath.Clean(abs))
}

const relocatedRel = "cfg/satelle"

const relocatedAgents = `[reviewer]
role = "reviewer"
model = "main-tree-reviewer"
`

func writeRelocatedProcess(t *testing.T, main string) string {
	t.Helper()
	data := filepath.Join(main, filepath.FromSlash(relocatedRel))
	writeFile(t, filepath.Join(main, ".satelle", "satelle.toml"),
		"data_dir = \""+relocatedRel+"\"\n\n[review]\ngate_create = false\n")
	writeFile(t, filepath.Join(data, "workflows", "done.toml"),
		"[meta]\nname = \"done\"\ntype = \"workflow\"\n\n# MAIN-PROCESS-DONE\n")
	writeFile(t, filepath.Join(data, "workflows", "step.toml"),
		"[meta]\nname = \"step\"\ntype = \"workflow\"\n\n# MAIN-PROCESS-STEP\n")
	writeFile(t, filepath.Join(data, "workflows", "agents.toml"), relocatedAgents)
	writeFile(t, filepath.Join(data, "skills", "place.md"),
		"---\nname: place\ntype: skill\ndescription: main skill\n---\n\nMAIN-PROCESS-SKILL\n")
	writeFile(t, filepath.Join(data, "principles", "place.md"),
		"---\nname: place\ntype: principle\ndescription: main principle\n---\n\nMAIN-PROCESS-PRINCIPLE\n")
	writeFile(t, filepath.Join(data, "documents", "place.md"),
		"# Place\n\nMAIN-PROCESS-DOCUMENT\n")
	writeFile(t, filepath.Join(data, "constitution.md"), "MAIN-PROCESS-CONSTITUTION\n")
	return data
}

// AC3: for one worktree, the runtime-plane repo marker and the process
// read-plane name the same main-tree path.
func TestWorktreeMarkerAndProcessRootNameTheMainTree(t *testing.T) {
	isolateProcessEnv(t)
	main, wt := linkedPair(t)
	writeRelocatedProcess(t, main)
	owning := owningRoot(t, wt)

	t.Chdir(wt)
	a, err := Open()
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	marker := config.ReadRepoPathMarker(a.RuntimeDir)
	process := a.ProcessRoot
	runtime := a.RuntimeDir
	dataDir := a.ProcessDataDir
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	if marker == "" {
		t.Fatal("repo.path marker is empty")
	}
	if marker != process || marker != owning {
		t.Fatalf("marker %q, process root %q, owning root %q must be the same path", marker, process, owning)
	}
	if filepath.Dir(filepath.Join(runtime, config.RepoPathMarkerName)) == owning {
		t.Fatalf("filepath.Dir of the marker file is the owning root; that dir is the runtime plane")
	}
	if filepath.Dir(dataDir) == owning {
		t.Fatalf("filepath.Dir of the nested data dir (%q) is the owning root", dataDir)
	}
	if config.RepoKey(wt) != config.RepoKey(main) {
		t.Fatalf("RepoKey(worktree) %q != RepoKey(main) %q", config.RepoKey(wt), config.RepoKey(main))
	}

	t.Chdir(main)
	b, err := Open()
	if err != nil {
		t.Fatalf("open main: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if b.RuntimeDir != runtime {
		t.Fatalf("runtime dir from main %q, from worktree %q", b.RuntimeDir, runtime)
	}
	if config.ReadRepoPathMarker(b.RuntimeDir) != marker {
		t.Fatalf("marker moved after opening the main tree: %q", config.ReadRepoPathMarker(b.RuntimeDir))
	}
}

// AC4: a worktree that also has its own data dir still loads the main tree's
// workflows, agents, and constitution. The copy is not the process of record.
func TestWorktreeOwnDataDirIsNotTheProcess(t *testing.T) {
	isolateProcessEnv(t)
	main, wt := linkedPair(t)
	writeFile(t, filepath.Join(main, ".satelle", "satelle.toml"), "[review]\ngate_create = false\n")
	writeFile(t, filepath.Join(main, ".satelle", "workflows", "done.toml"),
		"[meta]\nname = \"done\"\ntype = \"workflow\"\n\n# MAIN-PROCESS\n")
	writeFile(t, filepath.Join(main, ".satelle", "workflows", "agents.toml"),
		"[reviewer]\nrole = \"reviewer\"\nmodel = \"main-agent-model\"\n")
	writeFile(t, filepath.Join(main, ".satelle", "constitution.md"), "MAIN-PROCESS-CONSTITUTION\n")

	writeFile(t, filepath.Join(wt, ".satelle", "satelle.toml"), "data_dir = \"decoy/process\"\n\n[review]\ngate_create = false\n")
	writeFile(t, filepath.Join(wt, ".satelle", "workflows", "done.toml"),
		"[meta]\nname = \"done\"\ntype = \"workflow\"\n\n# WORKTREE-COPY\n")
	writeFile(t, filepath.Join(wt, ".satelle", "workflows", "agents.toml"),
		"[reviewer]\nrole = \"reviewer\"\nmodel = \"worktree-agent-model\"\n")
	writeFile(t, filepath.Join(wt, ".satelle", "constitution.md"), "WORKTREE-COPY-CONSTITUTION\n")

	t.Chdir(wt)
	a, err := Open()
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if resolved(t, a.ProcessDataDir) != filepath.Join(resolved(t, main), config.DefaultDataDir) {
		t.Fatalf("ProcessDataDir = %q, want the main tree's %s", a.ProcessDataDir, config.DefaultDataDir)
	}
	if resolved(t, a.DataDir) != filepath.Join(resolved(t, wt), "decoy", "process") {
		t.Fatalf("DataDir = %q, want the decoy data_dir under the worktree", a.DataDir)
	}
	if a.Config.DataDir != "decoy/process" {
		t.Fatalf("location Config.DataDir = %q, want the decoy", a.Config.DataDir)
	}
	decoyWorkflows := a.Config.ResolveAuthoredDirs(a.RepoRoot)["workflows"]
	if resolved(t, decoyWorkflows) != filepath.Join(resolved(t, wt), "decoy", "process", "workflows") {
		t.Fatalf("location authored workflows = %q, want the decoy directory", decoyWorkflows)
	}
	if got, ok := config.FindDataDir(wt); !ok || resolved(t, got) != filepath.Join(resolved(t, wt), config.DefaultDataDir) {
		t.Fatalf("FindDataDir(worktree) = %q, %v; want the worktree's own %s", got, ok, config.DefaultDataDir)
	}
	if _, err := a.Store.DocIndex.SyncRoots(context.Background(), a.AuthoredDirs().Index(), time.Now()); err != nil {
		t.Fatal(err)
	}
	doc, err := a.Store.DocIndex.Get(context.Background(), "workflows", "done")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Body, "MAIN-PROCESS") || strings.Contains(doc.Body, "WORKTREE-COPY") {
		t.Fatalf("workflow body is not the main tree's:\n%s", doc.Body)
	}
	eff, err := config.LoadEffectiveAgents(a.ProcessDataDir, a.ProcessConfig.Vars)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Agents.Reviewer.Model != "main-agent-model" {
		t.Fatalf("reviewer model = %q, want main-agent-model", eff.Agents.Reviewer.Model)
	}
	constBody, err := os.ReadFile(config.ResolveProcessConstitution(a.ProcessConfig, a.ProcessRoot))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(constBody), "MAIN-PROCESS-CONSTITUTION") {
		t.Fatalf("constitution = %q", constBody)
	}
	copyBody, err := os.ReadFile(filepath.Join(wt, ".satelle", "constitution.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(copyBody), "WORKTREE-COPY-CONSTITUTION") {
		t.Fatalf("worktree constitution was rewritten: %q", copyBody)
	}
	wtDone, err := os.ReadFile(filepath.Join(wt, ".satelle", "workflows", "done.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wtDone), "WORKTREE-COPY") {
		t.Fatalf("worktree workflow was rewritten: %q", wtDone)
	}
}

// AC5: when the main tree has no data dir, the process read-plane does not
// invent one. Opening the worktree fails as not initialised, the same way
// opening the main tree does.
func TestWorktreeWithUngovernedMainStaysUninitialised(t *testing.T) {
	home := isolateProcessEnv(t)
	main, wt := linkedPair(t)
	// A directory that is not named .satelle must not count as a data dir.
	if err := os.MkdirAll(filepath.Join(main, "process"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := homeEntries(t, home)

	if config.DefaultDataDir != ".satelle" {
		t.Fatalf("DefaultDataDir = %q", config.DefaultDataDir)
	}
	if got := config.CanonicalRepoRoot(wt); got != wt {
		t.Fatalf("CanonicalRepoRoot(worktree of an ungoverned main) = %q, want %q", got, wt)
	}
	if _, ok := config.FindDataDir(main); ok {
		t.Fatal("FindDataDir(main) found a data dir")
	}
	if _, ok := config.FindDataDir(wt); ok {
		t.Fatal("FindDataDir(worktree) found a data dir")
	}
	t.Chdir(wt)
	if path, err := config.ResolvePath(""); err != nil || path != "" {
		t.Fatalf("ResolvePath from the worktree = %q, %v; want empty", path, err)
	}
	if got := (config.Config{}).ResolveDataDir(wt); got != filepath.Join(wt, config.DefaultDataDir) {
		t.Fatalf("empty ResolveDataDir = %q, want %q", got, filepath.Join(wt, config.DefaultDataDir))
	}
	if _, err := os.Stat(filepath.Join(wt, config.DefaultDataDir)); !os.IsNotExist(err) {
		t.Fatalf("ResolveDataDir created %s: %v", config.DefaultDataDir, err)
	}
	if _, err := os.Stat(filepath.Join(main, config.DefaultDataDir)); !os.IsNotExist(err) {
		t.Fatalf("main gained %s: %v", config.DefaultDataDir, err)
	}

	a, err := Open()
	if err == nil {
		_ = a.Close()
		t.Fatal("opening the worktree must fail when the main tree has no data dir")
	}
	if !errors.Is(err, ErrNotInitialised) {
		t.Fatalf("want ErrNotInitialised, got %T: %v", err, err)
	}
	for _, want := range []string{"not a satelle repo", "satelle init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must contain %q, got: %v", want, err)
		}
	}

	t.Chdir(main)
	b, err := Open()
	if err == nil {
		_ = b.Close()
		t.Fatal("opening the main tree must fail the same way")
	}
	if !errors.Is(err, ErrNotInitialised) {
		t.Fatalf("main: want ErrNotInitialised, got %T: %v", err, err)
	}
	after := homeEntries(t, home)
	if len(after) != len(before) {
		t.Fatalf("Open wrote the home plane: before=%v after=%v", before, after)
	}
}

// AC1 field shape: the main tree has a data dir and no satelle.toml. The
// process config is zero, the process data dir is the main tree's .satelle,
// and the location data dir is the worktree join, which is not created.
func TestWorktreeZeroConfigProcessIsTheMainDirectory(t *testing.T) {
	isolateProcessEnv(t)
	main, wt := linkedPair(t)
	writeFile(t, filepath.Join(main, ".satelle", "constitution.md"), "ZERO-CONFIG-CONSTITUTION\n")
	writeFile(t, filepath.Join(main, ".satelle", "workflows", "agents.toml"), "[reviewer]\nrole = \"reviewer\"\nmodel = \"zero-main\"\n")
	t.Chdir(wt)

	a, err := Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if !reflect.DeepEqual(a.ProcessConfig, config.Config{}) {
		t.Fatalf("ProcessConfig = %+v, want zero", a.ProcessConfig)
	}
	if resolved(t, a.ProcessRoot) != resolved(t, main) {
		t.Fatalf("ProcessRoot = %q, want %q", a.ProcessRoot, main)
	}
	if resolved(t, a.ProcessDataDir) != filepath.Join(resolved(t, main), config.DefaultDataDir) {
		t.Fatalf("ProcessDataDir = %q", a.ProcessDataDir)
	}
	if resolved(t, a.DataDir) != filepath.Join(resolved(t, wt), config.DefaultDataDir) {
		t.Fatalf("DataDir = %q, want the location join", a.DataDir)
	}
	if _, err := os.Stat(a.DataDir); !os.IsNotExist(err) {
		t.Fatalf("location DataDir was created: %v", err)
	}
	if resolved(t, a.RepoRoot) == resolved(t, main) {
		t.Fatalf("RepoRoot moved to the main tree: %q", a.RepoRoot)
	}
	body, err := os.ReadFile(config.ResolveProcessConstitution(a.ProcessConfig, a.ProcessRoot))
	if err != nil || !strings.Contains(string(body), "ZERO-CONFIG-CONSTITUTION") {
		t.Fatalf("constitution = %q, %v", body, err)
	}
}

// Heading B: opening a governed main tree from a subdirectory still resolves
// that tree's data dir. Covers a present satelle.toml and an absent one.
func TestOpenFromSubdirectoryResolvesMainDataDir(t *testing.T) {
	isolateProcessEnv(t)
	for _, withToml := range []bool{false, true} {
		name := "no-toml"
		if withToml {
			name = "with-toml"
		}
		t.Run(name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(repo, "README.md"), "x\n")
			gitAt(t, repo, "init", "-q")
			gitAt(t, repo, "config", "user.email", "t@example.com")
			gitAt(t, repo, "config", "user.name", "t")
			gitAt(t, repo, "add", "-A")
			gitAt(t, repo, "commit", "-q", "-m", "init")
			data := filepath.Join(repo, config.DefaultDataDir)
			if withToml {
				writeFile(t, filepath.Join(data, config.ConfigName), "[review]\ngate_create = false\n")
			} else if err := os.MkdirAll(data, 0o755); err != nil {
				t.Fatal(err)
			}
			sub := filepath.Join(repo, "sub", "deep")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(sub)
			a, err := Open()
			if err != nil {
				t.Fatalf("open from subdirectory: %v", err)
			}
			t.Cleanup(func() { _ = a.Close() })
			if resolved(t, a.ProcessRoot) != resolved(t, repo) {
				t.Fatalf("ProcessRoot = %q, want %q", a.ProcessRoot, repo)
			}
			if resolved(t, a.ProcessDataDir) != resolved(t, data) {
				t.Fatalf("ProcessDataDir = %q, want %q", a.ProcessDataDir, data)
			}
			if resolved(t, a.ProcessDataDir) == filepath.Join(resolved(t, sub), config.DefaultDataDir) {
				t.Fatal("ProcessDataDir was joined onto the subdirectory")
			}
		})
	}
}
