package cli

// sty_ce858cb3 — bytes `satelle init` writes into the substrate while a story is
// engaged are not that story's change. These tests run the real runInit against a
// repo whose story is engaged over the repo's own runtime database, with the verb
// ledger unwired during the run, so init can only reach its ledger by itself.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
)

const seedTomlRel = ".satelle/satelle.toml"

// mine is a valid skill the story itself writes during its engagement.
const (
	mine      = ".satelle/skills/mine.md"
	mineSkill = "---\nname: mine\ntype: skill\ndescription: the story's own skill\n---\n\nbody\n"
)

// initRuntimeDB is the database runInit creates for repo.
func initRuntimeDB(t *testing.T, repo string) string {
	t.Helper()
	cfg, _, _ := config.Load(filepath.Join(repo, config.DefaultDataDir, config.ConfigName))
	return filepath.Join(cfg.ResolveRuntimeDir(repo).Dir, config.DefaultDBName)
}

// seedRepo is a git repo with .satelle/ ignored, as a real checkout keeps it,
// scaffolded by the real runInit, with SATELLE_CONFIG naming it.
func seedRepo(t *testing.T) string {
	t.Helper()
	isolateUserHome(t)
	t.Setenv("SATELLE_SERVER_ENDPOINT", "none")
	repo := t.TempDir()
	gitTree(t, repo, ".satelle/")
	if err := runInit(io.Discard, repo, true, nil); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	t.Setenv("SATELLE_CONFIG", filepath.Join(repo, seedTomlRel))
	return repo
}

// healTrigger gives satelle.toml a retired [sync] key, which runInit removes.
func healTrigger(t *testing.T, repo string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repo, seedTomlRel))
	if err != nil {
		t.Fatal(err)
	}
	return config.UpsertKey(string(raw), "sync", retiredHoldStaleAfterKey, `"30m"`)
}

// seededStory is seedRepo with satelle.toml carrying a heal trigger written before
// a story engaged over the repo's own database.
func seededStory(t *testing.T) (string, *storyEnv) {
	t.Helper()
	repo := seedRepo(t)
	editBeforeEngage(t, repo, seedTomlRel, healTrigger(t, repo))
	e := engageSyncStoryIn(t, repo, initRuntimeDB(t, repo))
	e.engage()
	return repo, e
}

// initUnwired runs the real runInit with the verb ledger unwired, then puts it
// back and pushes the healed file's mtime past any coarse clock, so the walk is
// certain to see the rewrite and only the exclusion can leave it out.
func initUnwired(t *testing.T, repo string, e *storyEnv) {
	t.Helper()
	withVerbWiring(t)
	verb.SetLedgerStore(nil)
	err := runInit(io.Discard, repo, true, nil)
	verb.SetLedgerStore(e.db.Ledger)
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}
	later := time.Now().Add(3 * time.Second) // time-subject: the rewrite's mtime must clear the engagement baseline's timestamp
	if err := os.Chtimes(filepath.Join(repo, seedTomlRel), later, later); err != nil {
		t.Fatal(err)
	}
}

// seedRows is every substrate_seeded file entry for rel in db, oldest first.
func seedRows(t *testing.T, db *store.DB, rel string) []verb.SeededFile {
	t.Helper()
	rows, err := db.Ledger.List(context.Background(), ledger.ListFilter{Kind: ledger.KindSubstrateSeeded, Limit: ledger.MaxListLimit})
	if err != nil {
		t.Fatal(err)
	}
	var out []verb.SeededFile
	for _, r := range rows {
		var p verb.SubstrateSeedPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			t.Fatal(err)
		}
		for _, f := range p.Files {
			if f.Path == rel {
				out = append(out, f)
			}
		}
	}
	return out
}

// rewriteAgain makes the next runInit rewrite satelle.toml from init's own bytes.
func rewriteAgain(t *testing.T) {
	t.Helper()
	initTomlHealHook = func(b []byte) []byte { return append(b, []byte("# rewritten by init\n")...) }
	t.Cleanup(func() { initTomlHealHook = nil })
}

// wantChained asserts the last two seed rows for rel chain: the later one started
// from the bytes the earlier one wrote, and wrote different ones.
func wantChained(t *testing.T, db *store.DB, rel string) {
	t.Helper()
	rows := seedRows(t, db, rel)
	if len(rows) < 2 {
		t.Fatalf("want a second seed row for %s, got %+v", rel, rows)
	}
	prev, last := rows[len(rows)-2], rows[len(rows)-1]
	if last.BeforeSHA256 != prev.SHA256 || last.SHA256 == prev.SHA256 {
		t.Fatalf("second row must start from the first row's bytes and change them: %+v then %+v", prev, last)
	}
}

func (e *storyEnv) substrateDiff() verb.StoryDiffResult {
	e.t.Helper()
	res, err := verb.StoryDiff(context.Background(), e.id, verb.StoryDiffOpts{Patch: true, IncludeSubstrate: true})
	if err != nil {
		e.t.Fatalf("StoryDiff: %v", err)
	}
	return res
}

