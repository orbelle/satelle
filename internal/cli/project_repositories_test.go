package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

func TestProjectShowAndListCarryRepositories(t *testing.T) {
	newMachine(t)
	srv := fakeserver.Start(t, testToken)

	must(t, "project", "create", "beta", "--bind", "--server", srv.URL)
	if out := must(t, "project", "show"); strings.Contains(out, "repo:") {
		t.Errorf("project show with no binding = %q, want no repo line", out)
	}

	srv.BindGitHubVia("beta", "orbelle/beta", 21)
	srv.BindGitHubVia("beta", "orbelle/beta-site", 22)

	out := must(t, "project", "show")
	for _, want := range []string{"repo:   orbelle/beta (installation 21)\n", "repo:   orbelle/beta-site (installation 22)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("project show = %q, want %q", out, want)
		}
	}

	out = must(t, "project", "list", "--hosted", "--json", "--server", srv.URL)
	var projects []struct {
		Slug         string `json:"slug"`
		Repositories []struct {
			Repository     string `json:"repository"`
			InstallationID int64  `json:"installation_id"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(out), &projects); err != nil {
		t.Fatalf("project list --hosted --json: %v\n%s", err, out)
	}
	if len(projects) != 1 || projects[0].Slug != "beta" {
		t.Fatalf("project list --hosted --json = %s", out)
	}
	r := projects[0].Repositories
	if len(r) != 2 || r[0].Repository != "orbelle/beta" || r[0].InstallationID != 21 || r[1].Repository != "orbelle/beta-site" || r[1].InstallationID != 22 {
		t.Errorf("repositories = %+v, want orbelle/beta (21) and orbelle/beta-site (22)", r)
	}
}
