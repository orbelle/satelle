package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/snapsync"
	"github.com/bobmcallan/satelle/internal/syncscope"
)

// `satelle sync pull` and `sync push` (sty_0fa4f732), in the two-machine
// snapshot-sync harness: two checkouts, one fake hosted server, real commands.

func (m *snapMachine) pull(args ...string) (string, error) {
	m.use()
	cmd := newSyncPullCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(append([]string{"--server", m.url}, args...))
	err := cmd.ExecuteContext(context.Background())
	return buf.String(), err
}

// behindFixture leaves B in step with A's first push and then behind it: A edited
// skills/x.md and documents/d.md and pushed; workflows are still in step.
func behindFixture(t *testing.T) (a, b *snapMachine, f *snapFake) {
	t.Helper()
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a = publishedMachine(t, ts.URL)
	b = newSnapMachine(t, ts.URL)
	b.deploy()
	b.pullDocs()
	a.write("skills/x.md", "from A\n")
	a.write("documents/d.md", "d from A\n")
	a.pushConfig()
	a.pushDocs()
	return a, b, f
}

func (m *snapMachine) wantAllInStep() {
	m.t.Helper()
	out, err := m.status()
	if err != nil {
		m.t.Fatalf("expected every area in step: %v\n%s", err, out)
	}
	for _, area := range []string{"skills", "workflows", "documents"} {
		wantState(m.t, out, area, "in step")
	}
}

// A behind area with no local change is brought in: its files equal the hosted
// copy and `sync status` reports it in step. Workflows were already in step.
func TestSyncPullBringsInABehindUneditedArea(t *testing.T) {
	_, b, _ := behindFixture(t)
	out, err := b.pull()
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if got := b.read("skills/x.md"); got != "from A\n" {
		t.Errorf("skills/x.md = %q, want the hosted copy", got)
	}
	if got := b.read("documents/d.md"); got != "d from A\n" {
		t.Errorf("documents/d.md = %q, want the hosted copy", got)
	}
	for _, want := range []string{"skills: pulled — in step", "documents: pulled — in step", "workflows: in step"} {
		if !strings.Contains(out, want) {
			t.Errorf("the full report lacks %q:\n%s", want, out)
		}
	}
	b.wantAllInStep()
}

// The fast-forward form applies the same rule and prints only what it did.
func TestSyncPullFastForwardOnlyAppliesAndIsQuiet(t *testing.T) {
	_, b, _ := behindFixture(t)
	out, err := b.pull("--fast-forward-only")
	if err != nil {
		t.Fatalf("pull --fast-forward-only: %v\n%s", err, out)
	}
	if b.read("skills/x.md") != "from A\n" || b.read("documents/d.md") != "d from A\n" {
		t.Errorf("a behind area was not fast-forwarded:\n%s", out)
	}
	if strings.Contains(out, "workflows: in step") {
		t.Errorf("the quiet form reported an area it did not pull:\n%s", out)
	}
	if !strings.Contains(out, "skills: pulled") {
		t.Errorf("the quiet form must still name an area it pulled:\n%s", out)
	}
	b.wantAllInStep()
}

// An area whose hosted copy holds no snapshot is reported and left untouched.
func TestSyncPullNoHostedSnapshotLeavesTheAreaUntouched(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	m := newSnapMachine(t, ts.URL)
	m.write("skills/x.md", "local only\n")
	out, err := m.pull()
	if err != nil {
		t.Fatalf("pull with nothing hosted must exit zero: %v\n%s", err, out)
	}
	if !strings.Contains(out, "holds no snapshot yet") || !strings.Contains(out, "satelle sync push") {
		t.Errorf("the report does not name the missing snapshot and the push:\n%s", out)
	}
	if got := m.read("skills/x.md"); got != "local only\n" {
		t.Errorf("skills/x.md = %q, want it untouched", got)
	}
	if base, ok := m.base("skills"); ok {
		t.Errorf("a pull with no snapshot recorded a base: %+v", base)
	}
}

