package cli

// A node's end-of-run publish carries the story attachments made on the node
// (sty_2d567c7f): through the node documents route under the node token, into the
// project collection a local pull reads. Only satelle.dev is faked.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotenode"
)

const publishNodeID = "nod_x"

// fakeNodeDocs is satelle-server's node documents route (sty_8daf5634) over the
// project collection d: the node token as Bearer, the path fenced to the run's
// story, and per-file refusals a test injects.
type fakeNodeDocs struct {
	d      *fakeDocs
	token  string
	mu     sync.Mutex
	story  string         // the run's story; any other path is 403
	refuse map[string]int // file name → status to answer
	auths  []string
	stored []string // paths the route stored, in order
}

func (n *fakeNodeDocs) routes(mux *http.ServeMux, f *fakeWorkstateServer) {
	n.d.routes(mux, f)
	mux.HandleFunc("PUT /api/v1/remote-nodes/{id}/documents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		n.mu.Lock()
		n.auths = append(n.auths, r.Header.Get("Authorization"))
		status, story := n.refuse[path[strings.LastIndex(path, "/")+1:]], n.story
		n.mu.Unlock()
		switch {
		case r.Header.Get("Authorization") != "Bearer "+n.token || r.PathValue("id") != publishNodeID:
			http.Error(w, "node token missing or ended", http.StatusUnauthorized)
			return
		case !strings.HasPrefix(path, "stories/"+story+"/"):
			http.Error(w, "path "+path+" is not under the run's story", http.StatusForbidden)
			return
		case status != 0:
			http.Error(w, "refused "+path, status)
			return
		}
		body, _ := io.ReadAll(r.Body)
		n.d.mu.Lock()
		_, existed := n.d.files[path]
		n.d.files[path] = body
		n.d.seq++
		n.d.ver[path] = n.d.seq
		n.d.mu.Unlock()
		n.mu.Lock()
		n.stored = append(n.stored, path)
		n.mu.Unlock()
		if existed {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"path": path, "blob_sha256": sha256Hex(body)})
	})
}

func (n *fakeNodeDocs) seen() (auths, stored []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.auths...), append([]string(nil), n.stored...)
}

// nodeDocsServer is the fake satelle.dev with the node documents route beside the
// project's, and a member credential for a local pull.
func nodeDocsServer(t *testing.T) (url string, f *fakeWorkstateServer, n *fakeNodeDocs) {
	t.Helper()
	n = &fakeNodeDocs{d: newFakeDocs(), token: "node-tok", refuse: map[string]int{}}
	ts, f := newFakeWorkstateServerWith(t, n.routes)
	seedCred(t, ts.URL)
	return ts.URL, f, n
}

// publishOnNode seeds a run on a fresh node machine, attaches docs (name → body)
// to its subject and runs the node publish under the node token.
func publishOnNode(t *testing.T, url string, n *fakeNodeDocs, docs ...[2]string) (nodeRun, remotenode.PublishReport, string, error) {
	t.Helper()
	newCLIMachine(t, storiesOnlyToml).use(t)
	run := seedNodeRun(t)
	n.mu.Lock()
	n.story = run.subject
	n.mu.Unlock()
	for _, d := range docs {
		attachDoc(t, run.subject, d[0], "output", d[1])
	}
	rep, log, err := runNodePublishReport(t, nodeClient(url, n.token), url, "probe", publishNodeID, run)
	return run, rep, log, err
}

// sty_2d567c7f AC1, AC2: a document attached on the node reaches
// stories/<id>/<file>.md through the node route under the node token, with the
// bytes the node holds, is counted, and a local workstate pull then writes it.
func TestNodePublishCarriesAttachments(t *testing.T) {
	url, _, n := nodeDocsServer(t)
	run, rep, log, err := publishOnNode(t, url, n, [2]string{"live-evidence", "attached on the node"})
	if err != nil {
		t.Fatalf("node publish: %v\n%s", err, log)
	}
	path := "stories/" + run.subject + "/live-evidence.md"
	want := mustDocBody(t, run.subject, "live-evidence")
	auths, stored := n.seen()
	if len(stored) != 1 || stored[0] != path {
		t.Fatalf("node route stored %v, want only %s\n%s", stored, path, log)
	}
	for _, a := range auths {
		if a != "Bearer node-tok" {
			t.Errorf("a node documents PUT carried %q, want the node token", a)
		}
	}
	if got, _ := n.d.file(path); string(got) != want {
		t.Errorf("hosted bytes = %q, want the node's %q", got, want)
	}
	if n.d.putCount() != 0 {
		t.Errorf("member-route PUTs = %d, want none from a node", n.d.putCount())
	}
	if rep.Attachments != 1 || len(rep.AttachmentFailures) != 0 {
		t.Errorf("report = %+v, want 1 attachment and no failures", rep)
	}

	newCLIMachine(t, storiesOnlyToml).use(t)
	out := syncWorkstateCLI(t, url, "pull")
	if got := mustDocBody(t, run.subject, "live-evidence"); got != want {
		t.Errorf("pulled attachment = %q, want %q\n%s", got, want, out)
	}
}

