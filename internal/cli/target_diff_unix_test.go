//go:build !windows

package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

func targetHostGit(t *testing.T) {
	t.Helper()
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	v, err := gitfacts.ParseVersion(string(out))
	if err != nil {
		t.Fatal(err)
	}
	if !v.AtLeast(2, 44) {
		t.Skipf("git %s is older than 2.44, below which a target checkout is refused by design", v.Raw)
	}
}

func targetControl(t *testing.T) (ctl, tgt string) {
	t.Helper()
	ctl = isolate(t)
	must(t, "init")
	files := map[string]string{
		".satelle/workflows/tw.toml": "name = \"tw\"\n[states.a]\nstart = true\n[states.c]\nterminal = true\n[gates.chk]\nskill = \"chk\"\n[[transition]]\nfrom = [\"a\"]\nto = \"c\"\ngroups = [{ gates = [\"chk\"] }]\n",
		".satelle/skills/chk.md":     "---\ntype: check\n---\n```sh\nexit 0\n```\n",
	}
	for name, body := range files {
		writeFile(t, filepath.Join(ctl, name), body)
	}
	writeRepos(t, ctl, "web")
	tgt = checkoutWith(t, "https://github.com/o/web")
	writeFile(t, filepath.Join(tgt, "web", "a.go"), "package web\n")
	gittest.Git(t, tgt, "add", "-A")
	gittest.Git(t, tgt, "commit", "-q", "-m", "init")
	writeFile(t, filepath.Join(ctl, ".satelle", reporeg.LocalFile), "[repos.web]\npath = \""+tgt+"\"\n")
	return ctl, tgt
}

func TestStoryDiffReadsTargetRepository(t *testing.T) {
	for _, mode := range []string{"target", "self"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "target" {
				targetHostGit(t)
			}
			ctl, tgt := targetControl(t)
			g, o := tgt, ctl
			args := []string{"story", "create", "--title", "t", "--workflow", "tw"}
			if mode == "target" {
				args = append(args, "--repo", "web")
			} else {
				g, o = ctl, tgt
			}
			id := createID(t, args...)
			head := gittest.Git(t, g, "rev-parse", "HEAD")
			must(t, "story", "seat", id, "--session", "s-1")
			for dir, name := range map[string]string{g: "feat/x.go", o: "noise/y.go"} {
				writeFile(t, filepath.Join(dir, name), "package x\n")
				gittest.Git(t, dir, "add", "--", name)
				gittest.Git(t, dir, "commit", "-q", "-m", name)
			}
			var got struct {
				Base  string
				Paths []string
			}
			if err := json.Unmarshal([]byte(must(t, "story", "diff", id, "--json")), &got); err != nil {
				t.Fatal(err)
			}
			if got.Base != head || !slices.Equal(got.Paths, []string{"feat/x.go"}) {
				t.Fatalf("story diff %+v, want base %s and feat/x.go", got, head)
			}
		})
	}
}

func TestGateDryShowsWorktree(t *testing.T) {
	for _, mode := range []string{"target", "self"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "target" {
				targetHostGit(t)
			}
			_, tgt := targetControl(t)
			args := []string{"story", "create", "--title", "t", "--workflow", "tw"}
			if mode == "target" {
				args = append(args, "--repo", "web")
			}
			id := createID(t, args...)
			must(t, "story", "seat", id, "--session", "s-1")
			out := must(t, "gate", "run", "tw/chk", "--item", id, "--show-input")
			if has := strings.Contains(out, "worktree: "+tgt+"\n"); has != (mode == "target") || mode == "self" && strings.Contains(out, "worktree:") {
				t.Fatalf("%s gate input:\n%s", mode, out)
			}
		})
	}
}
