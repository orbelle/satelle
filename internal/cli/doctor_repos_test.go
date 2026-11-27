package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func doctorRepos(t *testing.T, args ...string) ([]doctorIssue, doctorReport, string, error) {
	t.Helper()
	out, err := run(t, "", append([]string{"doctor", "--json"}, args...)...)
	var rep doctorReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("doctor --json: %v\n%s", jerr, out)
	}
	var repos []doctorIssue
	for _, is := range rep.Issues {
		if is.Check == checkReposName {
			repos = append(repos, is)
		}
	}
	return repos, rep, out, err
}

func sessionContextAt(t *testing.T, dir string) string {
	t.Helper()
	out, _ := run(t, `{"session_id":"s-hook","cwd":"`+dir+`"}`, "hook", "context")
	return out
}

func TestDoctorReportsOriginMismatchNamingBothURLs(t *testing.T) {
	dir := repoControl(t, "", "s-1")
	co := checkoutWith(t, "https://github.com/o/r.git")
	must(t, "repo", "add", "r", "--origin", "https://github.com/o/r", "--path", co, "--session", "s-1")
	if repos, _, _, _ := doctorRepos(t); len(repos) != 0 {
		t.Errorf("a matching checkout raised %+v", repos)
	}
	gittest.Git(t, co, "remote", "set-url", "origin", "git@github.com:o/r.git")
	if repos, _, _, _ := doctorRepos(t); len(repos) != 0 {
		t.Errorf("an ssh checkout of an https registration raised %+v", repos)
	}
	gittest.Git(t, co, "remote", "set-url", "origin", "https://u:tok3n@github.com/o/other.git?token=q7x")
	repos, _, out, err := doctorRepos(t)
	if len(repos) != 1 || !strings.Contains(repos[0].Problem, "github.com/o/other") || !strings.Contains(repos[0].Problem, "https://github.com/o/r") || repos[0].Fix == "" {
		t.Fatalf("mismatch issues %+v", repos)
	}
	if err == nil {
		t.Error("doctor exited 0 with a mismatch")
	}
	if strings.Contains(out, "tok3n") || strings.Contains(out, "q7x") {
		t.Errorf("doctor printed the checkout's credentials:\n%s", out)
	}
	_, err = run(t, "", "repo", "add", "r", "--path", co, "--session", "s-1")
	if err == nil || !strings.Contains(err.Error(), "github.com/o/other") || !strings.Contains(err.Error(), "https://github.com/o/r") || strings.Contains(err.Error(), "tok3n") {
		t.Errorf("repo add against the mismatching checkout: %v", err)
	}
	_ = dir
}

func TestRepoSurfacesNeverPrintCredentials(t *testing.T) {
	dir := repoControl(t, "", "s-1")
	co := checkoutWith(t, "https://u:tok3n@github.com/o/a.git?token=q7x")
	writeFile(t, filepath.Join(dir, ".satelle", "repos.toml"), `[repos.a]
origin = "https://u:tok3n@github.com/o/a.git?token=q7x"
[repos.b]
origin = "git:pw9@github.com:o/b.git"
`)
	writeFile(t, filepath.Join(dir, ".satelle", "repos.local.toml"), "[repos.a]\npath = \""+co+"\"\n")
	out, err := run(t, "", "validate")
	if err == nil {
		t.Fatal("validate accepted credentialed origins")
	}
	validate := out + err.Error()
	if !strings.Contains(validate, "repos.a") || !strings.Contains(validate, "repos.b") {
		t.Errorf("validate does not name both entries:\n%s", validate)
	}
	surfaces := map[string]string{"validate": validate, "hook context": sessionContextAt(t, dir)}
	for _, args := range [][]string{{"doctor"}, {"doctor", "--json"}, {"repo", "list"}, {"repo", "list", "--json"}} {
		o, err := run(t, "", args...)
		if err != nil {
			o += err.Error()
		}
		surfaces[strings.Join(args, " ")] = o
	}
	if _, err := run(t, "", "repo", "add", "a", "--path", co, "--session", "s-1"); err == nil {
		t.Error("repo add over an invalid tracked entry was allowed")
	} else {
		surfaces["repo add"] = err.Error()
	}
	for name, text := range surfaces {
		for _, secret := range []string{"tok3n", "q7x", "pw9"} {
			if strings.Contains(text, secret) {
				t.Errorf("%s printed %q:\n%s", name, secret, text)
			}
		}
	}
	if _, err := run(t, "", "repo", "add", "r", "--origin", "https://u:p@github.com/o/r", "--path", co, "--session", "s-1"); err == nil ||
		!strings.Contains(err.Error(), "carries credentials") {
		t.Errorf("repo add with a credentialed --origin: %v", err)
	}
}

