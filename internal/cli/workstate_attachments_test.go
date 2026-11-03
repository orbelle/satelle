package cli

// Story attachments travel with the story (sty_4d42e13e): push uploads them to
// the personal documents collection, pull/snapshot write them back on another
// machine. Only the hosted server is faked; the CLI, stores and attachment dirs
// are real.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
)

const (
	storiesOnlyToml  = "[sync]\n\n[hosted]\nproject = \"probe\"\n"
	attachmentPrefix = "/api/v1/projects/probe/documents/"
)

// fakeDocs is the hosted documents collection: path-keyed text, a changed-since
// list with cursors "c<N>", and injectable PUT failures.
type fakeDocs struct {
	mu      sync.Mutex
	files   map[string][]byte
	ver     map[string]int
	seq     int
	puts    []string // URL paths of PUTs, in order
	reqs    []string // URL paths of every documents request
	failPut map[string]int
	root    string // the documents route; the project's unless set (a team workspace's)
	refuse  int    // when non-zero, every PUT is answered with this status
}

func newFakeDocs() *fakeDocs {
	return &fakeDocs{files: map[string][]byte{}, ver: map[string]int{}, failPut: map[string]int{}}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (d *fakeDocs) routes(mux *http.ServeMux, _ *fakeWorkstateServer) {
	root := d.root
	if root == "" {
		root = "/api/v1/projects/{project}/documents"
	}
	mux.HandleFunc("PUT "+root+"/{path...}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.reqs = append(d.reqs, r.URL.Path)
		path := r.PathValue("path")
		if d.refuse != 0 {
			http.Error(w, "refused", d.refuse)
			return
		}
		if d.failPut[path] > 0 {
			d.failPut[path]--
			http.Error(w, "injected", http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_, existed := d.files[path]
		d.files[path] = body
		d.seq++
		d.ver[path] = d.seq
		d.puts = append(d.puts, r.URL.Path)
		if existed {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"path": path, "version": d.seq, "blob_sha256": sha256Hex(body)})
	})
	mux.HandleFunc("GET "+root+"/{path...}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.reqs = append(d.reqs, r.URL.Path)
		body, ok := d.files[r.PathValue("path")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", sha256Hex(body))
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET "+root, func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.reqs = append(d.reqs, r.URL.Path)
		var since int
		if c := r.URL.Query().Get("since"); c != "" {
			_, _ = fmt.Sscanf(c, "c%d", &since)
		}
		items := []hosted.ConfigItem{}
		for _, p := range slices.Sorted(maps.Keys(d.files)) {
			if d.ver[p] > since {
				items = append(items, hosted.ConfigItem{Path: p, Version: d.ver[p], BlobSHA256: sha256Hex(d.files[p]), Size: int64(len(d.files[p]))})
			}
		}
		_ = json.NewEncoder(w).Encode(hosted.DocumentChanges{Items: items, Cursor: fmt.Sprintf("c%d", d.seq)})
	})
}

func (d *fakeDocs) putCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.puts)
}

func (d *fakeDocs) requestCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.reqs)
}

func (d *fakeDocs) file(path string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.files[path]
	return b, ok
}

// attachmentServer is the fake workstate server plus the documents collection.
func attachmentServer(t *testing.T) (url string, f *fakeWorkstateServer, d *fakeDocs) {
	t.Helper()
	d = newFakeDocs()
	ts, f := newFakeWorkstateServerWith(t, d.routes)
	seedCred(t, ts.URL)
	return ts.URL, f, d
}

// storyDocBody reads an attachment back through `satelle story doc`.
func storyDocBody(t *testing.T, id, name string) (string, error) {
	t.Helper()
	out, err := runRoot(t, "story", "doc", id, name)
	if err != nil {
		return "", err
	}
	var ref struct {
		Body string `json:"body"`
	}
	if derr := json.NewDecoder(strings.NewReader(out)).Decode(&ref); derr != nil {
		t.Fatalf("parse story doc: %v\n%s", derr, out)
	}
	return ref.Body, nil
}