// A locally edited area is diverged: pull leaves its bytes alone, names reconcile
// and exits non-zero — while an unedited behind area is still brought in.
func TestSyncPullLocalEditLeavesBytesUnchanged(t *testing.T) {
	for _, flags := range [][]string{nil, {"--fast-forward-only"}} {
		_, b, _ := behindFixture(t)
		b.write("skills/x.md", "from B\n")
		before, _ := b.base("skills")

		out, err := b.pull(flags...)
		if err == nil {
			t.Fatalf("%v: a diverged area must make pull exit non-zero:\n%s", flags, out)
		}
		if !strings.Contains(out, "skills: diverged") || !strings.Contains(out, "satelle sync reconcile") {
			t.Errorf("%v: the report does not name the diverged area and reconcile:\n%s", flags, out)
		}
		if got := b.read("skills/x.md"); got != "from B\n" {
			t.Errorf("%v: skills/x.md = %q, want the local edit kept", flags, got)
		}
		if after, _ := b.base("skills"); after.Version != before.Version {
			t.Errorf("%v: the diverged area's base moved: %d -> %d", flags, before.Version, after.Version)
		}
		if got := b.read("documents/d.md"); got != "d from A\n" {
			t.Errorf("%v: the unedited behind area was not pulled: %q", flags, got)
		}
	}
}

