package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/wfgovern"
)

// Work-state push holds a story's rows until its code is on the git remote
// (sty_7361569a). These tests drive a real bare remote and real clones; only the
// hosted server is faked.

const holdSyncToml = "[sync]\n\n[hosted]\nproject = \"probe\"\n"

// holdRemote is a bare remote with a main branch that has one commit.
func holdRemote(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "remote.git")
	gitIn(t, filepath.Dir(bare), "init", "--bare", "-b", "main", bare)
	seed := filepath.Join(t.TempDir(), "seed")
	gitIn(t, filepath.Dir(seed), "clone", bare, seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-m", "seed")
	gitIn(t, seed, "push", "origin", "HEAD:main")
	return bare
}

func cloneOf(t *testing.T, bare string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	gitIn(t, filepath.Dir(dir), "clone", bare, dir)
	return dir
}

// makeRepoAClone turns the satelle repo itself into a clone of bare, keeping the
// satelle substrate out of git status.
func makeRepoAClone(t *testing.T, repo, bare string) {
	t.Helper()
	gitIn(t, repo, "init", "-b", "main")
	gitIn(t, repo, "remote", "add", "origin", bare)
	gitIn(t, repo, "fetch", "origin")
	gitIn(t, repo, "reset", "--hard", "origin/main")
	gitIn(t, repo, "branch", "--set-upstream-to=origin/main", "main")
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte(".satelle/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func createStoryID(t *testing.T, title string) string {
	t.Helper()
	out, err := runRoot(t, "story", "create", "--title", title, "--body", "b", "--acceptance", "1. x")
	if err != nil {
		t.Fatalf("create %s: %v\n%s", title, err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&created); err != nil || created.ID == "" {
		t.Fatalf("parse create: %v %s", err, out)
	}
	return created.ID
}

// routeStates returns an executor-allocated state and a terminal state of the
// workflow that governs the story, read from the route like the hold does.
func routeStates(t *testing.T, a *app.App, id string) (executor, terminal string) {
	t.Helper()
	ctx := context.Background()
	it, err := a.Store.Stories.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	wfs, err := a.Store.DocIndex.List(ctx, "workflows")
	if err != nil {
		t.Fatal(err)
	}
	route, _, err := wfgovern.RouteFor(wfs, it)
	if err != nil {
		t.Fatal(err)
	}
	if ex := route.Spec.EditCapableStates(); len(ex) > 0 {
		executor = ex[0]
	}
	for _, s := range route.Spec.States {
		if route.Spec.IsTerminalState(s.Name) {
			terminal = s.Name
			break
		}
	}
	if executor == "" || terminal == "" {
		t.Fatalf("route has executor=%q terminal=%q", executor, terminal)
	}
	return executor, terminal
}

// putInState moves a story to status as a committed transition and, for an
// engaged state, records the engagement baseline taken from tree.
func putInState(t *testing.T, id, status, tree, head string, baseline bool) {
	t.Helper()
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	row, err := a.Store.Stories.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.Stories.SetStatus(ctx, id, status, now); err != nil {
		t.Fatal(err)
	}
	// from is the status the chain is at: a transition from any other is stale
	// and the fold skips it.
	payload, _ := json.Marshal(map[string]string{"from": row.Status, "to": status})
	if _, err := a.Store.Ledger.Append(ctx, ledger.AppendInput{
		StoryID: id, Kind: ledger.KindStatusTransition, Actor: "executor", Body: row.Status + " → " + status, Payload: payload,
	}, now); err != nil {
		t.Fatal(err)
	}
	settleRow(t, a, id) // the row is a cache of the chain just written
	if !baseline {
		return
	}
	bp, _ := json.Marshal(map[string]any{"head_sha": head, "to": status, "worktree": tree})
	if _, err := a.Store.Ledger.Append(ctx, ledger.AppendInput{
		StoryID: id, Kind: ledger.KindEngagementBaseline, Actor: "executor", Body: "baseline", Payload: bp,
	}, now); err != nil {
		t.Fatal(err)
	}
}

func executorStateFor(t *testing.T, id string) (executor, terminal string) {
	t.Helper()
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	return routeStates(t, a, id)
}

func (f *fakeWorkstateServer) hasItem(project, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.itemsByID[project][id]
	return ok
}

// hostedItem is the record the server holds for id, or nil.
func (f *fakeWorkstateServer) hostedItem(project, id string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, _ := f.itemsByID[project][id].(map[string]any)
	return rec
}

func (f *fakeWorkstateServer) ledgerCount(project, storyID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, raw := range f.ledgerByID[project] {
		if m, _ := raw.(map[string]any); m["story_id"] == storyID {
			n++
		}
	}
	return n
}

type holdWorld struct {
	f                         *fakeWorkstateServer
	url                       string
	repo, bare                string
	exec, term                string
	dirty, unpushed, ok, plan string // story ids
	unpushedTree, cleanTree   string
	dirtyFile                 string
}

// newHoldWorld builds four stories against real git state:
//   - dirty:    in an executor state, its tree (the satelle repo) has an uncommitted file
//   - unpushed: in an executor state, its tree has a commit no remote ref contains
//   - ok:       in an executor state, clean tree, HEAD on origin
//   - plan:     never entered an executor state, sharing the dirty story's tree
func newHoldWorld(t *testing.T, syncToml string) *holdWorld {
	t.Helper()
	return newHoldWorldWith(t, syncToml, nil)
}

// newHoldWorldWith is newHoldWorld on a server that also serves extra routes.
func newHoldWorldWith(t *testing.T, syncToml string, routes func(mux *http.ServeMux, f *fakeWorkstateServer)) *holdWorld {
	t.Helper()
	ts, f := newFakeWorkstateServerWith(t, routes)
	seedCred(t, ts.URL)
	w := &holdWorld{f: f, url: ts.URL}
	w.repo = workstateRepo(t, syncToml)
	w.bare = holdRemote(t)
	makeRepoAClone(t, w.repo, w.bare)
	repoHead := gitIn(t, w.repo, "rev-parse", "HEAD")

	w.unpushedTree = cloneOf(t, w.bare)
	w.cleanTree = cloneOf(t, w.bare)

	w.dirty = createStoryID(t, "dirty in flight")
	w.unpushed = createStoryID(t, "committed not pushed")
	w.ok = createStoryID(t, "clean and pushed")
	w.plan = createStoryID(t, "still planning")
	w.exec, w.term = executorStateFor(t, w.dirty)

	putInState(t, w.dirty, w.exec, w.repo, repoHead, true)
	putInState(t, w.unpushed, w.exec, w.unpushedTree, gitIn(t, w.unpushedTree, "rev-parse", "HEAD"), true)
	putInState(t, w.ok, w.exec, w.cleanTree, gitIn(t, w.cleanTree, "rev-parse", "HEAD"), true)
	// The planning story carries a baseline on the shared dirty tree, so only the
	// executor-eligibility gate keeps it from being held with the dirty story.
	putInState(t, w.plan, "plan", w.repo, repoHead, true)

	// Work done after the last transition: no new ledger row, only git state.
	w.dirtyFile = filepath.Join(w.repo, "feature.go")
	if err := os.WriteFile(w.dirtyFile, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.unpushedTree, "work.go"), []byte("package y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, w.unpushedTree, "add", "-A")
	gitIn(t, w.unpushedTree, "commit", "-m", "work")
	return w
}

func (w *holdWorld) push(t *testing.T, extra ...string) string {
	t.Helper()
	out, err := runRoot(t, append([]string{"sync", "workstate", "push", "--server", w.url}, extra...)...)
	if err != nil {
		t.Fatalf("push must exit zero: %v\n%s", err, out)
	}
	return out
}

func (w *holdWorld) assertPublished(t *testing.T, id string, want bool) {
	t.Helper()
	if got := w.f.hasItem("probe", id); got != want {
		t.Errorf("story %s item published = %v, want %v", id, got, want)
	}
	// A published story has every one of its local ledger rows on the server; a
	// held story has none.
	wantRows := 0
	if want {
		wantRows = localLedgerCount(t, id)
		if wantRows == 0 {
			t.Fatalf("story %s has no local ledger rows to compare", id)
		}
	}
	if got := w.f.ledgerCount("probe", id); got != wantRows {
		t.Errorf("story %s ledger rows on server = %d, want %d (published=%v)", id, got, wantRows, want)
	}
}

// localLedgerCount is the number of ledger rows this machine holds for the story.
func localLedgerCount(t *testing.T, id string) int {
	t.Helper()
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rows, err := a.Store.Ledger.ListByStory(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// AC1, AC2, AC3, AC5: dirty and unpushed in-flight stories are held with a
// reason line, everything else is sent, and push exits zero.
func TestWorkstatePushHoldsStoriesWhoseCodeIsNotOnTheRemote(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	out := w.push(t)

	if !strings.Contains(out, "held "+w.dirty+": uncommitted changes in "+w.repo) {
		t.Errorf("missing uncommitted line for %s in:\n%s", w.dirty, out)
	}
	head := gitIn(t, w.unpushedTree, "rev-parse", "HEAD")[:8]
	if !strings.Contains(out, "held "+w.unpushed+": head "+head+" is not on any remote branch") {
		t.Errorf("missing not-on-remote line for %s in:\n%s", w.unpushed, out)
	}
	if strings.Contains(out, "held "+w.ok) || strings.Contains(out, "held "+w.plan) {
		t.Errorf("clean pushed or planning story reported held:\n%s", out)
	}
	w.assertPublished(t, w.dirty, false)
	w.assertPublished(t, w.unpushed, false)
	w.assertPublished(t, w.ok, true)
	// The planning story shares the dirty story's tree and still publishes.
	w.assertPublished(t, w.plan, true)
}

// AC4: once the code is on the remote the next push sends every held row, with
// no new transition and no --full.
func TestWorkstatePushPublishesHeldRowsOnceCodeIsPushed(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	w.push(t)
	w.assertPublished(t, w.dirty, false)
	w.assertPublished(t, w.unpushed, false)

	// Still held while nothing has changed.
	w.push(t)
	w.assertPublished(t, w.dirty, false)

	gitIn(t, w.repo, "add", "-A")
	gitIn(t, w.repo, "commit", "-m", "feature")
	gitIn(t, w.repo, "push", "origin", "HEAD:main")
	gitIn(t, w.unpushedTree, "push", "origin", "HEAD:refs/heads/side")

	out := w.push(t)
	if strings.Contains(out, "held ") {
		t.Errorf("nothing should be held once the code is pushed:\n%s", out)
	}
	w.assertPublished(t, w.dirty, true)
	w.assertPublished(t, w.unpushed, true)
}

// sty_c203fd62 AC1: a push whose only unpublished stories are held exits zero,
// records no failure reason and records the held ids with their reasons apart
// from it; once the code is on the remote the next push clears them.
func TestWorkstatePushRecordsHeldStoriesApartFromFailure(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	w.push(t) // fails the test on a non-zero exit

	st, ok, err := syncstate.Load(config.GlobalDir(), w.repo)
	if err != nil || !ok {
		t.Fatalf("no sync state recorded: ok=%v err=%v", ok, err)
	}
	if st.PushReason != "" {
		t.Errorf("a push that only held stories recorded a failure reason: %q", st.PushReason)
	}
	if st.PushLastSuccess.IsZero() {
		t.Error("a push that only held stories must record success")
	}
	if got := st.PushHeld[w.dirty]; got != "uncommitted changes in "+w.repo {
		t.Errorf("held reason for %s = %q, want uncommitted changes in %s", w.dirty, got, w.repo)
	}
	head := gitIn(t, w.unpushedTree, "rev-parse", "HEAD")[:8]
	if got := st.PushHeld[w.unpushed]; got != "head "+head+" is not on any remote branch" {
		t.Errorf("held reason for %s = %q", w.unpushed, got)
	}
	if len(st.PushHeld) != 2 {
		t.Errorf("only the two held stories may be recorded: %+v", st.PushHeld)
	}

	gitIn(t, w.repo, "add", "-A")
	gitIn(t, w.repo, "commit", "-m", "feature")
	gitIn(t, w.repo, "push", "origin", "HEAD:main")
	gitIn(t, w.unpushedTree, "push", "origin", "HEAD:refs/heads/side")
	w.push(t)
	if st, _, _ := syncstate.Load(config.GlobalDir(), w.repo); len(st.PushHeld) != 0 {
		t.Errorf("held stories must clear once their code is pushed: %+v", st.PushHeld)
	}
}

// A held story only holds its own rows back: a later row of another story is
// still sent, and the held rows survive until the story's code is pushed.
func TestWorkstatePushHeldRowsDoNotBlockLaterRows(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	w.push(t)
	later := createStoryID(t, "created after the hold")
	w.push(t)
	w.assertPublished(t, later, true)
	w.assertPublished(t, w.dirty, false)
}

// AC5: terminal stories publish whatever their tree looks like, and an
// unreachable tree or a repo with no git is reported, not held.
func TestWorkstatePushReachabilityUnavailableHoldsNothing(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, holdSyncToml)

	done := createStoryID(t, "already done")
	noGit := createStoryID(t, "engaged with no git")
	gone := createStoryID(t, "tree left on another machine")
	exec, term := executorStateFor(t, done)

	missing := filepath.Join(t.TempDir(), "not-here")
	putInState(t, done, term, missing, "abc123", true)
	putInState(t, noGit, exec, "", "", true)
	putInState(t, gone, exec, missing, "abc123", true)

	out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if strings.Contains(out, "held ") {
		t.Errorf("nothing may be held on unavailable reachability:\n%s", out)
	}
	if !strings.Contains(out, "reachability not checked for 2 stor(y/ies)") {
		t.Errorf("want the two engaged stories counted as not checked, the terminal one not:\n%s", out)
	}
	for _, id := range []string{done, noGit, gone} {
		if !f.hasItem("probe", id) {
			t.Errorf("story %s should have published", id)
		}
	}

	out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL, "--full", "--verbose")
	if err != nil {
		t.Fatalf("verbose push: %v\n%s", err, out)
	}
	if strings.Contains(out, "reachability unavailable") && strings.Contains(out, done) {
		t.Errorf("terminal story was checked:\n%s", out)
	}
	for _, id := range []string{noGit, gone} {
		if !strings.Contains(out, "): "+id+" not checked") || !strings.Contains(out, "reachability unavailable (") {
			t.Errorf("missing unavailable line for %s:\n%s", id, out)
		}
	}
}

// unavailableWorld is a repo with hold on and two engaged stories whose recorded
// engagement tree is not on this machine, as when they were engaged elsewhere.
func unavailableWorld(t *testing.T) (f *fakeWorkstateServer, url, repo string, ids []string) {
	t.Helper()
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	repo = workstateRepo(t, holdSyncToml)
	elsewhere := filepath.Join(t.TempDir(), "Development", "engaged-on-another-machine")
	for _, title := range []string{"engaged elsewhere one", "engaged elsewhere two"} {
		id := createStoryID(t, title)
		exec, _ := executorStateFor(t, id)
		putInState(t, id, exec, elsewhere, "abc123", true)
		ids = append(ids, id)
	}
	return f, ts.URL, repo, ids
}

// syncFailing is what satelle doctor reports as sync.failing for repo.
func syncFailing(repo string) health.Findings {
	rep := doctor.Check(context.Background(), doctor.Opts{RepoRoot: repo})
	return byID(rep.Findings, health.IDSyncFailing)
}

// sty_906870a6 AC1, AC2: a push whose only gap is reachability it could not
// check succeeds, says so in one summary line naming no story (the ids only
// under --verbose), and records a success doctor does not flag.
func TestWorkstatePushUnavailableIsOneSummaryLine(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		f, url, repo, ids := unavailableWorld(t)
		out, err := runRoot(t, "sync", "workstate", "push", "--server", url)
		if err != nil {
			t.Fatalf("push must exit zero: %v\n%s", err, out)
		}
		var summaries int
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "reachability not checked for 2 stor(y/ies)") {
				summaries++
			}
		}
		if summaries != 1 {
			t.Errorf("want exactly one summary line counting 2, got %d:\n%s", summaries, out)
		}
		for _, id := range ids {
			if strings.Contains(out, id) {
				t.Errorf("story %s named without --verbose:\n%s", id, out)
			}
			if !f.hasItem("probe", id) {
				t.Errorf("story %s should have published", id)
			}
		}

		st, _, lerr := syncstate.Load(config.GlobalDir(), repo)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if st.PushReason != "" || st.PushLastSuccess.IsZero() {
			t.Errorf("recorded push outcome = reason %q, last success %v; want a success", st.PushReason, st.PushLastSuccess)
		}
		if got := syncFailing(repo); len(got) != 0 {
			t.Errorf("doctor reports sync.failing after a push that published everything: %+v", got)
		}
	})
	t.Run("verbose", func(t *testing.T) {
		_, url, _, ids := unavailableWorld(t)
		out, err := runRoot(t, "sync", "workstate", "push", "--server", url, "--verbose")
		if err != nil {
			t.Fatalf("push must exit zero: %v\n%s", err, out)
		}
		if !strings.Contains(out, "reachability not checked for 2 stor(y/ies)") {
			t.Errorf("missing summary line:\n%s", out)
		}
		for _, id := range ids {
			if !strings.Contains(out, "): "+id+" not checked") {
				t.Errorf("--verbose does not list %s:\n%s", id, out)
			}
		}
	})
}