func mustDocBody(t *testing.T, id, name string) string {
	t.Helper()
	body, err := storyDocBody(t, id, name)
	if err != nil {
		t.Fatalf("story doc %s %s: %v", id, name, err)
	}
	return body
}

func attachDoc(t *testing.T, id, name, typ, body string) {
	t.Helper()
	if out, err := runRoot(t, "story", "attach", id, "--name", name, "--type", typ, "--body", body); err != nil {
		t.Fatalf("attach %s: %v\n%s", name, err, out)
	}
}

func runPush(t *testing.T, url string, extra ...string) string {
	t.Helper()
	out, err := runRoot(t, append([]string{"sync", "workstate", "push", "--server", url}, extra...)...)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	return out
}

func syncWorkstateCLI(t *testing.T, url string, verbArgs ...string) string {
	t.Helper()
	args := append([]string{"sync", "workstate", verbArgs[0], "--server", url}, verbArgs[1:]...)
	out, err := runRoot(t, args...)
	if err != nil {
		t.Fatalf("%v: %v\n%s", verbArgs, err, out)
	}
	return out
}

// TestAttachmentExposure (AC4, replaces sty_948ad5df's
// TestWorkstatePushCarriesNoAttachmentBodies): a planted change patch is never
// uploaded under a local scope; under a syncing scope it goes only to the bound
// project's documents route; and it is never inside a workstate Apply.
func TestAttachmentExposure(t *testing.T) {
	const secret = "planted-change-attachment-secret-xyz-4d42e13e"

	t.Run("scope local: nothing uploaded", func(t *testing.T) {
		url, f, docs := attachmentServer(t)
		useScope(t, kindForeign)
		workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")
		id := createStoryID(t, "exposure local")
		attachDoc(t, id, "change-in_progress-done", "change", "diff\n+"+secret+"\n")
		runPush(t, url)
		if n := docs.requestCount(); n != 0 {
			t.Errorf("documents requests under a local scope = %d, want 0", n)
		}
		if f.postCount("probe") != 0 || f.chain.appendCount() != 0 {
			t.Error("a local scope sent work-state to the server")
		}
	})

	t.Run("stories personal: documents route only", func(t *testing.T) {
		url, f, docs := attachmentServer(t)
		workstateRepo(t, ledgerPushToml)
		id := createStoryID(t, "exposure personal")
		attachDoc(t, id, "change-in_progress-done", "change", "diff\n+"+secret+"\n")
		runPush(t, url)
		if docs.putCount() != 1 {
			t.Fatalf("PUTs = %d, want 1", docs.putCount())
		}
		for _, p := range docs.puts {
			if !strings.HasPrefix(p, attachmentPrefix+"stories/"+id+"/") {
				t.Errorf("PUT went to %q, want under %sstories/%s/", p, attachmentPrefix, id)
			}
		}
		got, _ := docs.file("stories/" + id + "/change-in_progress-done.md")
		if !strings.Contains(string(got), secret) {
			t.Error("the patch should reach the documents collection")
		}
		assertNoSecretInApply(t, f, secret)
	})
}

func assertNoSecretInApply(t *testing.T, f *fakeWorkstateServer, secret string) {
	t.Helper()
	// Story events go by Chain/Append, not Apply (sty_9bdf08c4): the body is as
	// absent from them as from the rows.
	if f.chain.carries(secret) {
		t.Error("an attachment body must never appear in a story event")
	}
	appends := f.chain.appendCount()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posts["probe"]) == 0 && appends == 0 {
		t.Fatal("expected at least one workstate Apply or story Append")
	}
	blob, _ := json.Marshal(f.posts)
	if strings.Contains(string(blob), secret) {
		t.Error("an attachment body must never appear in a workstate items/ledger POST")
	}
}

