package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/snapsync"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/syncscope"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/testutil"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Substrate continuity (sty_7562055c): `sync status`, and the state each sync
// operation records. The two-machine harness is sync_snapshot_test.go's.

func (m *snapMachine) status() (string, error) {
	m.use()
	cmd, buf := testCmd()
	err := runSyncStatus(cmd, m.url)
	return buf.String(), err
}

func (m *snapMachine) recorded() map[string]syncstate.SubstrateArea {
	m.t.Helper()
	st, _, err := syncstate.Load(config.GlobalDir(), m.repo)
	if err != nil {
		m.t.Fatal(err)
	}
	return st.SubstrateAreas
}

// published is a machine that has pushed one file in each of skills, workflows
// and documents, so all three are in step.
// publishedAreas are the areas publishedMachine leaves a hosted snapshot of: its
// files, plus the settings and agents layer every syncing scope sends.
var publishedAreas = []string{"skills", "workflows", "documents", "settings", "agents"}

func publishedMachine(t *testing.T, url string) *snapMachine {
	t.Helper()
	a := newSnapMachine(t, url)
	a.write("skills/x.md", "x\n")
	a.write("workflows/w.md", "w\n")
	a.write("documents/d.md", "d\n")
	a.pushConfig()
	a.pushDocs()
	return a
}

var colSplit = regexp.MustCompile(` {2,}`)

// row is the columns of an area's line in `sync status` output.
func row(t *testing.T, out, area string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, area+" ") {
			return colSplit.Split(strings.TrimSpace(line), -1)
		}
	}
	t.Fatalf("no %s row in:\n%s", area, out)
	return nil
}

func wantState(t *testing.T, out, area, state string) []string {
	t.Helper()
	cols := row(t, out, area)
	if len(cols) < 5 || cols[1] != state {
		t.Fatalf("%s = %v, want state %q\n%s", area, cols, state, out)
	}
	return cols
}

// forgeRecord appends a snapshot record the way another machine's push would,
// without any of its blobs.
func (f *snapFake) forgeRecord(t *testing.T, area string, parent int, files map[string]string) int {
	t.Helper()
	rec, err := snapsync.NewRecord(parent, "forged", files)
	if err != nil {
		t.Fatal(err)
	}
	_, v, _ := f.cfg.put("probe", snapsync.RecordPath(area), rec.Encode())
	return v
}

func TestSyncStatusNeverSyncedExitsNonZero(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a := newSnapMachine(t, ts.URL)
	a.write("skills/x.md", "x\n")
	out, err := a.status()
	if err == nil {
		t.Fatalf("a machine that never synced must exit non-zero:\n%s", out)
	}
	cols := wantState(t, out, "skills", "never synced")
	if cols[4] != "satelle sync push" {
		t.Errorf("skills next = %q", cols[4])
	}
	// Nothing local and nothing hosted: a push has nothing to send (sty_09b6b0e4).
	for _, area := range []string{"workflows", "documents"} {
		wantState(t, out, area, "in step")
	}
	if len(a.recorded()) != len(config.SubstrateAreas(config.PersonalScope)) {
		t.Errorf("status must record each verified area: %v", a.recorded())
	}
}

func TestSyncStatusInStepExitsZero(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	out, err := a.status()
	if err != nil {
		t.Fatalf("an in-step machine must exit zero: %v\n%s", err, out)
	}
	for _, area := range []string{"skills", "workflows", "documents"} {
		cols := wantState(t, out, area, "in step")
		if cols[2] != "snapshot 1" || cols[3] != "snapshot 1" {
			t.Errorf("%s versions = %v, want snapshot 1 / snapshot 1", area, cols)
		}
	}
	rec := a.recorded()
	for _, area := range []string{"skills", "workflows", "documents"} {
		if rec[area].State != snapsync.StateInStep || rec[area].CheckedAt.IsZero() {
			t.Errorf("recorded %s = %+v", area, rec[area])
		}
	}
}