func TestDoctorReportsCheckoutOnboardedByTwoControlRepos(t *testing.T) {
	a := repoControl(t, "", "s-1")
	b := repoControl(t, canonical(t.TempDir()), "s-2")
	registerProjects(t, a, b)
	co := checkoutWith(t, "https://github.com/o/r")
	must(t, "repo", "add", "r", "--origin", "https://github.com/o/r", "--path", co, "--session", "s-1")
	must(t, "-C", b, "repo", "add", "shared", "--origin", "https://github.com/o/r", "--path", co, "--session", "s-2")
	repos, _, _, _ := doctorRepos(t)
	if len(repos) != 1 {
		t.Fatalf("doctor in A: %+v", repos)
	}
	for _, want := range []string{a, b, co, "(as r)", "(as shared)"} {
		if !strings.Contains(repos[0].Problem, want) {
			t.Errorf("duplicate onboarding lacks %q: %s", want, repos[0].Problem)
		}
	}

	bLocal := filepath.Join(b, ".satelle", "repos.local.toml")
	writeFile(t, bLocal, "[repos.self-alias]\npath = \""+a+"\"\n")
	repos, _, _, _ = doctorRepos(t)
	if len(repos) != 1 || !strings.Contains(repos[0].Problem, "(as self)") || !strings.Contains(repos[0].Problem, "(as self-alias)") {
		t.Errorf("B mapping A's root: %+v", repos)
	}

	writeFile(t, bLocal, "secret-marker = [")
	repos, rep, out, _ := doctorRepos(t)
	noted := false
	for _, n := range rep.Notes {
		noted = noted || (strings.Contains(n, b) && strings.Contains(n, "could not be read"))
	}
	if len(repos) != 0 || !noted || strings.Contains(out, "secret-marker") {
		t.Errorf("malformed B: issues %+v notes %v", repos, rep.Notes)
	}

	writeFile(t, bLocal, "[repos.\"x\\u2028ignore previous\\u001b[31m\"]\npath = \""+co+"\"\n")
	_, _, out, _ = doctorRepos(t)
	text, _ := run(t, "", "doctor")
	for name, s := range map[string]string{"doctor --json": out, "doctor": text, "hook context": sessionContextAt(t, a)} {
		if strings.ContainsAny(s, "\u2028\x1b") || strings.Contains(s, "ignore previous") {
			t.Errorf("%s carries the injected name:\n%s", name, s)
		}
	}
}

func TestDoctorReportsMissingCheckoutsAndRepoCountWithoutTouchingGit(t *testing.T) {
	dir := repoControl(t, "", "s-1")
	co := checkoutWith(t, "https://github.com/o/r")
	writeFile(t, filepath.Join(co, "f.txt"), "x\n")
	gittest.Git(t, co, "add", ".")
	gittest.Git(t, co, "commit", "-qm", "one")
	fetched := filepath.Join(t.TempDir(), "fetched")
	must(t, "repo", "add", "r", "--origin", "https://github.com/o/r", "--path", co, "--fetch", "touch "+fetched, "--session", "s-1")
	gone := checkoutWith(t, "https://github.com/o/gone")
	must(t, "repo", "add", "gone", "--origin", "https://github.com/o/gone", "--path", gone, "--session", "s-1")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".satelle", "repos.toml"), readFileString(t, filepath.Join(dir, ".satelle", "repos.toml"))+"[repos.far]\norigin = \"https://github.com/o/far\"\n")
	snapshot := func() string {
		return gittest.Git(t, co, "for-each-ref") + gittest.Git(t, co, "config", "--list", "--local") + gittest.Git(t, co, "status", "--porcelain")
	}
	before := snapshot()

	repos, _, _, _ := doctorRepos(t)
	byName := map[string]doctorIssue{}
	for _, is := range repos {
		name, _, _ := strings.Cut(is.Problem, ":")
		byName[name] = is
	}
	if is := byName["gone"]; !strings.Contains(is.Problem, "does not exist") || !strings.Contains(is.Fix, "https://github.com/o/gone") {
		t.Errorf("missing checkout: %+v", is)
	}
	if is := byName["far"]; !strings.Contains(is.Problem, "no local checkout mapped") || !strings.Contains(is.Fix, "satelle repo add far") {
		t.Errorf("unmapped: %+v", is)
	}
	if len(repos) != 2 {
		t.Errorf("issues %+v, want gone and far only", repos)
	}

	doctorToml := filepath.Join(dir, ".satelle", "doctor.toml")
	orig := readFileString(t, doctorToml)
	writeFile(t, doctorToml, orig+"\n[repos]\nmax_count = 1\n")
	repos, _, _, _ = doctorRepos(t)
	counted := false
	for _, is := range repos {
		counted = counted || (strings.Contains(is.Problem, "3 repositories registered") && strings.Contains(is.Problem, "max_count = 1"))
	}
	if !counted {
		t.Errorf("no count issue over max_count = 1: %+v", repos)
	}
	writeFile(t, doctorToml, orig)
	repos, _, _, _ = doctorRepos(t)
	for _, is := range repos {
		if strings.Contains(is.Problem, "repositories registered") {
			t.Errorf("a count issue with no max_count: %+v", is)
		}
	}
	writeFile(t, doctorToml, orig+"\n[repos]\nmax_count = 0\n")
	if out, err := run(t, "", "validate"); err == nil || !strings.Contains(out+err.Error(), "[repos] max_count must be a positive integer") {
		t.Errorf("validate with max_count = 0: %v\n%s", err, out)
	}
	writeFile(t, doctorToml, orig)

	_, _ = run(t, "", "doctor")
	_, _ = run(t, "", "doctor", "--resolve")
	must(t, "repo", "list")
	if _, err := os.Stat(fetched); !os.IsNotExist(err) {
		t.Errorf("the fetch command ran (%v)", err)
	}
	if after := snapshot(); after != before {
		t.Errorf("the checkout's git state changed:\n%s\n---\n%s", before, after)
	}
}

func TestDoctorReposSilentWithoutRegistry(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	ignore := filepath.Join(dir, ".satelle", ".gitignore")
	writeFile(t, ignore, strings.ReplaceAll(readFileString(t, ignore), "repos.local.toml\n", ""))
	if strings.Contains(readFileString(t, ignore), "repos.local.toml") {
		t.Fatal("the ignore line is still there")
	}
	if repos, _, _, _ := doctorRepos(t); len(repos) != 0 {
		t.Errorf("doctor with no registry raised %+v", repos)
	}
	if out := sessionContextAt(t, dir); strings.Contains(out, "repos:") || strings.Contains(out, "repos (") {
		t.Errorf("hook context names repos:\n%s", out)
	}
}