// TestAttachmentPushReportsBinary (AC4): binary attachments stay on the machine
// and are reported as not mirrored.
func TestAttachmentPushReportsBinary(t *testing.T) {
	url, _, docs := attachmentServer(t)
	workstateRepo(t, storiesOnlyToml)
	id := createStoryID(t, "binary report")
	attachDoc(t, id, "notes", "note", "text")
	binPath := filepath.Join(t.TempDir(), "shot.png")
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89}
	if err := os.WriteFile(binPath, png, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runRoot(t, "story", "attach", id, "--name", "shot.png", "--type", "screenshot",
		"--binary-file", binPath, "--content-type", "image/png"); err != nil {
		t.Fatalf("binary attach: %v\n%s", err, out)
	}
	out := runPush(t, url)
	if !strings.Contains(out, "1 binary not mirrored") {
		t.Errorf("push should report the binary attachment: %q", out)
	}
	if docs.putCount() != 1 {
		t.Errorf("PUTs = %d, want only the markdown note", docs.putCount())
	}
	for p := range docs.files {
		if strings.Contains(p, "shot.png") {
			t.Errorf("binary attachment uploaded as %q", p)
		}
	}
}

// TestAttachmentPushRetriesWithNoNewRows (AC5): a failed upload is reported, does
// not fail the push, and is sent by the next push even though that push has no
// new rows (the up-to-date exit).
func TestAttachmentPushRetriesWithNoNewRows(t *testing.T) {
	url, _, docs := attachmentServer(t)
	workstateRepo(t, storiesOnlyToml)
	id := createStoryID(t, "retry")
	attachDoc(t, id, "plan", "plan", "p")
	attachDoc(t, id, "summary", "step-summary", "s")
	docs.failPut["stories/"+id+"/plan.md"] = 1

	out := runPush(t, url)
	if !strings.Contains(out, "attachment upload failed stories/"+id+"/plan.md") {
		t.Errorf("a failed upload should be reported: %q", out)
	}
	if !strings.Contains(out, "1 uploaded") || !strings.Contains(out, "1 failed") {
		t.Errorf("summary should count one upload and one failure: %q", out)
	}
	if _, ok := docs.file("stories/" + id + "/plan.md"); ok {
		t.Fatal("the failed file must not be hosted yet")
	}

	out = runPush(t, url)
	if !strings.Contains(out, "up to date") {
		t.Fatalf("second push should have no new rows: %q", out)
	}
	if _, ok := docs.file("stories/" + id + "/plan.md"); !ok {
		t.Error("the retry on a push with no new rows should upload the failed file")
	}
	before := docs.putCount()
	runPush(t, url)
	if docs.putCount() != before {
		t.Errorf("a settled push PUTs = %d more, want none", docs.putCount()-before)
	}
}

// TestAttachmentBackfillAfterUpgrade (AC7): attachments behind the work-state
// cursor with no record of being uploaded go up on the next push, with no new
// rows and no --full.
func TestAttachmentBackfillAfterUpgrade(t *testing.T) {
	url, _, docs := attachmentServer(t)
	workstateRepo(t, storiesOnlyToml)
	id := createStoryID(t, "backfill")
	attachDoc(t, id, "change-in_progress-done", "change", "patch")
	attachDoc(t, id, "plan", "plan", "plan")
	runPush(t, url)
	if docs.putCount() != 2 {
		t.Fatalf("first push PUTs = %d, want 2", docs.putCount())
	}

	// The pre-upgrade state: the cursor has advanced, nothing records an upload.
	statePath := hosted.DocumentSyncStatePathOverride
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if _, ok := state["attachments_uploaded"]; !ok {
		t.Fatal("expected an uploaded-state record after the first push")
	}
	delete(state, "attachments_uploaded")
	raw, _ = json.Marshal(state)
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	out := runPush(t, url)
	if !strings.Contains(out, "up to date") {
		t.Fatalf("the backfill push should have no new rows: %q", out)
	}
	if docs.putCount() != 4 {
		t.Errorf("backfill PUTs = %d, want both attachments again (4 total)", docs.putCount())
	}
	runPush(t, url)
	if docs.putCount() != 4 {
		t.Errorf("a push after the backfill PUTs = %d total, want 4", docs.putCount())
	}
}