func TestSyncStatusUnpushed(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	a.write("skills/x.md", "edited\n")
	out, err := a.status()
	if err == nil {
		t.Fatalf("unpushed work must exit non-zero:\n%s", out)
	}
	if cols := wantState(t, out, "skills", "unpushed"); cols[4] != "satelle sync push" {
		t.Errorf("next = %q", cols[4])
	}
	wantState(t, out, "workflows", "in step")
}

func TestSyncStatusBehindAndDiverged(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	b := newSnapMachine(t, ts.URL)
	b.deploy()
	b.pullDocs()
	a.write("skills/x.md", "from A\n")
	a.write("documents/d.md", "d from A\n")
	a.pushConfig()
	a.pushDocs()

	out, err := b.status()
	if err == nil {
		t.Fatalf("a machine behind must exit non-zero:\n%s", out)
	}
	// One pull verb for every area; the per-area deploy and documents pull are
	// no longer what status points at.
	if cols := wantState(t, out, "skills", "behind"); cols[4] != "satelle sync pull" {
		t.Errorf("skills next = %q", cols[4])
	}
	if cols := wantState(t, out, "documents", "behind"); cols[4] != "satelle sync pull" {
		t.Errorf("documents next = %q", cols[4])
	}
	wantState(t, out, "workflows", "in step")

	b.write("skills/x.md", "from B\n")
	out, _ = b.status()
	if cols := wantState(t, out, "skills", "diverged"); !strings.Contains(cols[4], "satelle sync reconcile") || !strings.HasSuffix(cols[4], "then satelle sync push") || strings.Contains(cols[4], "deploy") {
		t.Errorf("diverged next = %q, want reconcile, then satelle sync push", cols[4])
	}
	if rec := b.recorded(); rec["skills"].State != "diverged" || rec["workflows"].State != "in step" {
		t.Errorf("recorded = %+v", rec)
	}
}

func TestSyncStatusUnmerged(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	b := newSnapMachine(t, ts.URL)
	b.deploy()
	a.write("skills/x.md", "from A\n")
	a.pushConfig()
	b.write("skills/x.md", "from B\n")
	b.deploy() // conflict: both sides changed x.md

	out, err := b.status()
	if err == nil {
		t.Fatalf("an unmerged area must exit non-zero:\n%s", out)
	}
	cols := wantState(t, out, "skills", "unmerged")
	if !strings.Contains(cols[4], "skills/x.md") || !strings.Contains(cols[4], "satelle sync reconcile") {
		t.Errorf("next must name reconcile and the unmerged path: %q", cols[4])
	}
}

// An orphaned record — a loser of a push race stays on the hosted copy as a dead
// head — is not a newer snapshot, so it must not read as behind.
func TestSyncStatusOrphanedRecordIsNotBehind(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	if v := f.forgeRecord(t, "skills", 0, map[string]string{"skills/x.md": snapsync.SHA([]byte("lost\n"))}); v != 2 {
		t.Fatalf("forged record version = %d, want 2", v)
	}
	out, err := a.status()
	if err != nil {
		t.Fatalf("an orphaned record must not fail status: %v\n%s", err, out)
	}
	wantState(t, out, "skills", "in step")
}

// A hosted snapshot that names bytes the hosted copy does not hold yet leaves a
// pull incomplete: the versions agree afterwards, but a pull would still change
// something, so the area is behind.
func TestSyncStatusPendingPullIsBehind(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	b := newSnapMachine(t, ts.URL)
	b.deploy()
	_ = a
	f.forgeRecord(t, "skills", 1, map[string]string{"skills/x.md": snapsync.SHA([]byte("not uploaded\n"))})

	b.deploy()
	base, ok := b.base("skills")
	if !ok || base.Version != 2 {
		t.Fatalf("test premise: the incomplete pull must advance the base to 2: %+v", base)
	}
	statusOut, err := b.status()
	if err == nil {
		t.Fatalf("a pending pull must exit non-zero:\n%s", statusOut)
	}
	cols := wantState(t, statusOut, "skills", "behind")
	if cols[2] != "snapshot 2" || cols[3] != "snapshot 2" {
		t.Errorf("versions = %v, want the base to have caught up with the hosted snapshot", cols)
	}
	if got := b.recorded()["skills"].State; got != "behind" {
		t.Errorf("recorded after the incomplete pull = %q, want behind", got)
	}
}

