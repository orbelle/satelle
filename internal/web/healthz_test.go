package web

import (
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
)

func stampBuild(t *testing.T) {
	t.Helper()
	n, v, c, b := buildinfo.Name, buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime
	buildinfo.Name, buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime = "satelled", "1.0.0+0123456789ab", "0123456789ab", "2026-11-06T09:30:00Z"
	t.Cleanup(func() { buildinfo.Name, buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime = n, v, c, b })
}

// /healthz answers the build identity as JSON and keeps the instance header.
func TestHealthzReportsBuildIdentity(t *testing.T) {
	stampBuild(t)
	srv := mirrorWith(t, fixtureSnapshot())
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("X-Satelle-Instance") == "" {
		t.Fatalf("status %d, content-type %q, instance %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("X-Satelle-Instance"))
	}
	var got struct {
		Status string `json:"status"`
		buildinfo.Info
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Info != buildinfo.Resolve() {
		t.Fatalf("healthz = %+v, want ok and %+v", got, buildinfo.Resolve())
	}
}

// Every page's top bar and footer carry the build identity line: name,
// version, commit and build time.
func TestPagesShowBuildIdentity(t *testing.T) {
	stampBuild(t)
	srv := mirrorWith(t, fixtureSnapshot())
	info := buildinfo.Resolve()
	line := info.String()
	if !strings.Contains(line, info.Commit) || !strings.Contains(line, info.BuildTime) {
		t.Fatalf("build line %q lacks the commit or build time", line)
	}
	for _, path := range []string{"/", "/r/proj/"} {
		body := html.UnescapeString(httpGetBody(t, srv.URL+path))
		if !strings.Contains(body, `<span class="topbar-build" title="`+line+`">`+line+`</span>`) {
			t.Errorf("%s lacks the top bar build line %q", path, line)
		}
		if !strings.Contains(body, `<span class="footer-version">`+line+`</span>`) {
			t.Errorf("%s lacks the footer build line %q", path, line)
		}
	}
}
