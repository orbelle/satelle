package web

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/logfile"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func TestLogsNavOnLandingAndProject(t *testing.T) {
	ms := newLogsFixture(t)
	landing := servePath(t, ms, "/")
	if !strings.Contains(landing, `href="/logs"`) {
		t.Fatalf("landing missing /logs nav:\n%s", landing)
	}
	if strings.Contains(landing, "/r/demo/logs") {
		t.Fatal("landing must not link a per-project logs URL")
	}
	if i := strings.Index(landing, ">Projects</a>"); i < 0 {
		t.Fatal("landing missing Projects")
	} else if !strings.Contains(landing[i:], ">Logs</a>") {
		t.Fatal("Logs must follow Projects in the nav")
	}

	proj := servePath(t, ms, "/r/demo/")
	if !strings.Contains(proj, `href="/logs"`) {
		t.Fatalf("project page missing /logs nav:\n%s", proj)
	}
	if strings.Contains(proj, "/r/demo/logs") {
		t.Fatal("project page must not link /r/{slug}/logs")
	}

	page := servePath(t, ms, "/logs")
	if !strings.Contains(page, `class="active" aria-current="page">Logs</a>`) {
		t.Fatalf("Logs item must be active on /logs:\n%s", page)
	}
	if strings.Contains(page, `class="active" aria-current="page">Projects</a>`) {
		t.Fatal("Projects must not stay active on /logs")
	}
}

func TestLogsTailsActiveFileNewestFirst(t *testing.T) {
	ms := newLogsFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")
	ms.LogPath = path

	var b strings.Builder
	for i := 1; i <= 600; i++ {
		fmt.Fprintf(&b, "2026-08-15T00:00:00Z\tINFO\tGET\t/n/%d\t200\t1\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server-20260101-000000.log"), []byte("ROTATED-SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := servePath(t, ms, "/logs")
	if n := strings.Count(body, `class="log-row"`); n != 500 {
		t.Fatalf("want 500 rows, got %d", n)
	}
	first := logRowText(t, body, 0)
	if !strings.Contains(first, "/n/600") {
		t.Fatalf("first row should be last-written /n/600, got %q", first)
	}
	if strings.Contains(body, "/n/100") {
		t.Fatal("the 100th oldest line must have been dropped by the 500-line tail")
	}
	if strings.Contains(body, "ROTATED-SECRET") {
		t.Fatal("rotated sibling must not appear")
	}
	if !strings.Contains(body, `class="logs-path">`+path) {
		t.Fatalf("populated page must name the path:\n%s", body)
	}
}

func TestLogsEmptyAndMissingStillNamePath(t *testing.T) {
	ms := newLogsFixture(t)
	missing := filepath.Join(t.TempDir(), "no-such-server.log")
	ms.LogPath = missing
	body := servePath(t, ms, "/logs")
	if !strings.Contains(body, `class="logs-path">`+missing) {
		t.Fatalf("missing file must still name the path:\n%s", body)
	}
	if !strings.Contains(body, "No log lines yet") {
		t.Fatalf("missing file should be an empty state:\n%s", body)
	}

	empty := filepath.Join(t.TempDir(), "server.log")
	if err := os.WriteFile(empty, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	ms.LogPath = empty
	body = servePath(t, ms, "/logs")
	if !strings.Contains(body, `class="logs-path">`+empty) {
		t.Fatalf("empty file must still name the path:\n%s", body)
	}
	if !strings.Contains(body, "No log lines yet") {
		t.Fatalf("empty file should be an empty state:\n%s", body)
	}
}

func TestLogsFilterKeepsUnstructured(t *testing.T) {
	ms := newLogsFixture(t)
	path := filepath.Join(t.TempDir(), "server.log")
	ms.LogPath = path
	content := strings.Join([]string{
		"2026-08-15T12:00:00Z\tINFO\tGET\t/ok\t200\t3",
		"2026-08-15T12:00:01Z\tWARN\tGET\t/slow\t200\t1500",
		"2026-08-15T12:00:02Z\tERROR\tGET\t/boom\t500\t9",
		"push failed hosted 505",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		url      string
		want     []string
		notWant  []string
		filterOn string
	}{
		{"/logs", []string{"/ok", "/slow", "/boom", "push failed hosted 505"}, nil, ""},
		{"/logs?level=info", []string{"/ok", "push failed hosted 505"}, []string{"/slow", "/boom"}, "info"},
		{"/logs?level=warn", []string{"/slow", "push failed hosted 505"}, []string{"/ok", "/boom"}, "warn"},
		{"/logs?level=error", []string{"/boom", "push failed hosted 505"}, []string{"/ok", "/slow"}, "error"},
	} {
		body := servePath(t, ms, tc.url)
		for _, w := range tc.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s missing %q:\n%s", tc.url, w, body)
			}
		}
		for _, n := range tc.notWant {
			if strings.Contains(body, n) {
				t.Errorf("%s unexpectedly contains %q", tc.url, n)
			}
		}
		un := unstructuredRow(t, body)
		if !strings.Contains(un, `class="log-time">—<`) || !strings.Contains(un, `class="log-level">—<`) {
			t.Errorf("%s unstructured row must use em dashes:\n%s", tc.url, un)
		}
		if strings.Contains(un, "INFO") || strings.Contains(un, "WARN") || strings.Contains(un, "ERROR") {
			t.Errorf("%s unstructured row must not invent a level:\n%s", tc.url, un)
		}
		if tc.filterOn != "" && !strings.Contains(body, `href="/logs?level=`+tc.filterOn+`"`) {
			t.Errorf("%s filter link missing", tc.url)
		}
	}
}

func TestLogsFragmentFiltersAndHasNoChrome(t *testing.T) {
	ms := newLogsFixture(t)
	path := filepath.Join(t.TempDir(), "server.log")
	ms.LogPath = path
	content := strings.Join([]string{
		"2026-08-15T12:00:00Z\tINFO\tGET\t/ok\t200\t3",
		"2026-08-15T12:00:02Z\tERROR\tGET\t/boom\t500\t9",
		"push failed hosted 505",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	body := servePath(t, ms, "/fragment/logs?level=error")
	if strings.Contains(body, "<html") || strings.Contains(body, ">Logs</a>") || strings.Contains(body, "<thead>") {
		t.Fatalf("fragment must be row markup only, not page chrome:\n%s", body)
	}
	if !strings.Contains(body, "/boom") || !strings.Contains(body, "push failed hosted 505") {
		t.Fatalf("fragment should keep ERROR + unstructured:\n%s", body)
	}
	if strings.Contains(body, "/ok") {
		t.Fatalf("fragment must apply the level filter:\n%s", body)
	}
	if n := strings.Count(body, `class="log-row"`); n != 2 {
		t.Fatalf("want 2 fragment rows, got %d:\n%s", n, body)
	}
}

func TestLogsReadOnlyAndOpen(t *testing.T) {
	ms := newLogsFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.RemoteAddr = "8.8.8.8:4321"
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("non-loopback GET /logs = %d, want 200", rr.Code)
	}

	post := httptest.NewRequest(http.MethodPost, "/logs", strings.NewReader("x"))
	pr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(pr, post)
	if pr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /logs = %d, want 405", pr.Code)
	}
}

func TestRequestLogSkipsLogsPaths(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "server.log")
	var notified int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := RequestLog(inner, logPath, logfile.DefaultConfig, func() { notified++ })

	for _, p := range []string{"/logs", "/fragment/logs"} {
		notified = 0
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if data, _ := os.ReadFile(logPath); len(data) != 0 {
			t.Fatalf("%s wrote a log line:\n%s", p, data)
		}
		if notified != 0 {
			t.Fatalf("%s published logs (%d)", p, notified)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "\tGET\t/\t200\t") {
		t.Fatalf("GET / should append: %q", data)
	}
	if notified != 1 {
		t.Fatalf("GET / should notify once, got %d", notified)
	}
}

func TestPublishLogsReachesEvents(t *testing.T) {
	ms := newLogsFixture(t)
	srv := httptest.NewServer(ms.Handler)
	t.Cleanup(srv.Close)

	got := make(chan string, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/events")
		if err != nil {
			got <- "err:" + err.Error()
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if sc.Text() == "data: logs" {
				got <- "logs"
				return
			}
		}
		got <- "eof"
	}()
	// Publish until the subscriber has registered and receives it.
	deadline := time.Now().Add(waitBudget)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond) // poll tick
		ms.Publish("logs")
		select {
		case v := <-got:
			if v != "logs" {
				t.Fatalf("events subscriber got %q", v)
			}
			return
		default:
		}
	}
	t.Fatal("timed out waiting for data: logs on /events")
}