// A signed-out machine is local: status names that and the login, checks
// nothing and records nothing.
func TestSyncStatusSignedOutIsLocal(t *testing.T) {
	ts, _ := newSnapServer(t) // no credential seeded
	a := newSnapMachine(t, ts.URL)
	a.write("skills/x.md", "x\n")
	out, err := a.status()
	if err != nil || strings.TrimSpace(out) != "scope: local — "+syncscope.ReasonSignedOut+"\npush: enabled" {
		t.Fatalf("signed-out status = %v\n%s", err, out)
	}
	if rec := a.recorded(); len(rec) != 0 {
		t.Errorf("a local status verified nothing and must not record areas: %v", rec)
	}
}

func TestSyncStatusUnavailableServer(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	signInTo(t, gone.URL)
	a := newSnapMachine(t, gone.URL)
	a.write("skills/x.md", "x\n")
	out, err := a.status()
	if err == nil {
		t.Fatalf("an unreadable hosted copy must exit non-zero:\n%s", out)
	}
	wantState(t, out, "skills", "unavailable")
	if rec := a.recorded(); len(rec) != 0 {
		t.Errorf("an unavailable answer verified nothing and must not be recorded: %v", rec)
	}
}

func TestSyncStatusAllLocalChecksNothing(t *testing.T) {
	tempRepo(t)
	out, err := runRoot(t, "sync", "status")
	if err != nil || !strings.Contains(out, "scope: local — "+syncscope.ReasonUnbound) {
		t.Fatalf("unbound status = %v\n%s", err, out)
	}
}

// The --story form opens the store itself (a bare "sync status" never does) and,
// in a repo that syncs nothing, has nothing to ask the story about.
func TestSyncStatusStoryFlagThroughRoot(t *testing.T) {
	tempRepo(t)
	out, err := runRoot(t, "sync", "status", "--story", "sty_00000000")
	if err != nil || !strings.Contains(out, "touched no synced substrate") {
		t.Fatalf("all-local --story status = %v\n%s", err, out)
	}
}

// AC2: every operation leaves the state the driver reports afterwards.
func TestSyncOperationsRecordDriverDerivedState(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)

	a := newSnapMachine(t, ts.URL)
	a.write("skills/x.md", "x\n")
	a.write("workflows/w.md", "w\n")
	a.write("documents/d.md", "d\n")
	a.pushConfig()
	if rec := a.recorded(); rec["skills"].State != "in step" || rec["workflows"].State != "in step" {
		t.Errorf("after a config push: %+v", rec)
	} else if _, ok := rec["documents"]; ok {
		t.Errorf("a config push must not record documents: %+v", rec)
	}
	a.pushDocs()
	if rec := a.recorded(); rec["documents"].State != "in step" {
		t.Errorf("after a documents push: %+v", rec)
	}

	// A push refused for being behind records behind, not in step.
	b := newSnapMachine(t, ts.URL)
	b.deploy()
	if rec := b.recorded(); rec["skills"].State != "in step" {
		t.Errorf("after a deploy: %+v", rec)
	}
	b.pullDocs()
	if rec := b.recorded(); rec["documents"].State != "in step" {
		t.Errorf("after a documents pull: %+v", rec)
	}
	a.write("skills/x.md", "from A\n")
	a.pushConfig()
	b.write("skills/other.md", "o\n")
	if _, err := b.tryPushConfig(); err == nil {
		t.Fatal("test premise: B's push must be refused as behind")
	}
	if got := b.recorded()["skills"].State; got != "diverged" {
		t.Errorf("after a refused push: %q, want diverged", got)
	}

	// A pull that leaves a conflict records unmerged.
	b.write("skills/x.md", "from B\n")
	b.deploy()
	if got := b.recorded()["skills"].State; got != "unmerged" {
		t.Errorf("after a pull that left a conflict: %q, want unmerged", got)
	}
	// ... and the unmerged paths are read from the area's synced base.
	if base, _ := b.base("skills"); len(base.Unmerged) != 1 {
		t.Errorf("base.Unmerged = %v", base.Unmerged)
	}
	st, _, _ := syncstate.Load(config.GlobalDir(), b.repo)
	if raw, _ := json.Marshal(st); strings.Contains(string(raw), "skills/x.md") {
		t.Errorf("unmerged paths must not be copied into sync state: %s", raw)
	}

	// A pull that leaves an incomplete entry records behind.
	c := newSnapMachine(t, ts.URL)
	c.deploy()
	f.forgeRecord(t, "workflows", 1, map[string]string{"workflows/w.md": snapsync.SHA([]byte("never uploaded\n"))})
	c.deploy()
	if got := c.recorded()["workflows"].State; got != "behind" {
		t.Errorf("after a pull that left an incomplete entry: %q, want behind", got)
	}
}

