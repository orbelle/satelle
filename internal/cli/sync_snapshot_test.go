package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/snapsync"
)

// Two-machine tests for snapshot-based substrate sync (sty_fe5a8ed4). Two repos,
// each with its OWN sync-state file, talk to one fake hosted server that
// implements the real contract: per-path sequential versions, pinned reads on the
// config route, head-only reads on the documents route, and no delete anywhere.

// snapFake is the shared hosted server plus a log of every PUT it received.
type snapFake struct {
	mu          sync.Mutex
	cfg         *fakeConfigStore
	docs        *fakeDocStore
	puts        []string
	onRecordPut func() // runs once, just before the next snapshot-record PUT is stored
	// unreadableRecords makes every pinned read of a snapshot record 404, as a
	// server without pinned reads does: records can be put but not read back.
	unreadableRecords bool
	// refuse counts, per config path, the PUTs still to be refused as a server
	// refuses a file it will not store (sty_195dd215).
	refuse map[string]int
	// down makes every request fail, as an unreachable server does.
	down bool
}

// refuseNext makes the next n config PUTs of path fail with HTTP 400.
func (f *snapFake) refuseNext(path string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse == nil {
		f.refuse = map[string]int{}
	}
	f.refuse[path] = n
}

func (f *snapFake) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *snapFake) putLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.puts...)
}

func newSnapServer(t *testing.T) (*httptest.Server, *snapFake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hosted.DocumentSyncStatePathOverride = ""
	t.Cleanup(func() { hosted.DocumentSyncStatePathOverride = "" })
	f := &snapFake{cfg: &fakeConfigStore{data: map[string]map[string][][]byte{}}, docs: newFakeDocStore()}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		down := f.down
		f.mu.Unlock()
		if down {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		segs := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/v1/projects/"), "/", 3)
		if len(segs) < 2 {
			http.NotFound(w, r)
			return
		}
		project, kind, path := segs[0], segs[1], ""
		if len(segs) == 3 {
			path = segs[2]
		}
		put := func(store interface {
			put(string, string, []byte) (string, int, bool)
		}) {
			if _, isRecord := snapsync.AreaOfRecordPath(path); isRecord && kind == "config" {
				f.mu.Lock()
				hook := f.onRecordPut
				f.onRecordPut = nil
				f.mu.Unlock()
				if hook != nil {
					hook()
				}
			}
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			refused := kind == "config" && f.refuse[path] > 0
			if refused {
				f.refuse[path]--
			} else {
				f.puts = append(f.puts, kind+":"+path)
			}
			f.mu.Unlock()
			if refused {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "could not store file (too large?)"})
				return
			}
			sha, ver, created := store.put(project, path, body)
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"path": path, "version": ver, "blob_sha256": sha, "size": len(body), "created": created})
		}
		switch kind {
		case "config":
			switch {
			case r.Method == http.MethodPut:
				put(f.cfg)
			case r.Method == http.MethodGet && path == "":
				_ = json.NewEncoder(w).Encode(f.cfg.manifest(project))
			case r.Method == http.MethodGet:
				ver := 0
				if v := r.URL.Query().Get("version"); v != "" {
					ver = atoiOr0(v)
				}
				if _, isRecord := snapsync.AreaOfRecordPath(path); isRecord && ver > 0 {
					f.mu.Lock()
					hidden := f.unreadableRecords
					f.mu.Unlock()
					if hidden {
						http.NotFound(w, r)
						return
					}
				}
				content, sha, ok := f.cfg.get(project, path, ver)
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("ETag", `"`+sha+`"`)
				_, _ = w.Write(content)
			default:
				http.NotFound(w, r)
			}
		case "documents":
			switch {
			case r.Method == http.MethodPut:
				put(f.docs)
			case r.Method == http.MethodGet && path == "":
				items, cursor := f.docs.changes(project, r.URL.Query().Get("since"))
				_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "cursor": cursor})
			case r.Method == http.MethodGet:
				content, sha, ok := f.docs.get(project, path)
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("ETag", `"`+sha+`"`)
				_, _ = w.Write(content)
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, f
}

