package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/health"
)

// registerRepos writes a workspace registry into the isolated home.
func registerRepos(t *testing.T, roots ...string) {
	t.Helper()
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	gc.Workspace.Repos = roots
	if err := config.SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
}

// TestCheckAllReportsEveryRepoIndependently is AC2: a mixed registry yields one
// report per repo, in registry order, and no single bad repo hides the rest.
// That isolation is the whole point — a sweep that aborts at the first failure
// is unusable for an operator with several repositories.
func TestCheckAllReportsEveryRepoIndependently(t *testing.T) {
	healthy := newFixtureRepo(t, fixtureOpts{}) // also isolates SATELLE_HOME
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, ".satelle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".satelle", "agents.toml"), []byte("[reviewer\nnot toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	registerRepos(t, healthy, broken, missing)
	reports := CheckAll(context.Background(), Opts{})

	if len(reports) != 3 {
		t.Fatalf("want one report per registered repo, got %d", len(reports))
	}
	if reports[0].Repo != healthy || reports[1].Repo != broken || reports[2].Repo != missing {
		t.Errorf("registry order must be preserved: %s %s %s", reports[0].Repo, reports[1].Repo, reports[2].Repo)
	}
	if !reports[0].OK {
		t.Errorf("the healthy repo must still report healthy alongside broken ones: %v",
			reports[0].Findings.Details(health.SeverityError))
	}
	if reports[1].OK || !ids(reports[1])[health.IDAgentsLoad] {
		t.Errorf("a malformed agents.toml must be reported: %+v", reports[1].Findings)
	}
	if reports[2].OK || !ids(reports[2])[health.IDRepoUnreadable] {
		t.Errorf("a non-existent root must be reported, not skipped: %+v", reports[2].Findings)
	}

	healthyN, unhealthyN := Summarise(reports)
	if healthyN != 1 || unhealthyN != 2 {
		t.Errorf("summary = %d healthy, %d unhealthy", healthyN, unhealthyN)
	}
	if ExitCode(reports) != ExitUnhealthy {
		t.Error("one unhealthy repo makes the sweep exit 1")
	}
	var buf strings.Builder
	RenderText(&buf, reports, false, false)
	out := buf.String()
	for _, want := range []string{"HEALTHY " + healthy, "UNHEALTHY " + broken, "UNHEALTHY " + missing, "1 healthy, 2 unhealthy"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

// TestCheckAllEmptyRegistry pins the quiet case: no registered repos is not a
// defect.
func TestCheckAllEmptyRegistry(t *testing.T) {
	newFixtureRepo(t, fixtureOpts{}) // isolate the home
	if reports := CheckAll(context.Background(), Opts{}); len(reports) != 0 {
		t.Errorf("an empty registry yields no reports, got %+v", reports)
	}
}