// AC6: on a fresh clone, rehydrate restores satelle.toml from the settings area.
func TestSyncRehydrateRestoresSettingsFromSnapshot(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	const withStale = "[sync]\nstale_after = \"6h\"\n" + boundProjectToml
	a := &snapMachine{t: t, url: ts.URL, repo: syncConfigRepo(t, withStale), state: t.TempDir() + "/state-a.json"}
	a.write("skills/x.md", "x\n")
	a.pushConfig()

	// A fresh clone: `project bind` leaves only the binding in satelle.toml.
	b := &snapMachine{t: t, url: ts.URL, repo: syncConfigRepo(t, boundProjectToml), state: t.TempDir() + "/state-b.json"}
	b.use()
	cmd, buf := testCmd()
	outcome, err := runSyncConfigDeployOutcome(cmd, ts.URL, "", 0)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, buf.String())
	}
	if !outcome.SettingsRestored {
		t.Fatalf("satelle.toml was not restored from the settings snapshot:\n%s", buf.String())
	}
	if got := b.read("satelle.toml"); !strings.Contains(got, `stale_after = "6h"`) || !strings.Contains(got, `project = "probe"`) {
		t.Errorf("restored satelle.toml = %q", got)
	}
	if b.has("backups/sync-conflicts/settings/satelle.toml") {
		t.Error("the hosted settings were parked as a conflict instead of restored")
	}
	if got := b.recorded()["settings"].State; got != "in step" {
		t.Errorf("recorded settings = %q", got)
	}
	if summary := rehydrateDoneSummary(outcome, syncscope.Resolution{Scope: config.PersonalScope, Account: testAccountEmail, Team: syncscope.Team{Name: "Ada"}, Project: "probe"}); !strings.Contains(summary, "settings: restored") || !strings.Contains(summary, "scope: personal") {
		t.Errorf("rehydrate's closing line = %q", summary)
	}
}

// Rehydrate's closing line names the resolved scope and, for a local one, the
// resolver's reason — not a settings key to restore.
func TestRehydrateSummaryNamesResolvedScope(t *testing.T) {
	for _, reason := range []string{syncscope.ReasonUnbound, syncscope.ReasonSignedOut} {
		summary := rehydrateDoneSummary(deployOutcome{}, syncscope.Resolution{Scope: config.LocalScope, Reason: reason})
		if !strings.Contains(summary, "scope: local — "+reason) || !strings.Contains(summary, "settings: not restored") {
			t.Errorf("summary = %q", summary)
		}
		if strings.Contains(summary, "[sync] all") || strings.Contains(summary, "settings area") {
			t.Errorf("summary names a removed scope key: %q", summary)
		}
	}
}