func atoiOr0(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// snapMachine is one checkout with its own sync-state file.
type snapMachine struct {
	t           *testing.T
	repo, state string
	url         string
}

const snapToml = "[sync]\n" + boundProjectToml

func newSnapMachine(t *testing.T, url string) *snapMachine {
	t.Helper()
	return &snapMachine{t: t, url: url, repo: syncConfigRepo(t, snapToml), state: filepath.Join(t.TempDir(), "document-sync-state.json")}
}

// use makes this machine the one the process acts as.
func (m *snapMachine) use() {
	m.t.Helper()
	pointAt(m.t, m.repo)
	hosted.DocumentSyncStatePathOverride = m.state
}

func (m *snapMachine) path(rel string) string {
	return filepath.Join(m.repo, ".satelle", filepath.FromSlash(rel))
}

func (m *snapMachine) write(rel, body string) {
	m.t.Helper()
	writeRepoFile(m.t, m.repo, ".satelle/"+rel, body)
}

func (m *snapMachine) remove(rel string) {
	m.t.Helper()
	if err := os.Remove(m.path(rel)); err != nil {
		m.t.Fatal(err)
	}
}

func (m *snapMachine) read(rel string) string {
	b, err := os.ReadFile(m.path(rel))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func (m *snapMachine) has(rel string) bool {
	_, err := os.Stat(m.path(rel))
	return err == nil
}

// The try* methods return the error so a test can assert a refusal; the plain
// methods fail the test on any error and return the command's output.
func (m *snapMachine) tryPushConfig() (string, error) {
	m.use()
	cmd, buf := testCmd()
	err := runSyncConfigPush(cmd, m.url, "", false)
	return buf.String(), err
}

func (m *snapMachine) tryDeploy() (string, error) {
	m.use()
	cmd, buf := testCmd()
	err := runSyncConfigDeploy(cmd, m.url, "personal", 0)
	return buf.String(), err
}

func (m *snapMachine) tryPushDocs() (string, error) {
	m.use()
	cmd, buf := testCmd()
	err := runSyncDocumentsPush(cmd, m.url, "", false)
	return buf.String(), err
}

func (m *snapMachine) tryPullDocs() (string, error) {
	m.use()
	cmd, buf := testCmd()
	err := runSyncDocumentsPull(cmd, m.url, "")
	return buf.String(), err
}

func (m *snapMachine) ok(out string, err error) string {
	m.t.Helper()
	if err != nil {
		m.t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	return out
}

func (m *snapMachine) pushConfig() string { m.t.Helper(); return m.ok(m.tryPushConfig()) }
func (m *snapMachine) deploy() string     { m.t.Helper(); return m.ok(m.tryDeploy()) }
func (m *snapMachine) pushDocs() string   { m.t.Helper(); return m.ok(m.tryPushDocs()) }
func (m *snapMachine) pullDocs() string   { m.t.Helper(); return m.ok(m.tryPullDocs()) }

// listTree is every file under root, relative, sorted.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func (m *snapMachine) base(area string) (hosted.AreaBase, bool) {
	m.t.Helper()
	m.use()
	b, ok, err := hosted.LoadAreaBase(m.url, "probe", m.repo, area)
	if err != nil {
		m.t.Fatal(err)
	}
	return b, ok
}

// AC1: a file deleted or renamed on A is gone on B after push and pull, and the
// abandoned hosted head is never written into any tree — not even a fresh one.
func TestSnapshotDeleteAndRenamePropagateConfig(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a, b := newSnapMachine(t, ts.URL), newSnapMachine(t, ts.URL)

	a.write("workflows/done.md", "markdown route\n")
	a.write("workflows/done.toml", "[route]\n")
	a.write("skills/old.md", "same bytes\n")
	a.pushConfig()
	b.deploy()
	for _, rel := range []string{"workflows/done.md", "workflows/done.toml", "skills/old.md"} {
		if !b.has(rel) {
			t.Fatalf("B did not receive %s on the first deploy", rel)
		}
	}

	a.remove("workflows/done.md")
	a.remove("skills/old.md")
	a.write("skills/new.md", "same bytes\n") // a rename
	a.pushConfig()

	out := b.deploy()
	if b.has("workflows/done.md") {
		t.Errorf("B still has the file A deleted\n%s", out)
	}
	if b.has("skills/old.md") || b.read("skills/new.md") != "same bytes\n" {
		t.Errorf("B did not follow the rename: old=%v new=%q\n%s", b.has("skills/old.md"), b.read("skills/new.md"), out)
	}
	if !b.has("workflows/done.toml") {
		t.Error("an untouched file disappeared")
	}
	if !strings.Contains(out, "removed (deleted on hosted copy): workflows/done.md") {
		t.Errorf("the deletion is not reported:\n%s", out)
	}
	// The hosted store never deletes a head — the snapshot is what hides it.
	if _, _, ok := f.cfg.get("probe", "workflows/done.md", 0); !ok {
		t.Fatal("test premise: the hosted copy must still hold the abandoned head")
	}

	// A machine that has never synced must not get the abandoned head either.
	c := newSnapMachine(t, ts.URL)
	c.deploy()
	if c.has("workflows/done.md") || c.has("skills/old.md") {
		t.Errorf("a fresh deploy materialised an abandoned head: %v", listTree(t, c.path("")))
	}
	if !c.has("workflows/done.toml") || !c.has("skills/new.md") {
		t.Errorf("a fresh deploy missed live files: %v", listTree(t, c.path("")))
	}
}

func TestSnapshotDeletePropagatesDocuments(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a, b := newSnapMachine(t, ts.URL), newSnapMachine(t, ts.URL)

	a.write("documents/x.md", "x\n")
	a.write("documents/y.md", "y\n")
	a.pushDocs()
	b.pullDocs()
	if !b.has("documents/x.md") || !b.has("documents/y.md") {
		t.Fatal("B did not receive the documents")
	}

	a.remove("documents/x.md")
	a.pushDocs()
	out := b.pullDocs()
	if b.has("documents/x.md") {
		t.Errorf("B still has the document A deleted\n%s", out)
	}
	if b.read("documents/y.md") != "y\n" {
		t.Errorf("B lost an untouched document:\n%s", out)
	}
	if _, _, ok := f.docs.get("probe", "documents/x.md"); !ok {
		t.Fatal("test premise: the hosted copy must still hold the deleted document's head")
	}
}

// AC2: the base is recorded outside the tree, printed by push, pull and
// `sync scopes`, and nothing new appears in either repo tree.
func TestSnapshotBaseRecordedAndPrinted(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a, b := newSnapMachine(t, ts.URL), newSnapMachine(t, ts.URL) // sets SATELLE_HOME
	if err := config.SaveGlobalHostedServer(ts.URL); err != nil {
		t.Fatal(err)
	}

	a.write("skills/x.md", "body\n")
	before := listTree(t, a.repo)
	out := a.pushConfig()
	if !strings.Contains(out, "skills: snapshot 1 (parent 0)") {
		t.Errorf("push output lacks the snapshot line:\n%s", out)
	}
	if after := listTree(t, a.repo); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("a push changed the repo tree:\nbefore %v\nafter  %v", before, after)
	}

	raw, err := os.ReadFile(a.state)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	var st struct {
		Snapshots map[string]struct {
			Version  int               `json:"version"`
			Files    map[string]string `json:"files"`
			Unmerged map[string]string `json:"unmerged"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	found := false
	for k, v := range st.Snapshots {
		if strings.HasSuffix(k, "|skills") {
			found = true
			if v.Version != 1 || v.Files["skills/x.md"] != sha256hex([]byte("body\n")) {
				t.Errorf("recorded base = %+v", v)
			}
		}
	}
	if !found {
		t.Fatalf("no skills base in the state file: %s", raw)
	}

	beforeB := listTree(t, b.repo)
	out = b.deploy()
	if !strings.Contains(out, "skills: synced to snapshot 1") {
		t.Errorf("pull output lacks the synced line:\n%s", out)
	}
	for _, p := range listTree(t, b.repo) {
		if strings.Contains(p, "snapshot") || strings.Contains(p, "backups") {
			t.Errorf("a snapshot artefact appeared in B's tree: %s", p)
		}
	}
	// The deployed skill, and the agents layer a personal scope also deploys.
	gained := []string{}
	for _, p := range listTree(t, b.repo) {
		if !slices.Contains(beforeB, p) {
			gained = append(gained, p)
		}
	}
	if want := []string{".satelle/skills/x.md", ".satelle/workflows/agents.toml"}; !slices.Equal(gained, want) {
		t.Errorf("B's tree should gain exactly the deployed files %v, gained %v", want, gained)
	}
	if got, ok := b.base("skills"); !ok || got.Version != 1 {
		t.Errorf("B's base = %+v ok=%v", got, ok)
	}

	// `satelle sync scopes` prints it.
	a.use()
	scopes, err := runRoot(t, "sync", "scopes")
	if err != nil {
		t.Fatalf("sync scopes: %v\n%s", err, scopes)
	}
	var skillsLine string
	for _, line := range strings.Split(scopes, "\n") {
		if strings.HasPrefix(line, "skills") {
			skillsLine = line
		}
	}
	if !strings.Contains(skillsLine, "last synced snapshot 1") {
		t.Errorf("sync scopes lacks the last-synced column for skills:\n%s", scopes)
	}
	// An area this machine never synced prints exactly as before.
	for _, line := range strings.Split(scopes, "\n") {
		if strings.HasPrefix(line, "principles") && strings.Contains(line, "last synced") {
			t.Errorf("an unsynced area claims a snapshot: %q", line)
		}
	}
}

// AC3 (the plain case): B has not pulled A's newer push, so B's push is refused
// before it uploads anything.
func TestSnapshotPushRefusedWhenBehind(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a, b := newSnapMachine(t, ts.URL), newSnapMachine(t, ts.URL)

	a.write("skills/a.md", "a0\n")
	a.write("skills/b.md", "b0\n")
	a.pushConfig()
	b.deploy()

	a.write("skills/a.md", "a1\n")
	a.pushConfig() // snapshot 2
	b.write("skills/b.md", "b1\n")
	before := len(f.putLog())
	out, err := b.tryPushConfig()
	if err == nil {
		t.Fatalf("B pushed over a newer snapshot it never pulled:\n%s", out)
	}
	for _, want := range []string{"snapshot 2", "snapshot 1", `satelle sync reconcile`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "sync rehydrate") {
		t.Errorf("refusal names rehydrate, which overwrites the local tree: %q", err.Error())
	}
	if n := len(f.putLog()) - before; n != 0 {
		t.Errorf("a refused push made %d PUT(s): %v", n, f.putLog()[before:])
	}
	if got, _, _ := f.cfg.get("probe", "skills/b.md", 0); string(got) != "b0\n" {
		t.Errorf("B's blob reached the hosted copy: %q", got)
	}

	// Pull, then the push goes through.
	b.deploy()
	if b.read("skills/a.md") != "a1\n" || b.read("skills/b.md") != "b1\n" {
		t.Fatalf("pull lost a side: a=%q b=%q", b.read("skills/a.md"), b.read("skills/b.md"))
	}
	out = b.pushConfig()
	if !strings.Contains(out, "skills: snapshot 3 (parent 2)") {
		t.Errorf("push after pull:\n%s", out)
	}
}

// AC3 (the race): both machines claim from the same base. The lowest version is
// effective; the loser is refused before uploading a blob, its record — the raw
// head — is ignored everywhere, and its deletions never apply.
func TestSnapshotLostRaceIsIgnoredEverywhere(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a, b := newSnapMachine(t, ts.URL), newSnapMachine(t, ts.URL)

	a.write("skills/a.md", "a0\n")
	a.write("skills/b.md", "b0\n")
	a.write("skills/gone.md", "g\n")
	a.pushConfig() // snapshot 1
	b.deploy()

	b.write("skills/b.md", "b1\n")
	b.remove("skills/gone.md")

	// A's whole push lands between B's check and B's claim.
	a.write("skills/a.md", "a1\n")
	f.onRecordPut = func() {
		if out, err := a.tryPushConfig(); err != nil { // snapshot 2
			t.Errorf("A's push inside the race: %v\n%s", err, out)
		}
		b.use()
	}
	out, err := b.tryPushConfig()
	if err == nil {
		t.Fatalf("the losing claim was accepted:\n%s", out)
	}
	for _, want := range []string{"snapshot 2", "snapshot 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err.Error(), want)
		}
	}
	if got, _, _ := f.cfg.get("probe", "skills/b.md", 0); string(got) != "b0\n" {
		t.Errorf("the loser uploaded a blob: %q", got)
	}
	// The loser's record IS the raw head.
	raw, _, ok := f.cfg.get("probe", snapsyncRecord("skills"), 0)
	if !ok || strings.Contains(string(raw), "gone.md") || !strings.Contains(string(raw), `"parent": 1`) {
		t.Fatalf("test premise: B's losing claim (no gone.md, parent 1) should be the raw head, got %s", raw)
	}

	// The winner can push again — the dead head does not block it.
	a.write("skills/a.md", "a2\n")
	out = a.pushConfig()
	if !strings.Contains(out, "skills: snapshot 4 (parent 2)") {
		t.Errorf("winner's next push:\n%s", out)
	}

	// B's deletion is applied nowhere: a third clone still has gone.md.
	c := newSnapMachine(t, ts.URL)
	c.deploy()
	if c.read("skills/gone.md") != "g\n" || c.read("skills/a.md") != "a2\n" || c.read("skills/b.md") != "b0\n" {
		t.Errorf("a third clone saw the loser's state: gone=%q a=%q b=%q", c.read("skills/gone.md"), c.read("skills/a.md"), c.read("skills/b.md"))
	}

	// B's next pull yields the winner's snapshot, then B's push goes through.
	out = b.deploy()
	if !strings.Contains(out, "skills: synced to snapshot 4") {
		t.Errorf("B's pull did not land on the winner's snapshot:\n%s", out)
	}
	if b.read("skills/a.md") != "a2\n" || b.read("skills/b.md") != "b1\n" || b.has("skills/gone.md") {
		t.Errorf("B after pull: a=%q b=%q gone=%v", b.read("skills/a.md"), b.read("skills/b.md"), b.has("skills/gone.md"))
	}
	out = b.pushConfig()
	if !strings.Contains(out, "skills: snapshot 5 (parent 4)") {
		t.Errorf("B's push after pulling:\n%s", out)
	}
	a.deploy()
	if a.has("skills/gone.md") || a.read("skills/b.md") != "b1\n" {
		t.Errorf("A did not receive B's published state: gone=%v b=%q", a.has("skills/gone.md"), a.read("skills/b.md"))
	}
}

func snapsyncRecord(area string) string { return snapsync.RecordPath(area) }

// AC4: a file changed on both sides is kept locally, the remote bytes are parked
// for merging, the push is refused until it is merged, and the merged bytes are
// then published and reach the other machine.
func TestSnapshotConflictKeepsBothAndMergesThroughPush(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	a, b := newSnapMachine(t, ts.URL), newSnapMachine(t, ts.URL) // sets SATELLE_HOME
	if err := config.SaveGlobalHostedServer(ts.URL); err != nil {
		t.Fatal(err)
	}

	a.write("skills/x.md", "base\n")
	a.write("skills/aonly.md", "a0\n")
	a.write("skills/bonly.md", "b0\n")
	a.pushConfig()
	b.deploy()

	a.write("skills/x.md", "from A\n")
	a.write("skills/aonly.md", "a1\n")
	a.pushConfig() // snapshot 2
	b.write("skills/x.md", "from B\n")
	b.write("skills/bonly.md", "b1\n")

	out := b.deploy()
	if b.read("skills/x.md") != "from B\n" {
		t.Errorf("the local file was overwritten: %q", b.read("skills/x.md"))
	}
	if got := b.read("backups/sync-conflicts/skills/x.md"); got != "from A\n" {
		t.Errorf("conflict copy = %q, want A's bytes", got)
	}
	if !strings.Contains(out, "conflict (changed on both sides): skills/x.md") {
		t.Errorf("the conflict is not named:\n%s", out)
	}
	if b.read("skills/aonly.md") != "a1\n" {
		t.Errorf("a remotely-only change was not written: %q", b.read("skills/aonly.md"))
	}
	if b.read("skills/bonly.md") != "b1\n" {
		t.Errorf("a locally-only change was not kept: %q", b.read("skills/bonly.md"))
	}
	st, ok := b.base("skills")
	if !ok || st.Version != 2 || st.Unmerged["skills/x.md"] != sha256hex([]byte("from A\n")) {
		t.Errorf("base after the conflicted pull = %+v (version must advance, path unmerged)", st)
	}
	scopes, _ := runRoot(t, "sync", "scopes")
	if !strings.Contains(scopes, "unmerged: skills/x.md") {
		t.Errorf("sync scopes does not list the unmerged path:\n%s", scopes)
	}

	// Refused while the conflict copy exists.
	pushOut, err := b.tryPushConfig()
	if err == nil || !strings.Contains(err.Error(), "unmerged skills/x.md") {
		t.Fatalf("push with an unresolved conflict: err=%v\n%s", err, pushOut)
	}

	// Merge, delete the copy, push.
	b.write("skills/x.md", "merged\n")
	b.remove("backups/sync-conflicts/skills/x.md")
	out = b.pushConfig()
	if !strings.Contains(out, "skills: snapshot 3 (parent 2)") {
		t.Errorf("push after merging:\n%s", out)
	}
	a.deploy()
	if a.read("skills/x.md") != "merged\n" || a.read("skills/bonly.md") != "b1\n" {
		t.Errorf("A did not receive the merge: x=%q bonly=%q", a.read("skills/x.md"), a.read("skills/bonly.md"))
	}
}

// AC5: --dry-run uploads nothing, a server with no snapshot deploys every head
// as before, and a first claim carries forward hosted files the tree lacks
// unless asked to prune.
func TestSnapshotDryRunUploadsNothing(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := newSnapMachine(t, ts.URL)
	a.write("skills/x.md", "x\n")
	a.write("documents/d.md", "d\n")
	a.use()

	cmd, buf := testCmd()
	if err := runSyncConfigPush(cmd, ts.URL, "", true); err != nil {
		t.Fatalf("config dry-run: %v\n%s", err, buf.String())
	}
	cmd, buf = testCmd()
	if err := runSyncDocumentsPush(cmd, ts.URL, "", true); err != nil {
		t.Fatalf("documents dry-run: %v\n%s", err, buf.String())
	}
	if puts := f.putLog(); len(puts) != 0 {
		t.Errorf("--dry-run PUT %v", puts)
	}
	if _, ok := a.base("skills"); ok {
		t.Error("--dry-run recorded a base")
	}
}

func TestSnapshotServerWithNoSnapshotDeploysEveryHead(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	f.cfg.put("probe", "skills/legacy.md", []byte("legacy\n"))
	f.cfg.put("probe", "workflows/old-route.md", []byte("old\n"))
	b := newSnapMachine(t, ts.URL)
	out := b.deploy()
	if b.read("skills/legacy.md") != "legacy\n" || b.read("workflows/old-route.md") != "old\n" {
		t.Errorf("a server with no snapshot must deploy every head:\n%s", out)
	}
	if strings.Contains(out, "synced to snapshot") {
		t.Errorf("no snapshot exists, none should be reported:\n%s", out)
	}
	if _, ok := b.base("skills"); ok {
		t.Error("a deploy with no snapshot recorded a base")
	}
}

func TestSnapshotFirstClaimCarriesForwardOrPrunes(t *testing.T) {
	record := func(t *testing.T, f *snapFake) snapsync.Record {
		t.Helper()
		raw, _, ok := f.cfg.get("probe", snapsyncRecord("skills"), 0)
		if !ok {
			t.Fatal("no skills record on the hosted copy")
		}
		r, err := snapsync.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	t.Run("carries forward", func(t *testing.T) {
		ts, f := newSnapServer(t)
		seedCred(t, ts.URL)
		f.cfg.put("probe", "skills/theirs.md", []byte("theirs\n")) // a store that predates snapshots
		a := newSnapMachine(t, ts.URL)
		a.write("skills/mine.md", "mine\n")
		a.pushConfig()
		r := record(t, f)
		if _, ok := r.Files["skills/theirs.md"]; !ok || r.Files["skills/mine.md"] == "" {
			t.Errorf("the first claim must keep hosted files this tree lacks: %v", r.Files)
		}
		// And the next pull brings the carried-forward file down rather than
		// reading it as something this machine deleted.
		a.deploy()
		if a.read("skills/theirs.md") != "theirs\n" {
			t.Errorf("carried-forward file not pulled: %q", a.read("skills/theirs.md"))
		}
	})
	t.Run("prune", func(t *testing.T) {
		ts, f := newSnapServer(t)
		seedCred(t, ts.URL)
		f.cfg.put("probe", "skills/theirs.md", []byte("theirs\n"))
		a := newSnapMachine(t, ts.URL)
		a.write("skills/mine.md", "mine\n")
		a.use()
		cmd, buf := testCmd()
		cmd.Flags().Bool("prune", false, "")
		if err := cmd.Flags().Set("prune", "true"); err != nil {
			t.Fatal(err)
		}
		if err := runSyncConfigPush(cmd, ts.URL, "", false); err != nil {
			t.Fatalf("prune push: %v\n%s", err, buf.String())
		}
		if r := record(t, f); len(r.Files) != 1 || r.Files["skills/mine.md"] == "" {
			t.Errorf("--prune must publish this tree as the whole truth: %v", r.Files)
		}
		// The abandoned head is then never deployed.
		c := newSnapMachine(t, ts.URL)
		c.deploy()
		if c.has("skills/theirs.md") {
			t.Error("a pruned head was deployed")
		}
	})
}

// AC6: the first pull by a machine with no base, against a server that has a
// snapshot.
func TestSnapshotFirstPullWithNoBase(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a, c := newSnapMachine(t, ts.URL), newSnapMachine(t, ts.URL)

	a.write("workflows/done.md", "stale route\n")
	a.write("workflows/keep.md", "keep from A\n")
	a.write("workflows/x.md", "same\n")
	a.pushConfig()
	a.remove("workflows/done.md")
	a.pushConfig() // snapshot 2 no longer names done.md; its head remains
	if _, _, ok := f.cfg.get("probe", "workflows/done.md", 0); !ok {
		t.Fatal("test premise: done.md must remain a hosted head")
	}

	// C is a machine from before snapshots: a stale rehydrated done.md, one file
	// equal to the snapshot, one that differs, and one nobody published.
	c.write("workflows/done.md", "stale route\n")
	c.write("workflows/x.md", "same\n")
	c.write("workflows/keep.md", "keep from C\n")
	c.write("workflows/notes.md", "unpublished\n")
	out := c.deploy()

	if c.has("workflows/done.md") {
		t.Errorf("the stale done.md is still in the tree:\n%s", out)
	}
	if got := c.read("backups/sync-removed/workflows/done.md"); got != "stale route\n" {
		t.Errorf("backup of the removed file = %q", got)
	}
	if !strings.Contains(out, "removed (deleted on hosted copy; backup at backups/sync-removed/workflows/done.md): workflows/done.md") {
		t.Errorf("the removal is not reported:\n%s", out)
	}
	if c.read("workflows/notes.md") != "unpublished\n" || !strings.Contains(out, "local only — push to publish: workflows/notes.md") {
		t.Errorf("an unpublished local file must be kept and reported:\n%s", out)
	}
	if c.read("workflows/x.md") != "same\n" {
		t.Errorf("the file equal to the snapshot changed: %q", c.read("workflows/x.md"))
	}
	if base, _ := c.base("workflows"); base.Files["workflows/x.md"] == "" || base.Version != 2 {
		t.Errorf("the equal file must be adopted into the base: %+v", base)
	}
	if c.read("workflows/keep.md") != "keep from C\n" || c.read("backups/sync-conflicts/workflows/keep.md") != "keep from A\n" {
		t.Errorf("a differing file must be a conflict (local kept, remote parked): local=%q copy=%q",
			c.read("workflows/keep.md"), c.read("backups/sync-conflicts/workflows/keep.md"))
	}
	if !strings.Contains(out, "conflict (changed on both sides): workflows/keep.md") {
		t.Errorf("the conflict is not named:\n%s", out)
	}
}

// dryRunPush runs `satelle sync push --dry-run` as machine m.
func (m *snapMachine) dryRunPush() string {
	m.t.Helper()
	m.use()
	out, err := runRoot(m.t, "sync", "push", "--dry-run", "--server", m.url)
	if err != nil {
		m.t.Fatalf("sync push --dry-run: %v\n%s", err, out)
	}
	return out
}

// livePush runs `satelle sync push` as machine m.
func (m *snapMachine) livePush() {
	m.t.Helper()
	m.use()
	if out, err := runRoot(m.t, "sync", "push", "--server", m.url, "--dry-run=false"); err != nil {
		m.t.Fatalf("sync push: %v\n%s", err, out)
	}
}

// dryRunAreas is the set of areas a dry run names with a changed-file header.
func dryRunAreas(out string) map[string]bool {
	got := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 3 && strings.HasSuffix(f[0], ":") && f[2] == "changed" && f[1] != "0" {
			got[strings.TrimSuffix(f[0], ":")] = true
		}
	}
	return got
}

// linesNaming is every line of out that names path.
func linesNaming(out, path string) []string {
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, path) {
			got = append(got, line)
		}
	}
	return got
}

// sty_18273486: a dry run should name what a push would actually send — every
// file of an area this checkout never pushed, and after a push only the changed
// one — and contacts no server.
func TestSyncPushDryRunListsOnlyChangedArea(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := newSnapMachine(t, ts.URL)
	paths := []string{"skills/x.md", "skills/y.md", "workflows/w.md", "documents/d.md"}
	for _, p := range paths {
		a.write(p, p+"\n")
	}

	out := a.dryRunPush()
	for _, p := range paths {
		lines := linesNaming(out, p)
		if len(lines) != 1 || !strings.Contains(lines[0], "-> personal (never synced)") {
			t.Errorf("never-synced %s not listed as sent in full: %v\n%s", p, lines, out)
		}
	}
	if puts := f.putLog(); len(puts) != 0 {
		t.Errorf("--dry-run PUT %v", puts)
	}
	for _, area := range []string{"skills", "workflows", "documents"} {
		if _, ok := a.base(area); ok {
			t.Errorf("--dry-run recorded a %s base", area)
		}
	}

	a.livePush()
	a.write("skills/x.md", "edited\n")
	before := len(f.putLog())
	out = a.dryRunPush()
	if lines := linesNaming(out, "skills/x.md"); len(lines) != 1 || !strings.Contains(lines[0], "-> personal") {
		t.Errorf("the edited file is not listed: %v\n%s", lines, out)
	}
	for _, p := range paths[1:] {
		if lines := linesNaming(out, p); len(lines) != 0 {
			t.Errorf("unchanged %s listed: %v\n%s", p, lines, out)
		}
	}
	if !strings.Contains(out, "Would push 1 config file(s)") {
		t.Errorf("config leg should count only the changed file:\n%s", out)
	}
	if n := len(f.putLog()) - before; n != 0 {
		t.Errorf("--dry-run made %d PUT(s): %v", n, f.putLog()[before:])
	}
}

// sty_18273486: on areas every one of which has been pushed, the dry run names
// exactly the areas matching what sync status reports as unpushed.
func TestSyncPushDryRunAgreesWithStatus(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := newSnapMachine(t, ts.URL)
	a.write("skills/x.md", "x\n")
	a.write("workflows/w.md", "w\n")
	a.write("documents/d.md", "d\n")
	a.livePush()
	before := len(f.putLog())

	areas := []string{"skills", "workflows", "documents"}
	for _, step := range []struct {
		edit string
		want []string
	}{
		{"", nil},
		{"skills/x.md", []string{"skills"}},
		{"documents/d.md", []string{"skills", "documents"}},
		{"workflows/w.md", []string{"skills", "workflows", "documents"}},
	} {
		if step.edit != "" {
			a.write(step.edit, "edited\n")
		}
		statusOut, _ := a.status()
		unpushed := map[string]bool{}
		for _, area := range areas {
			if row(t, statusOut, area)[1] == snapsync.StateUnpushed {
				unpushed[area] = true
			}
		}
		dry := a.dryRunPush()
		named := dryRunAreas(dry)
		want := map[string]bool{}
		for _, area := range step.want {
			want[area] = true
		}
		if !reflect.DeepEqual(named, unpushed) || !reflect.DeepEqual(named, want) {
			t.Errorf("after editing %q: dry run named %v, status unpushed %v, want %v\ndry run:\n%s\nstatus:\n%s", step.edit, named, unpushed, want, dry, statusOut)
		}
		if len(want) == 0 && !strings.Contains(dry, "nothing to push — every area matches its last push") {
			t.Errorf("a clean tree's dry run does not say nothing to push:\n%s", dry)
		}
	}
	if n := len(f.putLog()) - before; n != 0 {
		t.Errorf("--dry-run made %d PUT(s): %v", n, f.putLog()[before:])
	}
}

// dryRunConfig runs `satelle sync config push --dry-run` as machine m.
func (m *snapMachine) dryRunConfig() string {
	m.t.Helper()
	m.use()
	cmd, buf := testCmd()
	if err := runSyncConfigPush(cmd, m.url, "", true); err != nil {
		m.t.Fatalf("sync config push --dry-run: %v\n%s", err, buf.String())
	}
	return buf.String()
}

// hasPut reports whether the fake received a PUT of key ("config:<path>").
func hasPut(puts []string, key string) bool { return slices.Contains(puts, key) }

// sty_195dd215 AC1, AC2, AC3: a push the server refuses one file of records only
// what it stored, still pushes every other file and the areas after it, names the
// refused file and exits non-zero; a task's run output never goes up; the next
// push sends the refused file.
func TestSnapshotPushRefusedFileStaysUnpushed(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := newSnapMachine(t, ts.URL)
	a.write("tasks/x.md", "x\n")
	a.write("tasks/y.md", "y\n")
	a.write("tasks/tsk_x/report.md", "a run output\n")
	f.refuseNext("tasks/x.md", 1)

	out, err := a.tryPushConfig()
	if err == nil {
		t.Fatalf("a push the server refused a file of exited zero:\n%s", out)
	}
	if !strings.Contains(err.Error(), "not stored: tasks/x.md") || !strings.Contains(err.Error(), "could not store file") {
		t.Errorf("the refused file is not named with the server's reason: %v", err)
	}
	puts := f.putLog()
	for _, want := range []string{"config:tasks/y.md", "config:" + config.ConfigName} {
		if !hasPut(puts, want) {
			t.Errorf("%s was not pushed after tasks/x.md was refused: %v", want, puts)
		}
	}
	base, ok := a.base("tasks")
	if !ok {
		t.Fatal("no tasks base saved")
	}
	if _, has := base.Files["tasks/x.md"]; has {
		t.Errorf("the tasks base records the refused tasks/x.md as pushed: %v", base.Files)
	}
	if _, has := base.Files["tasks/y.md"]; !has {
		t.Errorf("the tasks base lacks the stored tasks/y.md: %v", base.Files)
	}

	// Offline, the dry run compares with the last push alone, and still lists it.
	f.setDown(true)
	dry := a.dryRunConfig()
	f.setDown(false)
	if lines := linesNaming(dry, "tasks/x.md"); len(lines) != 1 || !strings.Contains(lines[0], "-> personal") {
		t.Errorf("an offline dry run does not list the refused file: %v\n%s", lines, dry)
	}
	if !strings.Contains(dry, "offline — compared with the last push only") {
		t.Errorf("an offline dry run does not say it is offline:\n%s", dry)
	}

	before := len(f.putLog())
	a.pushConfig()
	if !hasPut(f.putLog()[before:], "config:tasks/x.md") {
		t.Errorf("the re-run did not push tasks/x.md: %v", f.putLog()[before:])
	}
	if body, _, ok := f.cfg.get("probe", "tasks/x.md", 0); !ok || string(body) != "x\n" {
		t.Errorf("hosted tasks/x.md = %q (ok=%v), want %q", body, ok, "x\n")
	}
	for _, put := range f.putLog() {
		if strings.Contains(put, "tsk_x/") {
			t.Errorf("a task run output was pushed: %s", put)
		}
	}
}

// sty_195dd215 AC4: a file whose hosted head holds older bytes than the tree is
// listed by the dry run even though the last-push record matches the tree, and
// the real push re-publishes it.
func TestSnapshotDryRunListsHostedHeadMismatch(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := newSnapMachine(t, ts.URL)
	a.write(config.AgentsRel, tierAgents("current"))
	a.pushConfig()
	if base, _ := a.base("agents"); base.Files[config.AgentsRel] == "" {
		t.Fatalf("the premise: the agents base must match the tree: %+v", base)
	}
	f.cfg.put("probe", config.AgentsRel, []byte(tierAgents("older")))

	dry := a.dryRunConfig()
	if lines := linesNaming(dry, config.AgentsRel); len(lines) != 1 || !strings.Contains(lines[0], "hosted head differs") {
		t.Fatalf("the dry run does not list the mismatched head: %v\n%s", lines, dry)
	}
	before := len(f.putLog())
	a.pushConfig()
	if !hasPut(f.putLog()[before:], "config:"+config.AgentsRel) {
		t.Errorf("the push did not re-publish %s: %v", config.AgentsRel, f.putLog()[before:])
	}
	if body, _, _ := f.cfg.get("probe", config.AgentsRel, 0); string(body) != tierAgents("current") {
		t.Errorf("hosted head = %q, want the tree's bytes", body)
	}
	if dry := a.dryRunConfig(); !strings.Contains(dry, "nothing to push") {
		t.Errorf("after the push the dry run still lists changes:\n%s", dry)
	}
}

// sty_195dd215 AC5: a hosted copy holding a legacy bare agents.toml beside
// workflows/agents.toml deploys the agents layer only from workflows/agents.toml:
// no agents.toml at the data dir's root, and the project tier holds the
// workflows/agents.toml head's bytes, listed once in its stamp.
func TestDeployIgnoresLegacyBareAgentsKey(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := newSnapMachine(t, ts.URL)
	// The tree holds only the canonical agents layer: the fixture's legacy-path
	// copy would make a root agents.toml that deploy never wrote.
	a.remove(config.AgentsConfigName)
	current := tierAgents("current")
	a.write(config.AgentsRel, current)
	a.pushConfig()
	f.cfg.put("probe", config.AgentsConfigName, []byte("[reviewer-gpt]\nrole = \"reviewer\"\nmodel = \"legacy\"\n"))

	out := a.deploy()
	if a.has(config.AgentsConfigName) {
		t.Errorf("deploy wrote the legacy key to the data dir's root: %q\n%s", a.read(config.AgentsConfigName), out)
	}
	tier := config.ProjectTierDir(config.Config{}.ResolveRuntimeDir(a.repo).Dir)
	got, err := os.ReadFile(filepath.Join(tier, filepath.FromSlash(config.AgentsRel)))
	if err != nil || string(got) != current {
		t.Errorf("tier %s = %q (err %v), want the workflows/agents.toml head\n%s", config.AgentsRel, got, err, out)
	}
	stamp, ok := config.ReadProjectTierStamp(tier)
	if !ok {
		t.Fatalf("no tier stamp\n%s", out)
	}
	n := 0
	for _, p := range stamp.Files {
		if p == config.AgentsRel {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the tier stamp lists %s %d time(s), want once: %v", config.AgentsRel, n, stamp.Files)
	}
}