// An area that is only unpushed has nothing to pull: reported with the push,
// exit zero, files unchanged. The quiet form says nothing at all.
func TestSyncPullUnpushedAreaIsANoOp(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	a.write("skills/x.md", "edited here\n")

	out, err := a.pull()
	if err != nil {
		t.Fatalf("an unpushed area must exit zero: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skills: unpushed") || !strings.Contains(out, "satelle sync push") {
		t.Errorf("the report does not name the unpushed area and the push:\n%s", out)
	}
	if got := a.read("skills/x.md"); got != "edited here\n" {
		t.Errorf("skills/x.md = %q, want it unchanged", got)
	}

	out, err = a.pull("--fast-forward-only")
	if err != nil {
		t.Fatalf("--fast-forward-only on an unpushed area: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("an unpushed area is a quiet no-op in the fast-forward form, got:\n%s", out)
	}
}

// A signed-out machine is local: nothing is pulled, nothing is asked of the
// server, and pull says to sign in.
func TestSyncPullSignedOutIsLocal(t *testing.T) {
	ts, _ := newSnapServer(t) // no credential seeded
	m := newSnapMachine(t, ts.URL)
	m.write("skills/x.md", "x\n")
	out, err := m.pull("--fast-forward-only")
	if err != nil || !strings.Contains(out, "scope: local — "+syncscope.ReasonSignedOut) {
		t.Fatalf("a signed-out pull is local and names the login: %v\n%s", err, out)
	}
	if got := m.read("skills/x.md"); got != "x\n" {
		t.Errorf("skills/x.md = %q, want it untouched", got)
	}
}

// An unreadable hosted copy (here, a server that is gone) is unavailable:
// nothing is pulled and pull exits non-zero with the next command.
func TestSyncPullUnavailableExitsNonZero(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	signInTo(t, gone.URL)
	m := newSnapMachine(t, gone.URL)
	m.write("skills/x.md", "x\n")
	out, err := m.pull("--fast-forward-only")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("an unavailable hosted copy must exit non-zero: %v\n%s", err, out)
	}
	if !strings.Contains(out, "satelle sync") {
		t.Errorf("the report does not name the next command:\n%s", out)
	}
	if got := m.read("skills/x.md"); got != "x\n" {
		t.Errorf("skills/x.md = %q, want it untouched", got)
	}
}

func TestSyncPullNothingOptedIn(t *testing.T) {
	tempRepo(t)
	out, err := runRoot(t, "sync", "pull")
	if err != nil || !strings.Contains(out, "Nothing to pull") {
		t.Fatalf("pull with no area opted in: err=%v\n%s", err, out)
	}
}

// A hosted snapshot naming bytes the hosted copy does not hold is an incomplete
// entry: the area is not a fast-forward, so it is left as it was and reported.
func TestSyncPullIncompleteEntryIsLeftUntouched(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := publishedMachine(t, ts.URL)
	_ = a
	b := newSnapMachine(t, ts.URL)
	b.deploy()
	f.forgeRecord(t, "skills", 1, map[string]string{"skills/x.md": snapsync.SHA([]byte("not uploaded\n"))})
	before, _ := b.base("skills")

	out, err := b.pull("--fast-forward-only")
	if err == nil {
		t.Fatalf("an incomplete entry must make the fast-forward exit non-zero:\n%s", out)
	}
	if !strings.Contains(out, "skills: behind, left unchanged") || !strings.Contains(out, "satelle sync reconcile") {
		t.Errorf("the refusal does not name the area and reconcile:\n%s", out)
	}
	if got := b.read("skills/x.md"); got != "x\n" {
		t.Errorf("skills/x.md = %q, want it untouched", got)
	}
	if after, _ := b.base("skills"); after.Version != before.Version {
		t.Errorf("the base moved: %d -> %d", before.Version, after.Version)
	}
}

// An edit placed after `sync status` classified the area and before the pull
// plans it turns the area away: the apply plans again at the point of writing.
func TestSyncPullEditAfterStatusLeavesTheAreaUntouched(t *testing.T) {
	_, b, _ := behindFixture(t)
	syncPullBeforeApply = func() { b.write("skills/x.md", "raced in\n") }
	t.Cleanup(func() { syncPullBeforeApply = nil })

	out, err := b.pull("--fast-forward-only")
	if err == nil {
		t.Fatalf("an area edited after status must make the fast-forward exit non-zero:\n%s", out)
	}
	if got := b.read("skills/x.md"); got != "raced in\n" {
		t.Errorf("skills/x.md = %q, want the raced edit kept", got)
	}
	if !strings.Contains(out, "skills: behind, left unchanged") || !strings.Contains(out, "satelle sync reconcile") {
		t.Errorf("the refusal does not name the area and reconcile:\n%s", out)
	}
}

// A pull never reaches work-state or the deploy's materialise-every-head path:
// a file the unedited behind area did not change is not rewritten.
func TestSyncPullRewritesOnlyWhatTheSnapshotChanged(t *testing.T) {
	_, b, _ := behindFixture(t)
	info, err := os.Stat(b.path("workflows/w.md"))
	if err != nil {
		t.Fatal(err)
	}
	old := info.ModTime().Add(-1_000_000_000_000) // a distinct, earlier mtime
	if err := os.Chtimes(b.path("workflows/w.md"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(b.path("skills/y.md"), old, old); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if out, err := b.pull(); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	after, err := os.Stat(b.path("workflows/w.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(old) {
		t.Errorf("an in-step area's file was rewritten by the pull (mtime %v, want %v)", after.ModTime(), old)
	}
}

// rehydrate no longer answers to "pull": `sync pull` is its own command.
func TestSyncPullIsNotARehydrateAlias(t *testing.T) {
	root := NewRootCmd()
	pull, _, err := root.Find([]string{"sync", "pull"})
	if err != nil || pull.Name() != "pull" {
		t.Fatalf("sync pull resolved to %v (%v)", pull.Name(), err)
	}
	rehydrate, _, err := root.Find([]string{"sync", "rehydrate"})
	if err != nil || rehydrate.Name() != "rehydrate" {
		t.Fatalf("sync rehydrate resolved to %v (%v)", rehydrate.Name(), err)
	}
	for _, a := range rehydrate.Aliases {
		if a == "pull" {
			t.Errorf("rehydrate still has the pull alias: %v", rehydrate.Aliases)
		}
	}
}

// `sync --help` lists push, pull, reconcile and status ahead of the per-area
// commands and describes rehydrate and config deploy --version by what is
// different about them.
func TestSyncHelpListsTheGitAlignedVerbsFirst(t *testing.T) {
	out, err := runRoot(t, "sync", "--help")
	if err != nil {
		t.Fatalf("sync --help: %v\n%s", err, out)
	}
	perArea := strings.Index(out, "Per-area and recovery commands:")
	if perArea < 0 {
		t.Fatalf("sync --help has no per-area group:\n%s", out)
	}
	for _, verb := range []string{"push", "pull", "reconcile", "status"} {
		if i := strings.Index(out, "\n  "+verb+" "); i < 0 || i > perArea {
			t.Errorf("%s must be listed before the per-area commands:\n%s", verb, out)
		}
	}
	for _, verb := range []string{"bindings", "config", "documents", "rehydrate", "workstate"} {
		if i := strings.Index(out, "\n  "+verb+" "); i < perArea {
			t.Errorf("%s must be listed under the per-area commands:\n%s", verb, out)
		}
	}
	for _, want := range []string{"full-tree recovery", "config deploy --version"} {
		if !strings.Contains(out, want) {
			t.Errorf("sync --help does not describe %q:\n%s", want, out)
		}
	}
}

// push runs the push legs of every opted-in area through the one composition
// bare sync runs, and pulls nothing: under --dry-run it previews config,
// documents and work-state in that order and has no pull note at all, which
// bare sync prints.
func TestSyncPushRunsOnlyThePushLegs(t *testing.T) {
	repo := tempRepo(t)
	signInTo(t, "http://sync.example.invalid")
	portableAgents(t, repo)
	cfg := "[sync]\nproject = \"probe\"\n"
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, repo, ".satelle/skills/my-skill.md", "---\ntype: skill\n---\nbody\n")
	writeRepoFile(t, repo, ".satelle/documents/doc.md", "# Doc\n")

	out, err := runRoot(t, "sync", "push", "--dry-run", "--server", "http://sync.example.invalid")
	if err != nil {
		t.Fatalf("sync push --dry-run: %v\n%s", err, out)
	}
	iConfig, iDoc, iWork := strings.Index(out, "skills/my-skill.md"), strings.Index(out, "documents/doc.md"), strings.Index(out, "work-state areas")
	if iConfig < 0 || iDoc < 0 || iWork < 0 || !(iConfig < iDoc && iDoc < iWork) {
		t.Errorf("push legs ran out of order or not at all (config=%d doc=%d work=%d):\n%s", iConfig, iDoc, iWork, out)
	}
	if strings.Contains(out, "pull") {
		t.Errorf("sync push mentions a pull:\n%s", out)
	}
	bare, err := runRoot(t, "sync", "--dry-run", "--server", "http://sync.example.invalid")
	if err != nil || !strings.Contains(bare, "documents pull: skipped") {
		t.Errorf("bare sync lost its documents pull (err=%v):\n%s", err, bare)
	}
}

// A live `sync push` publishes config, documents and work-state and pulls
// nothing: a document only the server holds does not land locally, unlike under
// bare sync.
func TestSyncPushLiveDoesNotPull(t *testing.T) {
	ts, f := newFakeSyncServer(t)
	seedCred(t, ts.URL)
	repo := syncConfigRepo(t, "[sync]\n"+boundProjectToml)
	writeRepoFile(t, repo, ".satelle/skills/my-skill.md", "---\ntype: skill\n---\nbody\n")
	writeRepoFile(t, repo, ".satelle/documents/local-doc.md", "# local\n")
	pointAt(t, repo)
	f.docs.put("probe", "documents/remote-only.md", []byte("# remote\n"))
	if out, err := runRoot(t, "story", "create", "--title", "Push probe", "--body", "x", "--acceptance", "1. ok"); err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}

	out, err := runRoot(t, "sync", "push", "--server", ts.URL, "--dry-run=false")
	if err != nil {
		t.Fatalf("sync push: %v\n%s", err, out)
	}
	if _, _, ok := f.cfg.get("probe", "skills/my-skill.md", 0); !ok {
		t.Errorf("config push did not reach the server:\n%s", out)
	}
	if _, _, ok := f.docs.get("probe", "documents/local-doc.md"); !ok {
		t.Errorf("documents push did not reach the server:\n%s", out)
	}
	f.mu.Lock()
	posts := f.workstate
	f.mu.Unlock()
	if posts == 0 {
		t.Errorf("work-state push did not reach the server:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".satelle", "documents", "remote-only.md")); err == nil {
		t.Errorf("sync push pulled a hosted document:\n%s", out)
	}
}