// AC5: the areas `sync status` judges come from config.ConfigAreas, documents
// and the repo's [sync] scopes — not from where this repo keeps its files. Here
// the data dir (and so the constitution path) is relocated and [sync] all opts
// everything in.
func TestSyncStatusTakesAreasFromConfigNotLayout(t *testing.T) {
	testutil.IsolateHome(t)
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	repo := t.TempDir()
	meta := filepath.Join(repo, "meta")
	writeRepoFile(t, repo, "meta/satelle.toml", "data_dir = \"meta\"\n[sync]\n"+boundProjectToml)
	writeRepoFile(t, repo, "meta/constitution.md", "# c\n")
	writeRepoFile(t, repo, "meta/skills/x.md", "x\n")
	t.Setenv("SATELLE_CONFIG", filepath.Join(meta, "satelle.toml"))
	hosted.DocumentSyncStatePathOverride = filepath.Join(t.TempDir(), "doc-state.json")
	t.Cleanup(func() { hosted.DocumentSyncStatePathOverride = "" })

	cmd, buf := testCmd()
	if err := runSyncStatus(cmd, ts.URL); err == nil {
		t.Fatalf("nothing is synced yet:\n%s", buf.String())
	}
	areas := append(append([]string{}, config.ConfigAreas...), "documents")
	for _, area := range areas {
		want := "in step" // nothing local to push (sty_09b6b0e4)
		if area == "constitution" || area == "skills" || area == "settings" {
			want = "never synced"
		}
		wantState(t, buf.String(), area, want)
	}

	cmd, buf = testCmd()
	if err := runSyncConfigPush(cmd, ts.URL, "", false); err != nil {
		t.Fatalf("push: %v\n%s", err, buf.String())
	}
	cmd, buf = testCmd()
	_ = runSyncStatus(cmd, ts.URL)
	for _, area := range areas {
		wantState(t, buf.String(), area, "in step")
	}
}

// --- `sync status --story` (sty_fcfe32e5) -----------------------------------
//
// A story that changed synced substrate cannot be done while that change exists
// only on this machine. The tests drive the real story-diff verb over a real
// store and git tree, and the real probe against the fake hosted server.

// openGater lets every edge through: these tests read a story's change sets, not
// its gates.
type openGater struct{}

func (openGater) Gate(context.Context, workitem.Item, string) (verb.GateDecision, error) {
	return verb.GateDecision{Gated: false}, nil
}

// gitTree makes dir a git tree with one commit, its substrate dirs ignored the
// way a real checkout's are: the authored process is never in git.
func gitTree(t *testing.T, dir string, ignore ...string) {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	writeRepoFile(t, dir, "a.go", "package a\n")
	writeRepoFile(t, dir, ".gitignore", strings.Join(ignore, "\n")+"\n")
	git("add", ".")
	git("commit", "-m", "init")
}

// storyEnv is a story engaged from a tree, with the verb layer wired to its own
// store. repo is the process-of-record root; tree is where the story was engaged
// (repo itself, or a linked worktree of it).
type storyEnv struct {
	t  *testing.T
	id string
	db *store.DB
}

// engageSyncStory wires the verb layer, makes tree the working directory and engages
// a story from it. SATELLE_CONFIG must already name the repo's config.
func engageSyncStory(t *testing.T, tree string) *storyEnv {
	t.Helper()
	return engageSyncStoryIn(t, tree, filepath.Join(t.TempDir(), "satelle.db"))
}