// sty_2d567c7f AC1: a publish whose Apply fails still sends the attachments and
// returns its own error with the count.
func TestNodePublishUploadsAttachmentsWhenApplyFails(t *testing.T) {
	url, f, n := nodeDocsServer(t)
	f.mu.Lock()
	f.failOnPost = 1
	f.mu.Unlock()
	run, rep, log, err := publishOnNode(t, url, n, [2]string{"live-evidence", "survives a failed apply"})
	if err == nil || !strings.Contains(err.Error(), "apply workstate") {
		t.Fatalf("publish = %v, want its apply error\n%s", err, log)
	}
	if _, ok := n.d.file("stories/" + run.subject + "/live-evidence.md"); !ok || rep.Attachments != 1 {
		t.Errorf("hosted = %v, report = %+v: want the document sent and counted despite the failed apply", ok, rep)
	}
}

// sty_2d567c7f AC3: a document the server refuses is named, with the story and
// the server's answer, never dropped; the one it took is counted.
func TestNodePublishReportsRefusedAttachment(t *testing.T) {
	url, _, n := nodeDocsServer(t)
	n.refuse["b-second.md"] = http.StatusForbidden
	run, rep, log, err := publishOnNode(t, url, n, [2]string{"a-first", "one"}, [2]string{"b-second", "two"})
	if err != nil {
		t.Fatalf("node publish: %v\n%s", err, log)
	}
	if rep.Attachments != 1 || len(rep.AttachmentFailures) != 1 {
		t.Fatalf("report = %+v, want one sent and one refused\n%s", rep, log)
	}
	got := rep.AttachmentFailures[0]
	if !strings.HasPrefix(got, "story "+run.subject+" document b-second.md: ") || !strings.Contains(got, "403") {
		t.Errorf("failure = %q, want the story, the document and the 403", got)
	}
}

// sty_2d567c7f AC3: after a 401 the leg sends nothing more; the file it did not
// send is named too, not only the one refused.
func TestNodePublishNamesFilesSkippedAfter401(t *testing.T) {
	url, _, n := nodeDocsServer(t)
	n.refuse["a-first.md"] = http.StatusUnauthorized
	run, rep, log, err := publishOnNode(t, url, n, [2]string{"a-first", "one"}, [2]string{"b-second", "two"})
	if err != nil {
		t.Fatalf("node publish: %v\n%s", err, log)
	}
	if rep.Attachments != 0 || len(rep.AttachmentFailures) != 2 {
		t.Fatalf("report = %+v, want both named\n%s", rep, log)
	}
	first, second := rep.AttachmentFailures[0], rep.AttachmentFailures[1]
	if !strings.HasPrefix(first, "story "+run.subject+" document a-first.md: ") || !strings.Contains(first, hosted.ErrLoginRequired.Error()) {
		t.Errorf("first = %q, want the 401's login-required error", first)
	}
	if !strings.HasPrefix(second, "story "+run.subject+" document b-second.md: not sent") {
		t.Errorf("second = %q, want it named as not sent", second)
	}
}

// The failure line attachmentDest.upload writes is the one attachmentUploadErrors
// reads: a reworded line fails here, not silently in a node's report.
func TestAttachmentUploadErrorsReadsTheLegsLine(t *testing.T) {
	var errw bytes.Buffer
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hosted.DocumentSyncStatePathOverride = t.TempDir() + "/state.json"
	t.Cleanup(func() { hosted.DocumentSyncStatePathOverride = "" })
	d := &attachmentDest{
		put:     func(context.Context, string, []byte) error { return errors.New("hosted: PUT x: 403 Forbidden") },
		pending: map[string][]pendingAttachment{"sty_a": {{hostRel: "stories/sty_a/plan.md"}}},
	}
	d.upload(context.Background(), io.Discard, &errw, "http://x", t.TempDir(), 0)
	byPath, other := attachmentUploadErrors(errw.String() + "attachments: something else\n")
	if byPath["stories/sty_a/plan.md"] != "hosted: PUT x: 403 Forbidden" || len(other) != 1 {
		t.Errorf("parsed %v, other %v from %q", byPath, other, errw.String())
	}
}
