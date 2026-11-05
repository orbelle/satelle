package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

func TestRunInitPlantsLogsPointer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink pointer")
	}
	repo := t.TempDir()
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}
	link := filepath.Join(repo, ".satelle", "logs")
	st, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("want symlink")
	}
	cfg, _, err := config.Load(filepath.Join(repo, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	rt := cfg.ResolveLogsDir(repo)
	if err := os.MkdirAll(filepath.Join(rt, "dispatch"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("planted-through-pointer\n")
	if err := os.WriteFile(filepath.Join(rt, "dispatch", "x.log"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(link, "dispatch", "x.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("read-through = %q", got)
	}
}

func TestPointerIsSymlinkOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink pointer")
	}
	repo := t.TempDir()
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(filepath.Join(repo, ".satelle", "logs"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatal(".satelle/logs must be a symlink, not a directory")
	}
}

func TestInitPointerIsGitIgnored_TrackedSatelle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git absent")
	}
	repo := t.TempDir()
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatal(err)
	}
	// sty_dbc0be85: a fresh init ignores .satelle/ as a whole, which covers the
	// pointer; the check-ignore below is the proof.
	for _, args := range [][]string{{"init"}, {"check-ignore", ".satelle/logs"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil && args[0] == "check-ignore" {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		} else if err != nil && args[0] == "init" {
			t.Fatalf("git init: %v\n%s", err, out)
		}
	}
}

// A repo that tracks part of .satelle/ (a negation here) keeps its own setting;
// init adds only the pointer line, which is what keeps the symlink uncommitted.
func TestInitPointerListed_PartlyTrackedSatelle(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".satelle/*\n!.satelle/workflows/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatal(err)
	}
	gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if !strings.HasPrefix(string(gi), ".satelle/*\n!.satelle/workflows/\n") {
		t.Fatalf("the operator's lines must stay as written:\n%s", gi)
	}
	if !strings.Contains(string(gi), ".satelle/logs\n") {
		t.Fatalf("pointer line missing:\n%s", gi)
	}
}

// sty_45bf867b AC2: an operator's own line that ignores .satelle/ wholesale
// is outside satelle's block, so init leaves it byte-for-byte as written, adds
// no re-include over it, and names the exact line to remove.
func TestInitReportsOwnSatelleIgnore(t *testing.T) {
	for _, own := range []string{".satelle/", "/.satelle"} {
		t.Run(own, func(t *testing.T) {
			repo := t.TempDir()
			before := "node_modules/\n" + own + "\n"
			if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(before), 0o644); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			if err := runInitTest(t, &out, repo); err != nil {
				t.Fatal(err)
			}
			gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
			if !strings.HasPrefix(string(gi), before) {
				t.Fatalf("lines outside the managed block must stay as written:\n%s", gi)
			}
			if got := string(gi)[len(before):]; strings.Contains(got, own+"\n") || strings.Contains(got, "\n!.satelle/\n") {
				t.Errorf("init must neither repeat nor override the operator's own line:\n%s", gi)
			}
			want := fmt.Sprintf("! .gitignore line %q ignores .satelle/ wholesale — remove it so the substrate is tracked", own)
			if !strings.Contains(out.String(), want) {
				t.Errorf("init must name the line to remove (%s):\n%s", want, out.String())
			}
		})
	}
}

func TestScaffoldTomlNamesLogsLocationAndPointer(t *testing.T) {
	if !strings.Contains(scaffoldToml, "~/.satelle/<repo-key>/logs") {
		t.Error("seed must name runtime location")
	}
	if !strings.Contains(scaffoldToml, ".satelle/logs") {
		t.Error("seed must name the pointer")
	}
}

func TestLogsCommentGuard(t *testing.T) {
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			trim := strings.TrimSpace(line)
			if !strings.HasPrefix(trim, "//") && !strings.HasPrefix(trim, "*") {
				continue
			}
			if !strings.Contains(line, ".satelle/logs") {
				continue
			}
			if strings.Contains(line, "pointer") || strings.Contains(line, "symlink") {
				continue
			}
			t.Errorf("%s:%d storage claim without pointer: %s", path, i+1, trim)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReindexDoesNotIndexPointerLogs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink pointer")
	}
	repo := t.TempDir()
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(repo, ".satelle", "logs", "dispatch", "leak.md")
	if err := os.MkdirAll(filepath.Dir(md), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(md, []byte("---\nname: leak\ntype: skill\n---\n# leak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(filepath.Join(repo, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	rout, err := runRootIn(t, "", "reindex")
	if err != nil {
		t.Fatalf("reindex: %v\n%s", err, rout)
	}
	if strings.Contains(rout, "leak") || strings.Contains(strings.ToLower(rout), ".satelle/logs") {
		t.Fatalf("reindex mentioned pointer content:\n%s", rout)
	}
	for kind, dir := range cfg.ResolveAuthoredDirs(repo) {
		_ = kind
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repo, path)
			if strings.Contains(filepath.ToSlash(rel), ".satelle/logs") {
				t.Errorf("authored walk saw pointer path %s", rel)
			}
			return nil
		})
	}
}
