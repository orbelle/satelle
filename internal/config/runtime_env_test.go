package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func TestGitLookupEnvKeepsOnlyTheLookupsVariables(t *testing.T) {
	environ := []string{
		"PATH=/bin", "HOME=/home/x", "XDG_CONFIG_HOME=/home/x/.config", "GIT_DIR=/repo/.git",
		"GIT_TOKEN=t1", "GH_HOST=github.com", "GITHUB_X=x", "MY_SECRET=s", "db_password=p", "API_TOKEN=t2",
		"PLAIN=plain", "PLAIN2=plain2", "GIT_PLAIN2=plain2",
	}
	got := GitLookupEnv(environ, []string{"PLAIN2", "GIT_PLAIN2"})
	want := []string{"PATH=/bin", "HOME=/home/x", "XDG_CONFIG_HOME=/home/x/.config", "GIT_DIR=/repo/.git"}
	if !slices.Equal(got, want) {
		t.Fatalf("GitLookupEnv = %q, want %q", got, want)
	}
	if got := GitLookupEnv(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("GitLookupEnv of nothing = %#v, want an empty, non-nil environment", got)
	}
}

func TestRuntimeDirEnvMatchesRuntimeDir(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	repo := t.TempDir()
	gittest.Git(t, repo, "init", "-q")
	gittest.Git(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	linked := filepath.Join(t.TempDir(), "linked")
	gittest.Git(t, repo, "worktree", "add", "-q", linked)
	outside := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(outside))
	for name, root := range map[string]string{"repo": repo, "linked worktree": linked, "no git": outside} {
		env := GitLookupEnv(os.Environ(), nil)
		if got, want := RuntimeDirEnv(root, env), RuntimeDir(root); got != want {
			t.Errorf("%s: RuntimeDirEnv = %s, RuntimeDir = %s", name, got, want)
		}
	}
	if RuntimeDirEnv(linked, GitLookupEnv(os.Environ(), nil)) != RuntimeDirEnv(repo, GitLookupEnv(os.Environ(), nil)) {
		t.Error("a linked worktree is keyed apart from its repo")
	}
}
