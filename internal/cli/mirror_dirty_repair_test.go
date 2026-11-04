package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/serve"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// repairRetryTick is the reconciler retry tick these tests run, and repairBudget
// how long a repaired push may take to reach the page.
const (
	repairRetryTick = 20 * time.Millisecond
	repairBudget    = 3 * time.Second
)

// repairEnv is a progressEnv whose mirror ingest can be told to refuse
// snapshots, and a running satelled reconciler that reseeds the repo from its
// store, as `satelle workspace add` would.
type repairEnv struct {
	*progressEnv
	refuse  atomic.Bool
	reseeds atomic.Int32
	ingest  *httptest.Server
	root    string
}

func newRepairEnv(t *testing.T) *repairEnv {
	t.Helper()
	e := &repairEnv{progressEnv: newProgressEnv(t)}
	e.ingest = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.refuse.Load() && r.URL.Path == "/ingest/snapshot" {
			http.Error(w, "refused", http.StatusServiceUnavailable)
			return
		}
		e.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(e.ingest.Close)
	t.Setenv(EnvServerEndpoint, e.ingest.URL)
	e.root = config.CanonicalRepoRoot(e.a.RepoRoot)
	return e
}

// start runs the reconciler with a pass interval far beyond the test, so only
// the retry tick can repair, and waits for its startup pass.
func (e *repairEnv) start(t *testing.T) {
	t.Helper()
	rec := &serve.Reconciler{
		Interval:  time.Hour,
		RetryTick: repairRetryTick,
		DirtyHome: config.GlobalDir(),
		Targets:   func(context.Context) []string { return []string{e.root} },
		Bound:     func(string) bool { return false },
		Reseed: func(ctx context.Context, _ string) error {
			e.reseeds.Add(1)
			snap, err := buildUISnapshot(ctx, e.a)
			if err != nil {
				return err
			}
			return postUISnapshotContext(ctx, e.ingest.URL, snap)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rec.Loop(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(repairBudget)
	for e.reseeds.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("reconciler startup pass never ran")
		}
		time.Sleep(5 * time.Millisecond) // time-subject: waiting on the reconciler goroutine's startup pass
	}
}

// refused runs push with the mirror refusing snapshots, then asserts it marked
// the repo dirty and lets the mirror accept again.
func (e *repairEnv) refused(t *testing.T, push func()) {
	t.Helper()
	e.refuse.Store(true)
	push()
	e.refuse.Store(false)
	if !slices.Contains(mirror.DirtyRepos(config.GlobalDir()), e.root) {
		t.Fatalf("a refused push must mark %s dirty; marks = %v", e.root, mirror.DirtyRepos(config.GlobalDir()))
	}
}

// cell is the PROGRESS cell of storyID's row as the page renders it now.
func (e *repairEnv) cell(t *testing.T, storyID string) string {
	t.Helper()
	resp, err := http.Get(e.srv.URL + "/r/" + partitionSlug(e.a) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	_, row, ok := strings.Cut(string(page), `data-id="`+storyID+`"`)
	if !ok {
		return ""
	}
	_, cell, _ := strings.Cut(row, `class="col-reviews">`)
	cell, _, _ = strings.Cut(cell, "</td>")
	return cell
}

// awaitCell waits until storyID's PROGRESS cell satisfies ok, within repairBudget.
func (e *repairEnv) awaitCell(t *testing.T, storyID, what string, ok func(cell string) bool) {
	t.Helper()
	deadline := time.Now().Add(repairBudget)
	for {
		c := e.cell(t, storyID)
		if ok(c) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("page never showed %s for %s within %s; cell = %q", what, storyID, repairBudget, c)
		}
		time.Sleep(repairRetryTick) // time-subject: polling the page until the retry tick's reseed lands
	}
	if left := mirror.DirtyRepos(config.GlobalDir()); len(left) != 0 {
		t.Errorf("dirty marks left after the repair: %v", left)
	}
}

func stageIs(cell, stage, state string) bool {
	return regexp.MustCompile(`stage-` + state + `" title="[^"]*">` + stage + `\b`).MatchString(cell)
}

// TestRefusedStatusDrainReachesPageOnRetryTick: a `story set` drain the mirror
// refuses marks the repo dirty, and satelled's retry tick reseeds it, so the
// page shows the new status (plan done, in_progress current) within seconds
// rather than at the next full pass.
func TestRefusedStatusDrainReachesPageOnRetryTick(t *testing.T) {
	e := newRepairEnv(t)
	id := e.story(t, "plan")
	e.append(t, edge(id, workitem.StatusBacklog, "plan")...)
	e.start(t)
	e.awaitCell(t, id, "plan current", func(c string) bool { return stageIs(c, "plan", "current") })

	if _, err := e.db.Stories.SetStatus(e.ctx, id, workitem.StatusInProgress, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	e.append(t, edge(id, "plan", workitem.StatusInProgress)...)
	e.refused(t, func() {
		d := &uiDrain{endpoint: e.ingest.URL, repoKey: config.RepoKey(e.a.RepoRoot), app: e.a}
		d.mark("stories")
		d.flush()
	})
	if stageIs(e.cell(t, id), workitem.StatusInProgress, "current") {
		t.Fatal("fixture: the refused drain reached the page")
	}

	e.awaitCell(t, id, "in_progress current and plan done", func(c string) bool {
		return stageIs(c, workitem.StatusInProgress, "current") && stageIs(c, "plan", "done")
	})
}

// TestRefusedSeatPushReachesPageOnRetryTick: a dispatch-start seat push the
// mirror refuses still reaches the page as the performing chip (story at plan,
// dispatch for in_progress), and a refused dispatch-end push still takes it off.
func TestRefusedSeatPushReachesPageOnRetryTick(t *testing.T) {
	e := newRepairEnv(t)
	id := e.story(t, "plan")
	e.append(t, edge(id, workitem.StatusBacklog, "plan")...)
	e.append(t, progress(id, "plan", workitem.StatusInProgress, ledger.KindReviewAccept)...)
	e.start(t)
	e.awaitCell(t, id, "plan current", func(c string) bool { return stageIs(c, "plan", "current") })

	if _, _, _, err := e.db.Leases.Acquire(e.ctx, id, "story", "test@local", "plan", true); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Leases.SetActivity(e.ctx, id, lease.Activity{Label: "dispatch:" + workitem.StatusInProgress, Index: 1, Total: 1}); err != nil {
		t.Fatal(err)
	}
	e.refused(t, func() {
		activityDetailSink(e.db.Leases, e.a, e.ingest.URL, id, agentstep.ActivityDetail{
			Agent: "coder", Model: "sonnet", Pid: 4242, EventLabel: "tool: Bash", EventAt: time.Now().UTC(), EventCount: 1,
		})
	})
	if strings.Contains(e.cell(t, id), "stage-performing") {
		t.Fatal("fixture: the refused seat push reached the page")
	}
	e.awaitCell(t, id, "in_progress performing and plan done", func(c string) bool {
		return stageIs(c, workitem.StatusInProgress, "performing") && stageIs(c, "plan", "done")
	})

	if err := e.db.Leases.ClearInFlight(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	e.refused(t, func() { pushSeatSnapshot(e.a, e.ingest.URL) })
	if !strings.Contains(e.cell(t, id), "stage-performing") {
		t.Fatal("fixture: the refused dispatch-end push reached the page")
	}
	e.awaitCell(t, id, "no performing chip", func(c string) bool {
		return c != "" && !strings.Contains(c, "stage-performing")
	})
}