// sty_906870a6 AC3 (and AC2's control): a story the server refuses still fails
// the push, and stdout names it beside the not-checked summary, so the refusal
// reads as the cause; stderr keeps the reason, and doctor sees the failure.
func TestWorkstatePushRefusalIsNamedOnStdoutBesideUnavailable(t *testing.T) {
	f, url, repo, unavailable := unavailableWorld(t)
	refused := createStoryID(t, "refused by the server")
	f.chain.refuse[refused] = status.Error(codes.PermissionDenied, "viewers may not append")

	stdout, stderr, err := runRootSplit(t, "", "sync", "workstate", "push", "--server", url)
	if err == nil {
		t.Fatalf("push must exit non-zero:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(err.Error(), refused) {
		t.Errorf("exit error %q does not name the refused story", err)
	}
	if !strings.Contains(stdout, "not published ("+refused+")") {
		t.Errorf("stdout does not name the refused story:\n%s", stdout)
	}
	if !strings.Contains(stdout, "reachability not checked for 2 stor(y/ies)") {
		t.Errorf("stdout lost the not-checked summary:\n%s", stdout)
	}
	if !strings.Contains(stderr, "the story "+refused+" was not published") || !strings.Contains(stderr, "viewers may not append") {
		t.Errorf("stderr lost the refusal reason:\n%s", stderr)
	}
	for _, id := range unavailable {
		if strings.Contains(err.Error(), id) {
			t.Errorf("a story not checked was blamed for the failure: %v", err)
		}
	}

	got := syncFailing(repo)
	if len(got) != 1 || !strings.Contains(got[0].Detail, refused) {
		t.Errorf("doctor sync.failing = %+v, want one naming %s", got, refused)
	}
}

// AC6: [sync] hold_unpushed = false sends the story anyway (a bare TOML boolean).
func TestWorkstatePushHoldOffSendsUnpushedStories(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml+"\n")
	// Rewrite the config with the switch off.
	cfg := filepath.Join(w.repo, ".satelle", "satelle.toml")
	if err := os.WriteFile(cfg, []byte("[sync]\nhold_unpushed = false\n\n[hosted]\nproject = \"probe\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := w.push(t)
	if strings.Contains(out, "held ") {
		t.Errorf("hold is off but push reported a hold:\n%s", out)
	}
	w.assertPublished(t, w.dirty, true)
	w.assertPublished(t, w.unpushed, true)
}

// AC6: a value that is not a boolean is a config error, not a silent default.
func TestWorkstatePushHoldUnpushedBadValueIsConfigError(t *testing.T) {
	w := newHoldWorld(t, "[sync]\nhold_unpushed = \"maybe\"\n\n[hosted]\nproject = \"probe\"\n")
	out, err := runRoot(t, "sync", "workstate", "push", "--server", w.url)
	if err == nil || !strings.Contains(err.Error(), "hold_unpushed") {
		t.Fatalf("want a hold_unpushed config error, got err=%v out=%s", err, out)
	}
	if w.f.postCount("probe") != 0 {
		t.Error("a config error must not push")
	}
}

// AC6: --dry-run names the stories a push would hold and contacts nothing.
func TestWorkstatePushDryRunListsHeldStories(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	out := w.push(t, "--dry-run")
	if !strings.Contains(out, "held "+w.dirty+": uncommitted changes in "+w.repo) {
		t.Errorf("dry-run missing dirty line:\n%s", out)
	}
	if !strings.Contains(out, "held "+w.unpushed+": head ") {
		t.Errorf("dry-run missing unpushed line:\n%s", out)
	}
	if strings.Contains(out, "held "+w.ok) || strings.Contains(out, "held "+w.plan) {
		t.Errorf("dry-run holds a story it should not:\n%s", out)
	}
	if w.f.postCount("probe") != 0 {
		t.Error("dry-run contacted the server")
	}
}

// coderFreezeWorld is a repo whose route has no executor step: the freeze step
// is performed by a dispatched coder (sty_87f407ef). Both stories share the
// satelle repo as their dirty tree.
type coderFreezeWorld struct {
	f            *fakeWorkstateServer
	url          string
	repo         string
	coding, plan string // story ids
}

func newCoderFreezeWorld(t *testing.T) *coderFreezeWorld {
	t.Helper()
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	w := &coderFreezeWorld{f: f, url: ts.URL}
	w.repo = workstateRepo(t, holdSyncToml)
	makeRepoAClone(t, w.repo, holdRemote(t))

	writeRoute(t, filepath.Join(w.repo, ".satelle", "workflows"),
		`["*"]
obligations = ["raised", "planned", "coded", "closed"]
park = { state = "blocked" }
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "planner"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "coder"
freeze = true
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.DocIndex.SyncRoots(context.Background(), a.AuthoredDirs().Index(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	a.Close()

	w.coding = createStoryID(t, "coder at the freeze step")
	w.plan = createStoryID(t, "planner before the freeze step")
	return w
}

// codeBearingState is the first code-bearing state of the route that governs id.
func codeBearingState(t *testing.T, id string) string {
	t.Helper()
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	it, err := a.Store.Stories.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	wfs, err := a.Store.DocIndex.List(ctx, "workflows")
	if err != nil {
		t.Fatal(err)
	}
	route, _, err := wfgovern.RouteFor(wfs, it)
	if err != nil {
		t.Fatal(err)
	}
	if got := route.Spec.EditCapableStates(); len(got) != 0 {
		t.Fatalf("fixture must have no executor step, EditCapableStates = %v", got)
	}
	states := route.Spec.CodeBearingStates()
	if len(states) == 0 {
		t.Fatal("route has no code-bearing state")
	}
	return states[0]
}

// A story being coded at a freeze step a coder performs, with work only in an
// uncommitted tree and no later transition, is held and named; a story still in
// the readiness step sharing that dirty tree is published (sty_87f407ef AC1, AC2).
func TestWorkstatePushHoldsCoderStoryAtFreezeStep(t *testing.T) {
	w := newCoderFreezeWorld(t)
	head := gitIn(t, w.repo, "rev-parse", "HEAD")
	putInState(t, w.coding, codeBearingState(t, w.coding), w.repo, head, true)
	putInState(t, w.plan, "plan", w.repo, head, true)
	if err := os.WriteFile(filepath.Join(w.repo, "feature.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runRoot(t, "sync", "workstate", "push", "--server", w.url)
	if err != nil {
		t.Fatalf("push must exit zero: %v\n%s", err, out)
	}
	if !strings.Contains(out, "held "+w.coding+": uncommitted changes in "+w.repo) {
		t.Errorf("coder story at the freeze step not held and named:\n%s", out)
	}
	if strings.Contains(out, "held "+w.plan) {
		t.Errorf("story in the readiness step reported held:\n%s", out)
	}
	if w.f.hasItem("probe", w.coding) || w.f.ledgerCount("probe", w.coding) != 0 {
		t.Errorf("held coder story %s reached the server", w.coding)
	}
	if !w.f.hasItem("probe", w.plan) {
		t.Errorf("planning story %s with a dirty tree was not published", w.plan)
	}
}
