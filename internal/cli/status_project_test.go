package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

const notAddedPhrase = "not one of this machine's projects"

func statusJSON(t *testing.T) (statusReport, string) {
	t.Helper()
	raw := must(t, "status", "--json")
	var rep statusReport
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatalf("status --json = %q: %v", raw, err)
	}
	return rep, raw
}

func assertNotAdded(t *testing.T, slug string) {
	t.Helper()
	out := must(t, "status")
	for _, want := range []string{slug, notAddedPhrase, "satelle project add"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	rep, raw := statusJSON(t)
	if rep.NotAdded == nil {
		t.Fatalf("status --json has no project_not_added: %s", raw)
	}
	if rep.NotAdded.Slug != slug || rep.NotAdded.Fix != "satelle project add" || rep.NotAdded.Project == "" {
		t.Errorf("project_not_added = %+v", *rep.NotAdded)
	}
}

func assertNoNotAdded(t *testing.T) {
	t.Helper()
	out := must(t, "status")
	if strings.Contains(out, notAddedPhrase) {
		t.Errorf("status warns of a not-added repo:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "project   ") {
			t.Errorf("status has a project line %q", line)
		}
	}
	rep, raw := statusJSON(t)
	if rep.NotAdded != nil || strings.Contains(raw, "project_not_added") {
		t.Errorf("status --json carries project_not_added: %s", raw)
	}
}

func TestStatusNamesABoundRepoThatIsNotAdded(t *testing.T) {
	t.Run("removed after bind", func(t *testing.T) {
		newMachine(t)
		srv := fakeserver.Start(t, testToken)
		must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
		must(t, "project", "remove")
		assertNotAdded(t, "shared")
		must(t, "project", "add")
		assertNoNotAdded(t)
	})
	t.Run("bound before bind added the repo", func(t *testing.T) {
		newMachine(t)
		r, err := findRepo("")
		if err != nil {
			t.Fatal(err)
		}
		if err := saveBinding(r, binding{Server: "https://satelle.dev", Project: "prj_shared", Slug: "shared"}); err != nil {
			t.Fatal(err)
		}
		assertNotAdded(t, "shared")
		must(t, "project", "add")
		assertNoNotAdded(t)
	})
}

func TestStatusHasNoNotAddedWarningWhenAdded(t *testing.T) {
	newMachine(t)
	srv := fakeserver.Start(t, testToken)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	assertNoNotAdded(t)
}

func TestStatusHasNoNotAddedWarningWhenUnbound(t *testing.T) {
	newMachine(t)
	assertNoNotAdded(t)
}
