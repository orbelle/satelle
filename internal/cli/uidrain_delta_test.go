package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// snapshotPost is one POST /ingest/snapshot the counting ingest saw.
type snapshotPost struct {
	bytes  int
	status int
	full   bool
	snap   mirror.Snapshot
}

// countingIngest serves the real mirror ingest handler and records every
// snapshot body, and the order of change and snapshot POSTs. refuse makes the
// next snapshot POST answer 503.
type countingIngest struct {
	mu     sync.Mutex
	posts  []snapshotPost
	paths  []string
	refuse bool
}

func (c *countingIngest) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.paths = append(c.paths, r.URL.Path)
		refuse := c.refuse && r.URL.Path == "/ingest/snapshot"
		c.mu.Unlock()
		if r.URL.Path != "/ingest/snapshot" {
			next.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var snap mirror.Snapshot
		_ = json.Unmarshal(body, &snap)
		p := snapshotPost{bytes: len(body), snap: snap, full: len(snap.Kinds) == 0 && len(snap.MergeKinds) == 0}
		if refuse {
			p.status = http.StatusServiceUnavailable
			http.Error(w, "refused", p.status)
		} else {
			rec := httptest.NewRecorder()
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(rec, r)
			p.status = rec.Code
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
		}
		c.mu.Lock()
		c.posts = append(c.posts, p)
		c.mu.Unlock()
	})
}

// take returns and forgets what was recorded since the last take.
func (c *countingIngest) take() ([]snapshotPost, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	posts, paths := c.posts, c.paths
	c.posts, c.paths = nil, nil
	return posts, paths
}

// deltaStoryBody is a realistic story body, so the full snapshot's size is
// what a 500-story repo sends.
var deltaStoryBody = strings.Repeat("As an operator running several repos I need the board current without satelled competing with my work. ", 3)

