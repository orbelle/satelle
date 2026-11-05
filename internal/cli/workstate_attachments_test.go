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
