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

func TestWithheldNames(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{NodeWithholdEnv + "= A , ,B,"}, []string{"A", "B"}},
		{[]string{NodeWithholdEnv + "=A", NodeWithholdEnv + "=C"}, []string{"C"}},
		{[]string{NodeWithholdEnv + "=  "}, nil},
	}
	for _, c := range cases {
		if got := WithheldNames(c.in); !slices.Equal(got, c.want) {
			t.Errorf("WithheldNames(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTargetGitEnv(t *testing.T) {
	in := []string{
		"PATH=/bin", "API_TOKEN=t", "MY_SECRET=s", "DB_PASSWORD=p", "NODE_KEY=k", NodeWithholdEnv + "=NODE_KEY",
		"HOME=/h", "GIT_DIR=/c/.git", "GIT_WORK_TREE=/c", "GIT_INDEX_FILE=/i", "GIT_OBJECT_DIRECTORY=/o", "GIT_COMMON_DIR=/c",
		"GIT_CEILING_DIRECTORIES=/t", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=remote.origin.url", "GIT_CONFIG_VALUE_0=x",
		"GIT_CONFIG_PARAMETERS='core.worktree'='/c'", "GIT_CONFIG_GLOBAL=/g", "GIT_ATTR_SOURCE=HEAD", "GIT_NO_LAZY_FETCH=0",
		"GIT_TERMINAL_PROMPT=1", "XDG_CONFIG_HOME=/x", "OTHER=1",
	}
	want := []string{"PATH=/bin", "HOME=/h", "XDG_CONFIG_HOME=/x", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0"}
	if got := TargetGitEnv(in); !slices.Equal(got, want) {
		t.Errorf("TargetGitEnv = %q, want %q", got, want)
	}
	if got := TargetGitEnv(nil); got == nil || !slices.Equal(got, []string{"GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0"}) {
		t.Errorf("TargetGitEnv(nil) = %#v", got)
	}
}
