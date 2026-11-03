package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/testutil"
	"github.com/bobmcallan/satelle/internal/verb"
)

func TestInitChangedAreas(t *testing.T) {
	home := testutil.IsolateHome(t)
	repo := t.TempDir()
	cfg := config.Config{Sync: map[string]string{"project": "p"}}
	// The last sync resolved personal for the binding: every substrate area syncs.
	if err := syncstate.RecordResolvedScope(home, repo, config.PersonalScope, "p", time.Now()); err != nil {
		t.Fatal(err)
	}
	files := func(paths ...string) []verb.SeededFile {
		var out []verb.SeededFile
		for _, p := range paths {
			out = append(out, verb.SeededFile{Path: p})
		}
		return out
	}
	for _, c := range []struct {
		name    string
		changed []verb.SeededFile
		want    []string
	}{
		{"nothing changed", nil, nil},
		{"satelle.toml is settings", files(".satelle/satelle.toml"), []string{"settings"}},
		{"a skill file is skills", files(".satelle/skills/mine.md"), []string{"skills"}},
		{"areas are de-duplicated and sorted", files(".satelle/skills/a.md", ".satelle/satelle.toml", ".satelle/skills/b.md"), []string{"settings", "skills"}},
		{"documents and the constitution sync under a personal scope", files(".satelle/documents/doc.md", ".satelle/constitution.md"), []string{"constitution", "documents"}},
		{"files no area owns", files(".satelle/deployed.version", ".satelle/hooks/satelle-hook.sh"), nil},
	} {
		if got := initChangedAreas(cfg, repo, c.changed); !slices.Equal(got, c.want) {
			t.Errorf("%s: areas = %v, want %v", c.name, got, c.want)
		}
	}
	// A recorded local scope, or a record for another binding, syncs nothing.
	if err := syncstate.RecordResolvedScope(home, repo, config.LocalScope, "p", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := initChangedAreas(cfg, repo, files(".satelle/skills/mine.md")); got != nil {
		t.Errorf("recorded local: areas = %v, want none", got)
	}
	if err := syncstate.RecordResolvedScope(home, repo, config.PersonalScope, "other", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := initChangedAreas(cfg, repo, files(".satelle/skills/mine.md")); got != nil {
		t.Errorf("recorded for another binding: areas = %v, want none", got)
	}
}

// TestInitPushCmdBindsRoot: the push init runs is this binary's own `sync push`,
// bound to the healed root by its working directory and SATELLE_CONFIG, so the
// child's pre-run opens that root's store and no other.
func TestInitPushCmdBindsRoot(t *testing.T) {
	root := t.TempDir()
	c, err := initPushCmd(root)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	if c.Path != exe || !slices.Equal(c.Args[1:], []string{"sync", "push", "--dry-run=false"}) {
		t.Errorf("push command = %s %v, want %s sync push --dry-run=false", c.Path, c.Args, exe)
	}
	if c.Dir != root {
		t.Errorf("push must run in the root: Dir = %q, want %q", c.Dir, root)
	}
	want := "SATELLE_CONFIG=" + filepath.Join(root, ".satelle", "satelle.toml")
	var bound string // the last assignment is the one the child sees
	for _, kv := range c.Env {
		if strings.HasPrefix(kv, "SATELLE_CONFIG=") {
			bound = kv
		}
	}
	if bound != want {
		t.Errorf("push must bind SATELLE_CONFIG to the root's satelle.toml (%s): %v", want, c.Env)
	}
}

const initPushFailedLine = "init changed synced substrate (settings) but the push failed — run `satelle sync push`"