// engageSyncStoryIn is engageSyncStory over the database at dbPath.
func engageSyncStoryIn(t *testing.T, tree, dbPath string) *storyEnv {
	t.Helper()
	withVerbWiring(t)
	cfg, cfgPath, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	repo := config.RepoRootFromConfigPath(cfgPath)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	wfDir := filepath.Join(t.TempDir(), "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range twoMachineRoute {
		if err := os.WriteFile(filepath.Join(wfDir, name+".toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.DocIndex.Sync(context.Background(), map[string]string{"workflows": wfDir}, time.Now()); err != nil {
		t.Fatalf("sync workflows: %v", err)
	}
	verb.SetWorkItemStore(db.Stories)
	verb.SetLedgerStore(db.Ledger)
	verb.SetTxRunner(db.InTx)
	verb.SetDocIndexStore(db.DocIndex)
	verb.SetLeaseStore(db.Leases)
	verb.SetStoryDir(filepath.Join(t.TempDir(), "stories"))
	verb.SetAuthoredDirs(cfg.ResolveAuthoredDirs(repo))
	verb.SetSubstrateConfigDir(cfg.ResolveDataDir(repo))
	verb.SetTransitionGater(openGater{})
	if tree != repo {
		verb.SetWorktreeConfig(cfg, repo)
		verb.SetProcessProbe(&verb.ProcessProbe{Process: cfg, ProcessRoot: repo, InvokingRoot: tree})
	}
	t.Chdir(tree)

	e := &storyEnv{t: t, db: db}
	var it workitem.Item
	json.Unmarshal(e.dispatch("story-create", map[string]any{
		"title": "touches substrate", "body": "goal", "acceptance_criteria": "1. ok",
		"category": "feature", "tags": []string{"workflow:cr-wf"},
	}), &it)
	e.id = it.ID
	return e
}

func (e *storyEnv) dispatch(name string, req map[string]any) json.RawMessage {
	e.t.Helper()
	raw, _ := json.Marshal(req)
	resp, err := verb.Dispatch(context.Background(), name, raw)
	if err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
	return resp
}

func (e *storyEnv) engage() {
	e.t.Helper()
	e.dispatch("story-set", map[string]any{"id": e.id, "status": "in_progress"})
}

// recordChange writes a change_record row naming files, as a transition does.
func (e *storyEnv) recordChange(files ...string) {
	e.t.Helper()
	payload, _ := json.Marshal(map[string]any{"from": "backlog", "to": "in_progress", "files": files, "file_count": len(files)})
	if _, err := e.db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: e.id, Kind: ledger.KindChangeRecord, Actor: "executor", Body: "change record", Payload: payload,
	}, time.Now()); err != nil {
		e.t.Fatal(err)
	}
}

// editAfterEngage writes a repo-relative file and stamps it later than the
// engagement baseline, so the live substrate leg sees it.
func editAfterEngage(t *testing.T, repo, rel, body string) {
	t.Helper()
	writeRepoFile(t, repo, rel, body)
	later := time.Now().Add(2 * time.Second) // time-subject: mtime must clear the engagement baseline's timestamp
	if err := os.Chtimes(filepath.Join(repo, filepath.FromSlash(rel)), later, later); err != nil {
		t.Fatal(err)
	}
}

// editBeforeEngage writes a repo-relative file whose mtime predates any
// engagement, so only a recorded change set can name it.
func editBeforeEngage(t *testing.T, repo, rel, body string) {
	t.Helper()
	writeRepoFile(t, repo, rel, body)
	earlier := time.Now().Add(-time.Hour) // time-subject: mtime must precede the engagement baseline's timestamp
	if err := os.Chtimes(filepath.Join(repo, filepath.FromSlash(rel)), earlier, earlier); err != nil {
		t.Fatal(err)
	}
}

// storyStatus runs `sync status --story` as this machine against server url,
// returning stdout and the refusal (which is part of what the operator sees).
func (m *snapMachine) storyStatusAt(url, id string) (string, error) {
	m.use()
	cmd, buf := testCmd()
	err := runSyncStatusForStory(cmd, url, id)
	out := buf.String()
	if err != nil {
		out += err.Error()
	}
	return out, err
}

func (m *snapMachine) storyStatus(id string) (string, error) { return m.storyStatusAt(m.url, id) }

// storyMachine is a published machine with a git tree and a story engaged on it.
func storyMachine(t *testing.T, url string) (*snapMachine, *storyEnv) {
	t.Helper()
	a := publishedMachine(t, url)
	gitTree(t, a.repo, ".satelle/")
	a.use()
	e := engageSyncStory(t, a.repo)
	e.engage()
	return a, e
}

func wantRefusal(t *testing.T, out string, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("a story whose substrate is not on the hosted copy must exit non-zero:\n%s", out)
	}
	for _, p := range parts {
		if !strings.Contains(out, p) {
			t.Errorf("output must contain %q:\n%s", p, out)
		}
	}
}