// TestAttachmentLegRunsWhenTheRowPushFails (AC5): a push that fails — here the
// Apply answers 500 — still sends the attachments, and the push still reports its
// own error; the leg neither masks nor adds to it.
func TestAttachmentLegRunsWhenTheRowPushFails(t *testing.T) {
	url, f, docs := attachmentServer(t)
	workstateRepo(t, storiesOnlyToml)
	id := createStoryID(t, "apply fails")
	attachDoc(t, id, "plan", "plan", "survives a failed apply")
	f.mu.Lock()
	f.failOnPost = 1
	f.mu.Unlock()

	out, err := runRoot(t, "sync", "workstate", "push", "--server", url)
	if err == nil || !strings.Contains(err.Error(), "apply workstate") {
		t.Fatalf("the push should fail with its own apply error, got %v\n%s", err, out)
	}
	got, ok := docs.file("stories/" + id + "/plan.md")
	if !ok {
		t.Fatal("the attachment should be uploaded even though the row push failed")
	}
	if want := mustDocBody(t, id, "plan"); string(got) != want {
		t.Errorf("uploaded bytes differ: got %q want %q", got, want)
	}
}

// cliMachine is one checkout's identity for the CLI: its own home (so its own
// runtime and attachment dirs) and its own repo and config.
type cliMachine struct{ home, cfg string }

func newCLIMachine(t *testing.T, toml string) cliMachine {
	t.Helper()
	workstateRepo(t, toml)
	return cliMachine{home: os.Getenv("SATELLE_HOME"), cfg: os.Getenv("SATELLE_CONFIG")}
}

func (m cliMachine) use(t *testing.T) {
	t.Helper()
	t.Setenv("SATELLE_HOME", m.home)
	t.Setenv("SATELLE_CONFIG", m.cfg)
}

// TestAttachmentPullNeverOverwritesSilently (AC3): a differing local attachment
// is kept and named without --force and replaced with it; a local attachment
// hosted lacks is always kept.
func TestAttachmentPullNeverOverwritesSilently(t *testing.T) {
	url, _, _ := attachmentServer(t)
	a := newCLIMachine(t, storiesOnlyToml)
	b := newCLIMachine(t, storiesOnlyToml)

	a.use(t)
	id := createStoryID(t, "conflict")
	runPush(t, url)
	b.use(t)
	syncWorkstateCLI(t, url, "pull") // the story arrives, with no attachments yet
	attachDoc(t, id, "plan", "plan", "machine B plan")
	attachDoc(t, id, "only-here", "note", "B only")
	wantB := mustDocBody(t, id, "plan")

	a.use(t)
	attachDoc(t, id, "plan", "plan", "machine A plan")
	wantA := mustDocBody(t, id, "plan")
	runPush(t, url)

	b.use(t)
	out := syncWorkstateCLI(t, url, "pull")
	if !strings.Contains(out, "kept local stories/"+id+"/plan.md") || !strings.Contains(out, "1 kept") {
		t.Errorf("a differing local file should be named as kept: %q", out)
	}
	if got := mustDocBody(t, id, "plan"); got != wantB {
		t.Errorf("without --force the local plan changed to %q, want %q", got, wantB)
	}

	out = syncWorkstateCLI(t, url, "pull", "--force")
	if got := mustDocBody(t, id, "plan"); got != wantA {
		t.Errorf("with --force hosted should win: plan = %q, want %q (%s)", got, wantA, out)
	}
	if got := mustDocBody(t, id, "only-here"); !strings.Contains(got, "B only") {
		t.Errorf("a local-only attachment must be kept, got %q", got)
	}
}
