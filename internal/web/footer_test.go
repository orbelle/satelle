package web

import (
	"html"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
)

func stubInstalled(t *testing.T, ib InstalledBuilds) {
	t.Helper()
	prev := installedBuilds
	SetInstalledBuilds(func() InstalledBuilds { return ib })
	t.Cleanup(func() { installedBuilds = prev })
}

var footerRe = regexp.MustCompile(`(?s)<footer class="site-footer">.*?</footer>`)

func footerOf(t *testing.T, url string) string {
	t.Helper()
	f := footerRe.FindString(html.UnescapeString(httpGetBody(t, url)))
	if f == "" {
		t.Fatalf("%s has no site footer", url)
	}
	return f
}

// row is the footer row with this label, or "" when absent.
func row(footer, label string) string {
	re := regexp.MustCompile(`<span class="footer-build( mismatch)?"><span class="footer-label">` + label + `</span> .*?</span>(</span>)?`)
	return re.FindString(footer)
}

// The footer shows the installed satelle CLI, the running satelled and the
// installed satelled, each with version and commit, and marks and names the
// remedy for a disagreement by the rule satelle service status uses
// (sto_49b6e762).
func TestFooterBuildLines(t *testing.T) {
	stampBuild(t)
	running := buildinfo.Resolve().String()
	cli0 := buildinfo.Info{Name: "satelle", Version: "1.0.0", Commit: "cccccccccccc", BuildTime: "2026-11-06T09:00:00Z"}.String()
	cli1 := buildinfo.Info{Name: "satelle", Version: "1.0.1", Commit: "dddddddddddd", BuildTime: "2026-11-06T10:00:00Z"}.String()
	inst1 := buildinfo.Info{Name: "satelled", Version: "1.0.1", Commit: "eeeeeeeeeeee", BuildTime: "2026-11-06T10:00:00Z"}.String()

	cases := []struct {
		name     string
		ib       InstalledBuilds
		marked   []string
		unmarked []string
		remedies []string
		noRemedy []string
		cliLine  string
		instLine string
	}{
		{name: "matching", ib: InstalledBuilds{CLI: cli0, Daemon: running},
			unmarked: []string{"satelle", "running", "installed"}, noRemedy: []string{"satelle service restart", "satelle update"},
			cliLine: cli0, instLine: running},
		{name: "running differs", ib: InstalledBuilds{CLI: cli1, Daemon: inst1},
			marked: []string{"running", "installed"}, unmarked: []string{"satelle"},
			remedies: []string{"satelle service restart"}, noRemedy: []string{"satelle update"},
			cliLine: cli1, instLine: inst1},
		{name: "CLI differs", ib: InstalledBuilds{CLI: cli1, Daemon: running},
			marked: []string{"satelle", "installed"}, unmarked: []string{"running"},
			remedies: []string{"satelle update"}, noRemedy: []string{"satelle service restart"},
			cliLine: cli1, instLine: running},
		{name: "CLI unknown", ib: InstalledBuilds{CLI: "unknown (/x/satelle has not run since it was installed)", Daemon: running},
			unmarked: []string{"satelle", "running", "installed"}, noRemedy: []string{"satelle update", "satelle service restart"},
			cliLine: "unknown (/x/satelle has not run since it was installed)", instLine: running},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubInstalled(t, tc.ib)
			srv := mirrorWith(t, fixtureSnapshot())
			for _, path := range []string{"/", "/r/proj/", "/logs"} {
				f := footerOf(t, srv.URL+path)
				if !strings.Contains(f, `<span class="footer-version">`+running+`</span>`) {
					t.Errorf("%s: no running line %q:\n%s", path, running, f)
				}
				if r := row(f, "satelle"); !strings.Contains(r, tc.cliLine) {
					t.Errorf("%s: satelle row %q lacks %q", path, r, tc.cliLine)
				}
				if r := row(f, "installed"); !strings.Contains(r, tc.instLine) {
					t.Errorf("%s: installed row %q lacks %q", path, r, tc.instLine)
				}
				for _, l := range tc.marked {
					if !strings.Contains(row(f, l), "mismatch") {
						t.Errorf("%s: row %s is not marked:\n%s", path, l, f)
					}
				}
				for _, l := range tc.unmarked {
					if r := row(f, l); r == "" || strings.Contains(r, "mismatch") {
						t.Errorf("%s: row %s missing or marked:\n%s", path, l, f)
					}
				}
				for _, rem := range tc.remedies {
					if !strings.Contains(f, `class="footer-warning" role="status">`) || !strings.Contains(f, "<code>"+rem+"</code>") {
						t.Errorf("%s: no warning naming %s:\n%s", path, rem, f)
					}
				}
				for _, rem := range tc.noRemedy {
					if strings.Contains(f, "<code>"+rem+"</code>") {
						t.Errorf("%s: names %s:\n%s", path, rem, f)
					}
				}
				if len(tc.remedies) == 0 && strings.Contains(f, "footer-warning") {
					t.Errorf("%s: a warning with nothing to warn:\n%s", path, f)
				}
			}
		})
	}
}
