package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/hook"
)

func TestJudgeHookJoinsFileToolPathsLiterally(t *testing.T) {
	repo := func() string {
		d, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		gittest.Git(t, d, "init", "-q")
		return d
	}
	root, a := repo(), repo()
	temp, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(a, "sublink")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	t.Setenv("TMPDIR", temp)
	t.Setenv("HOME", t.TempDir())
	g := hook.Gate{
		Root:      root,
		TempRoots: []string{temp},
		Grant:     func(string) hook.Grant { return hook.Grant{} },
		Targets: func() []hook.Target {
			return []hook.Target{{Names: []string{"a"}, Item: "sto_1", Tree: a, Grant: hook.Grant{Tree: true}}}
		},
	}
	v := judgeHook(g, hook.Input{Tool: "Write", Cwd: a, Path: "sublink/../main.go"})
	if v.Allow || !strings.Contains(v.Reason, "resolves through a symlink to "+filepath.Join(root, "main.go")) {
		t.Errorf("sublink/../main.go: %+v", v)
	}
	if v := judgeHook(g, hook.Input{Tool: "Write", Cwd: a, Path: "x.go"}); !v.Allow {
		t.Errorf("x.go in the seated target refused: %s", v.Reason)
	}
	for _, p := range []string{"$TMPDIR/x", "~/x", "   "} {
		if v := judgeHook(g, hook.Input{Tool: "Write", Cwd: root, Path: p}); v.Allow {
			t.Errorf("%q under the unseated root allowed", p)
		}
	}
}