// diffLists reports whether rel is in any of the diff's four outputs.
func diffLists(res verb.StoryDiffResult, rel string) bool {
	return slices.Contains(res.Files, rel) || slices.Contains(res.ProcessRootFiles, rel) ||
		strings.Contains(res.Stat, rel) || strings.Contains(res.Patch, rel)
}

// lastChangeRecord is the files of the story's newest change_record.
func (e *storyEnv) lastChangeRecord() []string {
	e.t.Helper()
	rows, err := e.db.Ledger.ListByStory(context.Background(), e.id, ledger.KindChangeRecord)
	if err != nil || len(rows) == 0 {
		e.t.Fatalf("no change_record: %v", err)
	}
	var p struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(rows[len(rows)-1].Payload, &p); err != nil {
		e.t.Fatal(err)
	}
	return p.Files
}

// AC2: once the story edits the file init wrote, it is the story's change again.
func TestInitRewriteThenStoryEditListed(t *testing.T) {
	repo, e := seededStory(t)
	initUnwired(t, repo, e)
	raw, err := os.ReadFile(filepath.Join(repo, seedTomlRel))
	if err != nil {
		t.Fatal(err)
	}
	editAfterEngage(t, repo, seedTomlRel, string(raw)+"# the story's edit\n")
	if res := e.substrateDiff(); !slices.Contains(res.Files, seedTomlRel) {
		t.Fatalf("the story's edit after init must be listed, got %v", res.Files)
	}
}

// AC4: one run records one row in the repo's own ledger naming what it healed,
// with the bytes before and after; a run that changes nothing records none.
func TestInitRecordsSeedRow(t *testing.T) {
	isolateUserHome(t)
	repo := t.TempDir()
	before := "[sync]\n" + retiredHoldStaleAfterKey + " = \"30m\"\n\n[review]\ngate_create = false\n"
	writeRepoFile(t, repo, seedTomlRel, before)
	for i := 0; i < 2; i++ {
		if err := runInit(io.Discard, repo, true, nil); err != nil {
			t.Fatalf("runInit %d: %v", i, err)
		}
	}
	db, err := store.Open(initRuntimeDB(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Ledger.List(context.Background(), ledger.ListFilter{Kind: ledger.KindSubstrateSeeded})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].StoryID != "" {
		t.Fatalf("want one repo-scoped substrate_seeded row across two runs, got %d: %+v", len(rows), rows)
	}
	var p verb.SubstrateSeedPayload
	if err := json.Unmarshal(rows[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	var toml *verb.SeededFile
	for i := range p.Files {
		if p.Files[i].Path == seedTomlRel {
			toml = &p.Files[i]
		}
	}
	if toml == nil {
		t.Fatalf("the row must name %s, got %+v", seedTomlRel, p.Files)
	}
	cur, err := os.ReadFile(filepath.Join(repo, seedTomlRel))
	if err != nil {
		t.Fatal(err)
	}
	if toml.BeforeSHA256 != sha256Hex([]byte(before)) || toml.SHA256 != sha256Hex(cur) || toml.BeforeMtime.IsZero() {
		t.Errorf("row must carry the bytes before and after and the pre-write mtime: %+v", *toml)
	}
}

// AC4: the --all heal records each healed repo's row in that repo's own ledger.
func TestInitAllRecordsPerRepo(t *testing.T) {
	isolateUserHome(t)
	repos := []string{t.TempDir(), t.TempDir()}
	for _, r := range repos {
		if err := runInit(io.Discard, r, false, []string{"claude"}); err != nil {
			t.Fatalf("runInit %s: %v", r, err)
		}
		writeRepoFile(t, r, satelleHookScriptRel, "# stale wrapper\n")
	}
	if err := runInitAll(io.Discard, true, nil); err != nil {
		t.Fatalf("init --all: %v", err)
	}
	for i, r := range repos {
		db, err := store.Open(initRuntimeDB(t, r))
		if err != nil {
			t.Fatal(err)
		}
		rows := seedRows(t, db, satelleHookScriptRel)
		all, _ := db.Ledger.List(context.Background(), ledger.ListFilter{Kind: ledger.KindSubstrateSeeded})
		_ = db.Close()
		if len(rows) == 0 || rows[len(rows)-1].BeforeSHA256 != sha256Hex([]byte("# stale wrapper\n")) {
			t.Errorf("repo %d: its own ledger must record the --all heal of %s, got %+v", i, satelleHookScriptRel, rows)
		}
		for _, e := range all {
			var p verb.SubstrateSeedPayload
			_ = json.Unmarshal(e.Payload, &p)
			if p.Root != r {
				t.Errorf("repo %d: ledger holds a row for another repo %s", i, p.Root)
			}
		}
	}
}