// AC4: the touched set is the union of the recorded change sets and the live
// substrate leg, so a path named only by a change_record row — its file older
// than the engagement — is still refused, and without the row the same state is
// unaffected.
func TestSyncStatusStoryRecordedOnlyPathIsRefused(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	gitTree(t, a.repo, ".satelle/")
	editBeforeEngage(t, a.repo, ".satelle/skills/y.md", "unpushed\n")
	a.use()
	e := engageSyncStory(t, a.repo)
	e.engage()

	out, err := a.storyStatus(e.id)
	if err != nil || !strings.Contains(out, "touched no synced substrate") {
		t.Fatalf("with no recorded row and no live edit the story touched nothing: %v\n%s", err, out)
	}
	e.recordChange(".satelle/skills/y.md")
	out, err = a.storyStatus(e.id)
	wantRefusal(t, out, err, "skills", "unpushed", ".satelle/skills/y.md", "satelle sync push")
}

// AC7: the area comes from where this repo keeps its files, not from a literal
// ".satelle/" — a relocated data dir is judged the same way.
func TestSyncStatusStoryHonoursNonDefaultDataDir(t *testing.T) {
	testutil.IsolateHome(t)
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	repo := t.TempDir()
	writeRepoFile(t, repo, "meta/satelle.toml", "data_dir = \"meta\"\n[sync]\n"+boundProjectToml)
	writeRepoFile(t, repo, "meta/constitution.md", "# c\n")
	t.Setenv("SATELLE_CONFIG", filepath.Join(repo, "meta", "satelle.toml"))
	hosted.DocumentSyncStatePathOverride = filepath.Join(t.TempDir(), "doc-state.json")
	t.Cleanup(func() { hosted.DocumentSyncStatePathOverride = "" })
	gitTree(t, repo, "meta/")
	e := engageSyncStory(t, repo)
	e.engage()
	editAfterEngage(t, repo, "meta/skills/x.md", "x\n")

	cmd, buf := testCmd()
	err := runSyncStatusForStory(cmd, ts.URL, e.id)
	out := buf.String()
	if err != nil {
		out += err.Error()
	}
	wantRefusal(t, out, err, "skills", "never synced", "meta/skills/x.md", "satelle sync")
}

// The no-baseline answer the check treats as an empty live leg is story-diff's
// own text, and any other refusal is returned rather than read as "touched
// nothing".
func TestStoryTouchedPathsReadsUnengagedAndRefusesUnreadable(t *testing.T) {
	repo := syncConfigRepo(t, snapToml)
	gitTree(t, repo, ".satelle/")
	pointAt(t, repo)
	e := engageSyncStory(t, repo)

	if _, err := verb.Dispatch(context.Background(), "story-diff", json.RawMessage(`{"id":"`+e.id+`","include_substrate":true}`)); err == nil || !strings.Contains(err.Error(), noEngagementBaseline) {
		t.Fatalf("story-diff on an unengaged story must answer %q, got %v", noEngagementBaseline, err)
	}
	if paths, err := storyTouchedPaths(context.Background(), e.id); err != nil || len(paths) != 0 {
		t.Errorf("an unengaged story touched nothing: %v, %v", paths, err)
	}
	if _, err := storyTouchedPaths(context.Background(), "sty_00000000"); err == nil {
		t.Error("an unknown story must be an error, not an empty change set")
	}
}

// sync status prints the machine's push mode: enabled, or disabled when this
// machine is marked pull-only (sty_07eefabe).
func TestSyncStatusShowsPullOnly(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	out, err := a.status()
	if err != nil || !strings.Contains(out, "push: enabled") || strings.Contains(out, "pull-only") {
		t.Fatalf("unmarked machine: err=%v, want push: enabled\n%s", err, out)
	}
	if err := setPullOnly(io.Discard, a.repo, true); err != nil {
		t.Fatal(err)
	}
	out, err = a.status()
	if err != nil || !strings.Contains(out, "push: disabled — pull-only ([sync] pull_only in .satelle/satelle.local.toml)") {
		t.Fatalf("pull-only machine: err=%v, want the pull-only push line\n%s", err, out)
	}
	wantState(t, out, "skills", "in step")
}
