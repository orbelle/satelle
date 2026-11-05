package cli

// A git worktree is the repository, not a project of its own (sty_cd219594).
//
// These tests drive the real verbs (init, workspace add/list/partitions, story
// create/route) against a real `git worktree add`, a real mirror store and the
// real web landing handler, so the registry, the partition and the view are the
// ones an operator sees.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

// wtFixture is a satelle-governed main tree, its linked worktree, and a live
// mirror behind an ingest endpoint on an isolated SATELLE_HOME.
type wtFixture struct {
	main, wt string
	canon    string // the main tree's canonical (symlink-resolved) root
	ms       *mirror.Store
	srv      *httptest.Server
	out      string // output of `satelle init` run in the worktree
}

// writeOpenGateConfig lets a fixture repo create stories without the create gate.
func writeOpenGateConfig(t *testing.T, root string) {
	t.Helper()
	p := filepath.Join(root, ".satelle", "satelle.toml")
	if err := os.WriteFile(p, []byte("[review]\ngate_create = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initWorktree runs `satelle init` in the worktree, exactly as a worktree
// dispatch does, and records its output.
func (f *wtFixture) initWorktree(t *testing.T) {
	t.Helper()
	var out strings.Builder
	if err := runInit(&out, f.wt, false, nil); err != nil {
		t.Fatalf("init worktree: %v\n%s", err, out.String())
	}
	f.out = out.String()
	writeOpenGateConfig(t, f.wt)
}

// useWorktree exercises the worktree the way a dispatched agent does: from
// inside it, re-seed the mirror and write a story (which drains a snapshot).
func (f *wtFixture) useWorktree(t *testing.T) {
	t.Helper()
	t.Chdir(f.wt)
	if out, err := runRoot(t, "workspace", "add"); err != nil {
		t.Fatalf("workspace add (worktree): %v\n%s", err, out)
	}
	if out, err := runRoot(t, "story", "create", "--title", "from the worktree",
		"--body", "b", "--acceptance", "1. a", "--category", "chore"); err != nil {
		t.Fatalf("story create (worktree): %v\n%s", err, out)
	}
}

func registry(t *testing.T) []string {
	t.Helper()
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	out := append([]string(nil), gc.Workspace.Repos...)
	sort.Strings(out)
	return out
}

func partitionDetails(t *testing.T, ms *mirror.Store) []mirror.PartitionDetail {
	t.Helper()
	d, err := ms.ListPartitionDetails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func partitionIdentity(t *testing.T, ms *mirror.Store, repoKey string) mirror.IdentityMeta {
	t.Helper()
	payload, err := ms.GetItem(context.Background(), repoKey, "identity", "meta")
	if err != nil {
		t.Fatalf("read identity of %s: %v", repoKey, err)
	}
	var id mirror.IdentityMeta
	if err := json.Unmarshal([]byte(payload), &id); err != nil {
		t.Fatal(err)
	}
	return id
}

func landingBody(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: %s\n%s", resp.Status, b)
	}
	return string(b)
}

const sixStepDone = `[meta]
name = "done"
type = "workflow"
scope = "project"

["*"]
obligations = ["raised", "readied", "coded", "integrated", "released", "closed"]
cancel = { state = "cancelled" }
`

const sixStepCatalogue = `[meta]
name = "step"
type = "workflow"
scope = "project"

[raised]
status = "backlog"
start = true

[readied]
status = "plan"
agent = "planner"
skills = ["plan"]
requires = ["raised"]

[coded]
status = "in_progress"
agent = "coder"
skills = ["coder"]
requires = ["readied"]

[integrated]
status = "integration"
agent = "executor"
skills = ["integrate"]
requires = ["coded"]

[released]
status = "release"
agent = "executor"
skills = ["release"]
requires = ["integrated"]

[closed]
status = "done"
terminal = true
requires = ["released"]
`
