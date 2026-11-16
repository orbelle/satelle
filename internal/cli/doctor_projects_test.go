package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

func TestDoctorReportsAGoneProjectAndResolveRemovesIt(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	registerProjects(t, dir, gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	out, _ := run(t, "", "doctor", "--json")
	var rep doctorReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	var found []doctorIssue
	for _, is := range rep.Issues {
		if is.Check == checkProjectsName {
			found = append(found, is)
		}
	}
	if len(found) != 1 || found[0].Resolve != resolveRemoveProject || found[0].Fix != "satelle project remove "+gone {
		t.Fatalf("want one %s issue for %s fixed by %s, got %+v", checkProjectsName, gone, resolveRemoveProject, found)
	}
	if n := resolvable(loadDoctorInput(""), found, false); n != 1 {
		t.Errorf("resolvable without a terminal = %d, want 1: the fix needs no answer", n)
	}

	out, _ = run(t, "", "doctor", "--resolve", "--json")
	var res resolveReport
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("doctor --resolve --json: %v\n%s", err, out)
	}
	if !slices.ContainsFunc(res.Actions, func(a resolveAction) bool {
		return a.Check == checkProjectsName && a.Result == resultRemoved
	}) {
		t.Errorf("doctor --resolve did not remove %s:\n%s", gone, out)
	}
	if slices.ContainsFunc(res.Left, func(l resolveLeft) bool { return l.Check == checkProjectsName }) {
		t.Errorf("doctor --resolve left the gone project:\n%s", out)
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(gc.Projects.Paths, gone) || !slices.Contains(gc.Projects.Paths, dir) {
		t.Errorf("[projects] paths after --resolve = %v; want %s removed and %s kept", gc.Projects.Paths, gone, dir)
	}
	out, _ = run(t, "", "doctor", "--json")
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(rep.Issues, func(is doctorIssue) bool { return is.Check == checkProjectsName }) {
		t.Errorf("doctor still reports a gone project after --resolve:\n%s", out)
	}
}

func TestDoctorChecksProjectsOutsideARepo(t *testing.T) {
	isolate(t)
	gone := filepath.Join(t.TempDir(), "gone")
	registerProjects(t, gone)
	t.Chdir(t.TempDir())
	issues, _ := checkProjects(t.Context(), loadDoctorInput(""))
	if len(issues) != 1 || issues[0].path != gone {
		t.Fatalf("outside a repo: want the gone project %s, got %+v", gone, issues)
	}
	if !slices.ContainsFunc(doctorChecks(loadDoctorInput("")), func(c doctorCheck) bool { return c.name == checkProjectsName }) {
		t.Error("the projects check does not run outside a repo")
	}
}

func TestProjectPathGone(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, path string
		want       bool
	}{
		{"missing absolute path", filepath.Join(dir, "missing"), true},
		{"existing directory", dir, false},
		{"existing file", file, false},
		{"empty string", "", false},
		{"missing relative path", "no-such-relative-dir", false},
	} {
		if got := projectPathGone(c.path); got != c.want {
			t.Errorf("%s: projectPathGone(%q) = %v, want %v", c.name, c.path, got, c.want)
		}
	}
}
