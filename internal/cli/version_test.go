package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
)

// stampBuild sets the ldflag variables for one test.
func stampBuild(t *testing.T, version, commit, built string) {
	t.Helper()
	v, c, b := buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime
	buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime = version, commit, built
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime = v, c, b })
}

// satelle version and --version print the build identity line, dev marker and all.
func TestVersionPrintsBuildIdentity(t *testing.T) {
	stampBuild(t, "1.0.0+0123456789ab-dirty", "0123456789ab", "2026-11-06T09:30:00Z")
	want := buildinfo.Resolve().String() + "\n"
	for _, args := range [][]string{{"version"}, {"--version"}} {
		out, err := run(t, "", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if out != want {
			t.Errorf("%v printed %q, want %q", args, out, want)
		}
		for _, s := range []string{"1.0.0+0123456789ab-dirty", "commit 0123456789ab", "built 2026-11-06T09:30:00Z"} {
			if !strings.Contains(out, s) {
				t.Errorf("%v output %q lacks %q", args, out, s)
			}
		}
	}
}