func TestAppJSLogsUsesSingleEventSource(t *testing.T) {
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if n := strings.Count(src, "new EventSource"); n != 1 {
		t.Errorf("want exactly 1 EventSource, got %d", n)
	}
	if !strings.Contains(src, `ev.data === "logs"`) {
		t.Error("logs trigger must live on the existing EventSource listener")
	}
	if strings.Contains(src, "setInterval") && strings.Contains(src, "fragment/logs") {
		// A timer next to the logs fetch would be a poll. The only setInterval
		// in this file is the freshness ticker, which must not drive logs.
		idx := strings.Index(src, "fragment/logs")
		window := src[max(0, idx-200):min(len(src), idx+200)]
		if strings.Contains(window, "setInterval") || strings.Contains(window, "setTimeout") {
			t.Fatalf("logs fetch looks polled:\n%s", window)
		}
	}
}

func newLogsFixture(t *testing.T) *MirrorServer {
	t.Helper()
	isolateHome(t)
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.TouchPartition(ctx, "rk", "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceKind(ctx, "rk", "identity", []mirror.ItemRow{{
		ID: "meta", Payload: `{"project_name":"demo","repo_root":"/tmp/demo"}`,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return NewMirror(s)
}

func servePath(t *testing.T, ms *MirrorServer, path string) string {
	t.Helper()
	rr := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s = %d\n%s", path, rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func logRowText(t *testing.T, body string, n int) string {
	t.Helper()
	const mark = `class="log-row"`
	rest := body
	for i := 0; i <= n; i++ {
		j := strings.Index(rest, mark)
		if j < 0 {
			t.Fatalf("log-row %d missing", i)
		}
		rest = rest[j+len(mark):]
	}
	end := strings.Index(rest, "</tr>")
	if end < 0 {
		t.Fatal("unclosed log-row")
	}
	return rest[:end]
}

func unstructuredRow(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "push failed hosted 505")
	if i < 0 {
		t.Fatalf("unstructured line missing:\n%s", body)
	}
	start := strings.LastIndex(body[:i], "<tr")
	end := strings.Index(body[i:], "</tr>")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate unstructured row")
	}
	return body[start : i+end]
}