// sty_a003a459 AC1: ten story edits on a 500-story repo satelled has never
// seeded post exactly one full snapshot — the first edit, after satelled
// answers its delta partition_unknown — and every later drain posts a merge
// body carrying only the edited row.
func TestDrainPostsOnlyTheChangedRows(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	repo := t.TempDir()
	_ = os.WriteFile(filepath.Join(repo, ".git"), []byte(""), 0o644)
	db, err := store.Open(filepath.Join(t.TempDir(), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ms, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	mux := http.NewServeMux()
	(&mirror.IngestHandler{Store: ms}).Mount(mux)
	counter := &countingIngest{}
	srv := httptest.NewServer(counter.wrap(mux))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	a := &app.App{Config: config.Config{}, RepoRoot: repo, Store: db}
	repoKey := config.RepoKey(repo)
	created := time.Now().UTC().Add(-time.Hour)
	ids := make([]string, 500)
	err = db.InTx(ctx, func(tx *sql.Tx) error {
		st := db.Stories.WithTx(tx)
		for i := range ids {
			it, err := st.Create(ctx, workitem.CreateInput{
				Kind: workitem.KindStory, Title: fmt.Sprintf("Story %03d", i), Body: deltaStoryBody,
				AcceptanceCriteria: "1. the board shows it", Category: "fix", Status: workitem.StatusBacklog,
				Tags: []string{"serve", "performance"},
			}, created)
			if err != nil {
				return err
			}
			ids[i] = it.ID
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Each edit is its own invocation: since is its start, and invocations are
	// spaced beyond deltaSlack so an earlier edit is not the later one's change.
	edit := func(i int) time.Time {
		since := created.Add(30*time.Minute + time.Duration(i)*10*deltaSlack)
		title := fmt.Sprintf("Story %03d edited", i)
		if _, err := db.Stories.Update(ctx, ids[i], workitem.UpdateInput{Title: &title}, since.Add(time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		d := &uiDrain{endpoint: srv.URL, repoKey: repoKey, app: a, since: since}
		d.mark("stories")
		d.flush()
		return since
	}

	var fulls, fullBytes, laterBytes int
	for i := 0; i < 10; i++ {
		edit(i)
		posts, paths := counter.take()
		if len(paths) == 0 || paths[0] != "/ingest/change" {
			t.Fatalf("edit %d: the drain must post its topics before the snapshot, got %v", i+1, paths)
		}
		for _, p := range posts {
			if p.full {
				fulls++
				fullBytes = p.bytes
			}
		}
		if i == 0 {
			if len(posts) != 2 || posts[0].full || posts[0].status != http.StatusConflict || !posts[1].full || posts[1].status != http.StatusOK {
				t.Fatalf("first edit: want a refused delta then one full body, got %+v", summarise(posts))
			}
			if n := len(posts[1].snap.Stories); n != 500 {
				t.Fatalf("first edit's full body carries %d stories, want 500", n)
			}
			continue
		}
		if len(posts) != 1 || posts[0].full || posts[0].status != http.StatusOK {
			t.Fatalf("edit %d: want one accepted merge body, got %+v", i+1, summarise(posts))
		}
		p := posts[0]
		laterBytes += p.bytes
		if !slices.Contains(p.snap.MergeKinds, "story") || slices.Contains(p.snap.Kinds, "story") {
			t.Fatalf("edit %d: stories must be a merge kind, kinds=%v merge=%v", i+1, p.snap.Kinds, p.snap.MergeKinds)
		}
		if len(p.snap.Stories) != 1 || len(p.snap.Tasks) != 0 || len(p.snap.Executions) != 0 {
			t.Fatalf("edit %d: body carries %d stories, %d tasks, %d executions; want only the edited story",
				i+1, len(p.snap.Stories), len(p.snap.Tasks), len(p.snap.Executions))
		}
		var row workitem.Item
		if err := json.Unmarshal(p.snap.Stories[0], &row); err != nil || row.ID != ids[i] {
			t.Fatalf("edit %d: body's row is %q, want the edited %s", i+1, row.ID, ids[i])
		}
	}
	if fulls != 1 {
		t.Fatalf("ten edits posted %d full snapshots, want exactly 1", fulls)
	}
	if laterBytes*20 >= fullBytes {
		t.Fatalf("edits 2-10 posted %d bytes, not under 5%% of the %d-byte full body", laterBytes, fullBytes)
	}
	t.Logf("full body %d bytes; edits 2-10 posted %d bytes in total", fullBytes, laterBytes)

	if n, _ := ms.CountItems(ctx, repoKey, "story"); n != 500 {
		t.Fatalf("mirror holds %d stories, want all 500", n)
	}
	for i := 0; i < 10; i++ {
		payload, err := ms.GetItem(ctx, repoKey, "story", ids[i])
		if err != nil || !strings.Contains(payload, fmt.Sprintf("Story %03d edited", i)) {
			t.Fatalf("mirror row %s lacks its edit (err %v)", ids[i], err)
		}
	}
	if marks := mirror.DirtyRepos(config.GlobalDir()); len(marks) != 0 {
		t.Fatalf("accepted drains left dirty marks: %v", marks)
	}

	// A delta that does not land marks the repo dirty for satelled's retry.
	counter.mu.Lock()
	counter.refuse = true
	counter.mu.Unlock()
	edit(10)
	if !slices.Contains(mirror.DirtyRepos(config.GlobalDir()), config.CanonicalRepoRoot(repo)) {
		t.Fatalf("a refused delta must mark the repo dirty; marks = %v", mirror.DirtyRepos(config.GlobalDir()))
	}
}

func summarise(posts []snapshotPost) []string {
	out := make([]string, 0, len(posts))
	for _, p := range posts {
		out = append(out, fmt.Sprintf("{status %d full %v stories %d bytes %d}", p.status, p.full, len(p.snap.Stories), p.bytes))
	}
	return out
}
