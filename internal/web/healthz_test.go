package web

import (
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/mirror"
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

// The build identity line (name, version, commit, build time) is in every
// page's footer and not in its top bar.
func TestPagesShowBuildIdentityInTheFooterOnly(t *testing.T) {
	stampBuild(t)
	srv := mirrorWith(t, fixtureSnapshot())
	info := buildinfo.Resolve()
	line := info.String()
	if !strings.Contains(line, info.Commit) || !strings.Contains(line, info.BuildTime) {
		t.Fatalf("build line %q lacks the commit or build time", line)
	}
	for _, path := range []string{"/", "/r/proj/", "/logs"} {
		body := html.UnescapeString(httpGetBody(t, srv.URL+path))
		top, _, ok := strings.Cut(body, "</header>")
		if !ok {
			t.Fatalf("%s has no top bar", path)
		}
		if strings.Contains(top, info.Commit) {
			t.Errorf("%s: the top bar carries the build line", path)
		}
		if !strings.Contains(body, `<span class="footer-version">`+line+`</span>`) {
			t.Errorf("%s lacks the footer build line %q", path, line)
		}
	}
}

// The top bar shows the user the CLI's latest push says it is signed in as:
// an avatar with the name's first letter, name and email in its title, on
// every page (a project's and the machine-wide ones). Signed out, no one.
func TestTopBarShowsTheSignedInUser(t *testing.T) {
	signedOut := mirrorWith(t, fixtureSnapshot())
	for _, path := range []string{"/", "/r/proj/", "/logs"} {
		top, _, _ := strings.Cut(html.UnescapeString(httpGetBody(t, signedOut.URL+path)), "</header>")
		if strings.Contains(top, `class="avatar"`) {
			t.Errorf("%s: signed out, yet the top bar shows an avatar", path)
		}
	}
	snap := fixtureSnapshot()
	snap.Identity = raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p", User: &mirror.User{Name: "bobmcallan", Email: "bob@example.com"}})
	srv := mirrorWith(t, snap)
	want := `<span class="avatar" title="bobmcallan · bob@example.com" aria-label="Signed in as bobmcallan (bob@example.com)">B</span>`
	for _, path := range []string{"/", "/r/proj/", "/logs"} {
		top, _, _ := strings.Cut(html.UnescapeString(httpGetBody(t, srv.URL+path)), "</header>")
		if !strings.Contains(top, want) {
			t.Errorf("%s top bar lacks %s:\n%s", path, want, top)
		}
	}
}
