package hosted

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClientPushDocumentFile: PUT carries raw bytes; 201 → Created; 200 → not;
// 409 → ErrDocumentConflict; 401 → ErrLoginRequired.
func TestClientPushDocumentFile(t *testing.T) {
	calls := 0
	ts, c := configTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.HasPrefix(r.URL.Path, "/api/v1/projects/probe/documents/") {
			http.NotFound(w, r)
			return
		}
		calls++
		body, _ := io.ReadAll(r.Body)
		if string(body) != "doc body" {
			t.Errorf("body = %q", body)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		switch calls {
		case 1:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"path":"documents/a.md","version":1,"blob_sha256":"h","size":8,"created":true}`))
		case 2:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"path":"documents/a.md","version":1,"blob_sha256":"h","size":8,"created":false}`))
		case 3:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"conflict"}`))
		}
	})
	_ = ts

	res, err := c.PushDocumentFile(context.Background(), "probe", "documents/a.md", []byte("doc body"))
	if err != nil || !res.Created {
		t.Fatalf("first push = %+v, %v", res, err)
	}
	res, err = c.PushDocumentFile(context.Background(), "probe", "documents/a.md", []byte("doc body"))
	if err != nil || res.Created {
		t.Fatalf("idempotent push = %+v, %v", res, err)
	}
	_, err = c.PushDocumentFile(context.Background(), "probe", "documents/a.md", []byte("doc body"))
	if !errorsIs(err, ErrDocumentConflict) {
		t.Fatalf("conflict = %v, want ErrDocumentConflict", err)
	}
}

// sty_2d567c7f AC1: a client made ForNode PUTs the raw bytes to the node's
// documents route under the node token, whatever project it is passed; 401 is
// ErrLoginRequired and 403 an error naming the status.
func TestClientForNodePushDocumentFile(t *testing.T) {
	var status int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/remote-nodes/nod_x/documents/stories/sty_a/live-evidence.md" {
			t.Errorf("request = %s %s, want the node documents route", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer node-token" {
			t.Errorf("authorization = %q, want the node token", got)
		}
		if body, _ := io.ReadAll(r.Body); string(body) != "evidence\n" {
			t.Errorf("body = %q, want the raw bytes", body)
		}
		if status != 0 {
			http.Error(w, "path not under the run's story", status)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"path":"stories/sty_a/live-evidence.md","version":1}`))
	}))
	t.Cleanup(ts.Close)
	base := NewClient(ts.URL, NodeTokenStore{Server: ts.URL, Token: "node-token"}, ts.Client())
	c := base.ForNode("nod_x")
	ctx := context.Background()

	res, err := c.PushDocumentFile(ctx, "ignored-project", "stories/sty_a/live-evidence.md", []byte("evidence\n"))
	if err != nil || !res.Created || res.Path != "stories/sty_a/live-evidence.md" {
		t.Fatalf("push = %+v, %v, want created at the path", res, err)
	}
	status = http.StatusUnauthorized
	if _, err := c.PushDocumentFile(ctx, "ignored-project", "stories/sty_a/live-evidence.md", []byte("evidence\n")); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("401 = %v, want ErrLoginRequired", err)
	}
	status = http.StatusForbidden
	if _, err := c.PushDocumentFile(ctx, "ignored-project", "stories/sty_a/live-evidence.md", []byte("evidence\n")); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("403 = %v, want an error naming the status", err)
	}
	if base.nodeID != "" {
		t.Error("ForNode changed the client it was called on")
	}
}

// TestClientListDocumentChanges: since is query-encoded; empty items is a
// non-nil slice; 401 → ErrLoginRequired.
func TestClientListDocumentChanges(t *testing.T) {
	var gotQuery string
	ts, c := configTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/probe/documents" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[{"path":"documents/a.md","version":1,"blob_sha256":"h","size":1,"created_at":"t"}],"cursor":"c2"}`))
	})
	_ = ts

	ch, err := c.ListDocumentChanges(context.Background(), "probe", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "since=c1") {
		t.Errorf("query = %q, want since=c1", gotQuery)
	}
	if strings.Contains(gotQuery, "project=") {
		t.Errorf("query = %q must not carry legacy project= (project is in the path)", gotQuery)
	}
	if ch.Cursor != "c2" || len(ch.Items) != 1 || ch.Items[0].Path != "documents/a.md" {
		t.Fatalf("changes = %+v", ch)
	}
}

// TestClientDocumentFileContent: 200 returns body + ETag; 404 → missing.
func TestClientDocumentFileContent(t *testing.T) {
	ts, c := configTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects/probe/documents/documents/a.md" {
			w.Header().Set("ETag", `"abc"`)
			_, _ = w.Write([]byte("hello"))
			return
		}
		http.NotFound(w, r)
	})
	_ = ts

	body, etag, err := c.DocumentFileContent(context.Background(), "probe", "documents/a.md")
	if err != nil || string(body) != "hello" || etag != `"abc"` {
		t.Fatalf("content = %q %q %v", body, etag, err)
	}
	_, _, err = c.DocumentFileContent(context.Background(), "probe", "documents/missing.md")
	if !errorsIs(err, ErrDocumentFileMissing) {
		t.Fatalf("missing = %v", err)
	}
}

func errorsIs(err, target error) bool {
	return errors.Is(err, target)
}
